package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion is the WhatsApp Desktop release whose data layout wa was built against.
const SchemaVersion = "26.34.15"

// coreDataEpoch is 2001-01-01T00:00:00Z as Unix seconds.
const coreDataEpoch = 978307200

// ChatKind says what sort of conversation a chat is.
type ChatKind string

const (
	KindDM         ChatKind = "dm"
	KindGroup      ChatKind = "group"
	KindStatus     ChatKind = "status"
	KindBroadcast  ChatKind = "broadcast"
	KindNewsletter ChatKind = "newsletter"
	KindOther      ChatKind = "other"
)

// Chat is one row of ZWACHATSESSION.
type Chat struct {
	PK            int64
	JID           string
	Name          string
	Kind          ChatKind
	LastMessageAt time.Time
	Unread        int
	Hidden        bool
}

// Store holds the databases wa reads. Contacts is nil when ContactsV2.sqlite is absent.
type Store struct {
	Dir      string
	Chat     *DB
	Contacts *DB

	book      map[string]string // address-book name by WhatsApp JID and by LID, loaded once
	pushNames map[string]string // profile push name by JID, loaded once
}

// required lists every table and column wa reads. CheckSchema fails when one is gone.
var required = map[string]map[string][]string{
	"ChatStorage": {
		"ZWACHATSESSION":     {"Z_PK", "ZCONTACTJID", "ZPARTNERNAME", "ZSESSIONTYPE", "ZLASTMESSAGEDATE", "ZUNREADCOUNT", "ZREMOVED", "ZHIDDEN"},
		"ZWAMESSAGE":         {"Z_PK", "ZCHATSESSION", "ZMESSAGEDATE", "ZISFROMME", "ZFROMJID", "ZTEXT", "ZMESSAGETYPE", "ZGROUPMEMBER", "ZMEDIAITEM", "ZSTANZAID"},
		"ZWAPROFILEPUSHNAME": {"ZJID", "ZPUSHNAME"},
		"ZWAMEDIAITEM":       {"Z_PK", "ZMEDIALOCALPATH", "ZMEDIAURL", "ZMEDIAKEY", "ZFILESIZE", "ZTITLE"},
		"ZWAGROUPMEMBER":     {"Z_PK", "ZMEMBERJID", "ZCONTACTNAME"},
		"Z_PRIMARYKEY":       {"Z_NAME", "Z_MAX"},
	},
	"ContactsV2": {
		"ZWAADDRESSBOOKCONTACT": {"ZWHATSAPPID", "ZLID", "ZFULLNAME"},
	},
}

// OpenStore opens ChatStorage.sqlite and, when present, ContactsV2.sqlite in dir.
func OpenStore(dir string) (*Store, error) {
	chat, err := Open(filepath.Join(dir, "ChatStorage.sqlite"))
	if err != nil {
		return nil, err
	}
	s := &Store{Dir: dir, Chat: chat}
	contacts := filepath.Join(dir, "ContactsV2.sqlite")
	if _, err := os.Stat(contacts); errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if s.Contacts, err = Open(contacts); err != nil {
		_ = chat.Close()
		return nil, err
	}
	return s, nil
}

// Close closes every open database.
func (s *Store) Close() error {
	err := s.Chat.Close()
	if s.Contacts != nil {
		err = errors.Join(err, s.Contacts.Close())
	}
	return err
}

// CheckSchema reports every required table column that is missing.
func (s *Store) CheckSchema() error {
	missing, err := missingColumns(s.Chat, required["ChatStorage"])
	if err != nil {
		return err
	}
	if s.Contacts != nil {
		more, err := missingColumns(s.Contacts, required["ContactsV2"])
		if err != nil {
			return err
		}
		missing = append(missing, more...)
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("schema drift: missing %s (WhatsApp Desktop changed its data layout; wa was built against %s)",
		strings.Join(missing, ", "), SchemaVersion)
}

func missingColumns(db *DB, tables map[string][]string) ([]string, error) {
	var missing []string
	for table, cols := range tables {
		have := map[string]bool{}
		err := Retry(func() error {
			rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return err
				}
				have[name] = true
			}
			return rows.Err()
		})
		if err != nil {
			return nil, fmt.Errorf("read columns of %s: %w", table, err)
		}
		for _, c := range cols {
			if !have[c] {
				missing = append(missing, table+"."+c)
			}
		}
	}
	return missing, nil
}

// Chats lists every chat that WhatsApp has not removed, newest first.
func (s *Store) Chats() ([]Chat, error) {
	var chats []Chat
	err := Retry(func() error {
		chats = chats[:0]
		rows, err := s.Chat.Query(`SELECT Z_PK, COALESCE(ZCONTACTJID, ''), COALESCE(ZPARTNERNAME, ''),
			COALESCE(ZSESSIONTYPE, -1), ZLASTMESSAGEDATE, COALESCE(ZUNREADCOUNT, 0), COALESCE(ZHIDDEN, 0)
			FROM ZWACHATSESSION WHERE COALESCE(ZREMOVED, 0) = 0
			ORDER BY ZLASTMESSAGEDATE DESC, Z_PK DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Chat
			var sessionType int
			var last sql.NullFloat64
			var hidden int
			if err := rows.Scan(&c.PK, &c.JID, &c.Name, &sessionType, &last, &c.Unread, &hidden); err != nil {
				return err
			}
			c.Kind = kindOf(sessionType, c.JID)
			c.Hidden = hidden != 0
			if last.Valid {
				c.LastMessageAt = FromCoreData(last.Float64)
			}
			chats = append(chats, c)
		}
		return rows.Err()
	})
	return chats, err
}

func kindOf(sessionType int, jid string) ChatKind {
	switch sessionType {
	case 0:
		return KindDM
	case 1:
		return KindGroup
	case 2:
		return KindBroadcast
	case 3:
		return KindStatus
	case 5:
		return KindNewsletter
	}
	if strings.HasSuffix(jid, "@g.us") {
		return KindGroup
	}
	return KindOther
}

// maxCoreData is 3000-01-01T00:00:00Z as a Core Data timestamp. Real sessions carry
// sentinel dates in the years 8026 and 9026 and beyond (measured 2026-09-14); those
// are not message times, and the largest would overflow time.Unix.
const maxCoreData = 31_525_372_800.0

// FromCoreData converts a Core Data timestamp to UTC time. A value that cannot be a
// real message time converts to the zero time.
func FromCoreData(v float64) time.Time {
	if math.IsNaN(v) || v > maxCoreData || v < -coreDataEpoch {
		return time.Time{}
	}
	sec, frac := math.Modf(v)
	return time.Unix(int64(sec)+coreDataEpoch, int64(frac*1e9)).UTC()
}

// ToCoreData converts a time to a Core Data timestamp.
func ToCoreData(t time.Time) float64 {
	return float64(t.Unix()-coreDataEpoch) + float64(t.Nanosecond())/1e9
}

// Resolve finds chats by exact JID, then row number, then a case-insensitive part
// of the name. It keeps the input order, so callers see newest chats first.
func Resolve(chats []Chat, query string) []Chat {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	for _, c := range chats {
		if c.JID == q {
			return []Chat{c}
		}
	}
	if pk, err := strconv.ParseInt(q, 10, 64); err == nil {
		for _, c := range chats {
			if c.PK == pk {
				return []Chat{c}
			}
		}
	}
	lq := strings.ToLower(q)
	var out []Chat
	for _, c := range chats {
		if strings.Contains(strings.ToLower(c.Name), lq) {
			out = append(out, c)
		}
	}
	return out
}
