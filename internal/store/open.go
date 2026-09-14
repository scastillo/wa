// Package store opens the WhatsApp Desktop databases and runs every query wa needs.
//
// wa must never change WhatsApp's data. Three defences stack:
//   - mode=ro opens the file read-only;
//   - PRAGMA query_only rejects any write statement on the connection;
//   - when WhatsApp Desktop is closed (no -wal and no -shm file), immutable=1 stops
//     SQLite from creating -wal or -shm files in WhatsApp's container.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DefaultDir is WhatsApp Desktop's group container on macOS.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Group Containers", "group.net.whatsapp.WhatsApp.shared"), nil
}

// DB is one WhatsApp database, open read-only.
type DB struct {
	*sql.DB
	Path string
	// Immutable is true when WhatsApp Desktop had no -wal or -shm file at open time.
	Immutable bool
}

const (
	busyRetries = 5
	busyWait    = 200 * time.Millisecond
)

// ErrNeedsApp means SQLite must recover the WAL, which needs write access wa never takes.
var ErrNeedsApp = errors.New("WhatsApp data needs recovery: open WhatsApp Desktop, then retry")

// Open opens a WhatsApp database read-only and proves the file is readable.
func Open(path string) (*DB, error) {
	name := filepath.Base(path)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	immutable := !exists(path+"-wal") && !exists(path+"-shm")

	db, err := sql.Open("sqlite", dsn(path, immutable))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	db.SetMaxOpenConns(1)
	err = Retry(func() error {
		var n int
		return db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n)
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	return &DB{DB: db, Path: path, Immutable: immutable}, nil
}

// dsn builds a SQLite URI that cannot write.
func dsn(path string, immutable bool) string {
	u := url.URL{Scheme: "file", Path: path}
	q := "mode=ro&_pragma=query_only(1)"
	if immutable {
		q += "&immutable=1"
	}
	return u.String() + "?" + q
}

// Retry runs fn again while SQLite reports that WhatsApp holds a lock.
func Retry(fn func() error) error {
	var err error
	for attempt := 0; attempt <= busyRetries; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		switch code(err) {
		case sqlite3.SQLITE_READONLY_RECOVERY, sqlite3.SQLITE_READONLY_CANTINIT:
			return fmt.Errorf("%w (%v)", ErrNeedsApp, err)
		}
		if code(err)&0xff != sqlite3.SQLITE_BUSY {
			return err
		}
		if attempt < busyRetries {
			time.Sleep(busyWait)
		}
	}
	return fmt.Errorf("database stayed busy after %d retries: %w", busyRetries, err)
}

func code(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
