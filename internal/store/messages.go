package store

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MediaState says where an attachment's bytes can come from.
type MediaState string

const (
	MediaOnDisk    MediaState = "on-disk"
	MediaLiveLink  MediaState = "live-link"
	MediaPhoneOnly MediaState = "phone-only"
)

// Media is the attachment of a message.
type Media struct {
	PK    int64
	Size  int64
	Title string
	State MediaState
}

// Message is one row of ZWAMESSAGE with its resolved sender.
type Message struct {
	PK        int64
	ChatPK    int64
	ChatJID   string
	ChatName  string
	SentAt    time.Time
	FromMe    bool
	SenderJID string
	Sender    string
	Text      string
	Type      int
	Media     *Media
}

// Query limits a message listing. Zero values mean no limit. Until is exclusive.
type Query struct {
	Since time.Time
	Until time.Time
	Limit int
}

const messageSelect = `SELECT m.Z_PK, m.ZCHATSESSION, m.ZMESSAGEDATE, COALESCE(m.ZISFROMME, 0),
	COALESCE(m.ZFROMJID, ''), COALESCE(m.ZTEXT, ''), COALESCE(m.ZMESSAGETYPE, 0),
	COALESCE(gm.ZMEMBERJID, ''), COALESCE(gm.ZCONTACTNAME, ''),
	mi.Z_PK, mi.ZMEDIALOCALPATH, mi.ZMEDIAURL, mi.ZFILESIZE, mi.ZTITLE
	FROM ZWAMESSAGE m
	LEFT JOIN ZWAGROUPMEMBER gm ON gm.Z_PK = m.ZGROUPMEMBER
	LEFT JOIN ZWAMEDIAITEM mi ON mi.Z_PK = m.ZMEDIAITEM`

// Messages lists the messages of one chat in date order. With a limit it keeps the newest.
func (s *Store) Messages(chat Chat, q Query) ([]Message, error) {
	where, args := dateFilter(q)
	query := messageSelect + " WHERE m.ZCHATSESSION = ?" + where + " ORDER BY m.ZMESSAGEDATE DESC, m.Z_PK DESC"
	args = append([]any{chat.PK}, args...)
	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
	}
	msgs, err := s.messages(query, args, map[int64]Chat{chat.PK: chat})
	slices.Reverse(msgs)
	return msgs, err
}

// Search finds text in the given chats only, newest first. No chats means no results.
func (s *Store) Search(text string, chats []Chat, q Query) ([]Message, error) {
	if len(chats) == 0 || strings.TrimSpace(text) == "" {
		return nil, nil
	}
	byPK := make(map[int64]Chat, len(chats))
	marks := make([]string, len(chats))
	args := make([]any, 0, len(chats)+4)
	for i, c := range chats {
		byPK[c.PK] = c
		marks[i] = "?"
		args = append(args, c.PK)
	}
	where, dateArgs := dateFilter(q)
	query := messageSelect + " WHERE m.ZCHATSESSION IN (" + strings.Join(marks, ",") + ")" +
		` AND m.ZTEXT LIKE ? ESCAPE '\'` + where + " ORDER BY m.ZMESSAGEDATE DESC, m.Z_PK DESC"
	args = append(args, "%"+escapeLike(text)+"%")
	args = append(args, dateArgs...)
	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
	}
	return s.messages(query, args, byPK)
}

