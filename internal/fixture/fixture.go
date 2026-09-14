// Package fixture builds synthetic WhatsApp Desktop databases for tests.
//
// The tables come from the real schema of WhatsApp Desktop 26.34.15 (dumped with
// `.schema`, no rows). Every row a test needs is written by the test itself, so no
// real chat data ever enters this repository.
package fixture

import (
	"database/sql"
	"embed"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// SchemaVersion names the WhatsApp Desktop release the schema dump came from.
const SchemaVersion = "26.34.15"

//go:embed testdata/schema-26.34.15/*.sql
var schemas embed.FS

// Set is one group container with the three databases wa reads.
type Set struct {
	Dir         string
	ChatStorage string
	ContactsV2  string
	LID         string
}

// New creates the three databases in WAL mode inside a fresh temp dir and closes
// them, so SQLite removes the -wal and -shm files as a cleanly closed app would.
func New(t testing.TB) *Set {
	t.Helper()
	dir := t.TempDir()
	s := &Set{
		Dir:         dir,
		ChatStorage: filepath.Join(dir, "ChatStorage.sqlite"),
		ContactsV2:  filepath.Join(dir, "ContactsV2.sqlite"),
		LID:         filepath.Join(dir, "LID.sqlite"),
	}
	for name, path := range map[string]string{"ChatStorage": s.ChatStorage, "ContactsV2": s.ContactsV2, "LID": s.LID} {
		ddl, err := schemas.ReadFile("testdata/schema-" + SchemaVersion + "/" + name + ".sql")
		if err != nil {
			t.Fatalf("read schema %s: %v", name, err)
		}
		db := OpenWritable(t, path)
		if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
			t.Fatalf("%s: set WAL: %v", name, err)
		}
		if _, err := db.Exec(string(ddl)); err != nil {
			t.Fatalf("%s: apply schema: %v", name, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("%s: close: %v", name, err)
		}
	}
	return s
}

// OpenWritable opens a fixture database for writing test rows. Production code
// must never use it.
func OpenWritable(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	return db
}

// Exec writes to a fixture database and closes it again.
func Exec(t testing.TB, path, query string, args ...any) {
	t.Helper()
	db := OpenWritable(t, path)
	defer db.Close()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// Files lists the file names in dir, so a test can prove nothing new appeared.
func Files(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