func (s *Store) messages(query string, args []any, chats map[int64]Chat) ([]Message, error) {
	book, push, err := s.names()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []Message
	err = Retry(func() error {
		out = out[:0]
		rows, err := s.Chat.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				m                             Message
				date                          sql.NullFloat64
				fromMe                        int
				fromJID, memberJID, contactNm string
				mediaPK, size                 sql.NullInt64
				local, link, title            sql.NullString
			)
			if err := rows.Scan(&m.PK, &m.ChatPK, &date, &fromMe, &fromJID, &m.Text, &m.Type,
				&memberJID, &contactNm, &mediaPK, &local, &link, &size, &title); err != nil {
				return err
			}
			chat := chats[m.ChatPK]
			m.ChatJID, m.ChatName = chat.JID, chat.Name
			if date.Valid {
				m.SentAt = FromCoreData(date.Float64)
			}
			m.FromMe = fromMe != 0
			m.SenderJID, m.Sender = sender(chat, m.FromMe, m.Type, fromJID, memberJID, contactNm, book, push)
			// Measured: text and link rows carry media items with size 0 and no file.
			if mediaPK.Valid && size.Int64 > 0 && (local.String != "" || link.String != "") {
				m.Media = &Media{PK: mediaPK.Int64, Size: size.Int64, Title: title.String,
					State: s.mediaState(local.String, link.String, now)}
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// IsSystem reports rows WhatsApp writes itself: group events (6) and notices (10).
func IsSystem(msgType int) bool { return msgType == 6 || msgType == 10 }

// sender picks the name the owner sees in the app: the address book, then the profile
// push name, then the group contact name, then the DM chat name, then the JID.
// ZWAMESSAGE.ZPUSHNAME is never used: on real data it holds an ID on almost every
// incoming row (measured 2026-09-14).
func sender(chat Chat, fromMe bool, msgType int, fromJID, memberJID, contactName string, book, push map[string]string) (jid, name string) {
	if fromMe {
		return "", "me"
	}
	jid = memberJID
	if jid == "" && chat.Kind != KindGroup {
		jid = fromJID
		if jid == "" {
			jid = chat.JID
		}
	}
	switch {
	case jid != "" && book[jid] != "":
		return jid, book[jid]
	case jid != "" && push[jid] != "":
		return jid, push[jid]
	case contactName != "":
		return jid, contactName
	case chat.Kind == KindDM && chat.Name != "":
		return jid, chat.Name
	case jid != "":
		return jid, jid
	case IsSystem(msgType):
		return "", "system"
	}
	return "", "unknown"
}

// names loads the address book (by WhatsApp JID and by LID) and the profile push
// names (by JID) once per Store.
func (s *Store) names() (book, push map[string]string, err error) {
	if s.book != nil {
		return s.book, s.pushNames, nil
	}
	book = map[string]string{}
	push = map[string]string{}
	if s.Contacts != nil {
		err := Retry(func() error {
			rows, err := s.Contacts.Query(`SELECT COALESCE(ZWHATSAPPID, ''), COALESCE(ZLID, ''), ZFULLNAME
				FROM ZWAADDRESSBOOKCONTACT WHERE COALESCE(ZFULLNAME, '') != ''`)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var waID, lid, name string
				if err := rows.Scan(&waID, &lid, &name); err != nil {
					return err
				}
				if waID != "" {
					book[waID] = name
				}
				if lid != "" {
					book[lid] = name
				}
			}
			return rows.Err()
		})
		if err != nil {
			return nil, nil, err
		}
	}
	err = Retry(func() error {
		rows, err := s.Chat.Query(`SELECT ZJID, ZPUSHNAME FROM ZWAPROFILEPUSHNAME
			WHERE COALESCE(ZJID, '') != '' AND COALESCE(ZPUSHNAME, '') != ''`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var jid, name string
			if err := rows.Scan(&jid, &name); err != nil {
				return err
			}
			push[jid] = name
		}
		return rows.Err()
	})
	if err != nil {
		return nil, nil, err
	}
	s.book, s.pushNames = book, push
	return book, push, nil
}

// LocalPath returns the absolute path of an attachment inside WhatsApp's Message
// folder, or false when the stored path is empty or escapes that folder.
func (s *Store) LocalPath(rel string) (string, bool) {
	if rel == "" || s.Dir == "" {
		return "", false
	}
	base := filepath.Join(s.Dir, "Message")
	full := filepath.Join(base, rel)
	r, err := filepath.Rel(base, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

func (s *Store) mediaState(local, link string, now time.Time) MediaState {
	if full, ok := s.LocalPath(local); ok {
		if fi, err := os.Stat(full); err == nil && fi.Mode().IsRegular() {
			return MediaOnDisk
		}
	}
	if exp, ok := LinkExpiry(link); ok && exp.After(now.Add(time.Minute)) {
		return MediaLiveLink
	}
	return MediaPhoneOnly
}

// LinkExpiry reads the hex Unix time in a CDN link's oe parameter.
func LinkExpiry(link string) (time.Time, bool) {
	if link == "" {
		return time.Time{}, false
	}
	u, err := url.Parse(link)
	if err != nil {
		return time.Time{}, false
	}
	oe, err := strconv.ParseInt(u.Query().Get("oe"), 16, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(oe, 0), true
}

func dateFilter(q Query) (string, []any) {
	var where string
	var args []any
	if !q.Since.IsZero() {
		where += " AND m.ZMESSAGEDATE >= ?"
		args = append(args, ToCoreData(q.Since))
	}
	if !q.Until.IsZero() {
		where += " AND m.ZMESSAGEDATE < ?"
		args = append(args, ToCoreData(q.Until))
	}
	return where, args
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
