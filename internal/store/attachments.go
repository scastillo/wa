package store

import (
	"database/sql"
	"slices"
	"strings"
	"time"
)

// DefaultAttachmentTypes are image, video, audio and document. Stickers (15) need asking.
var DefaultAttachmentTypes = []int{1, 2, 3, 8}

// Attachment is one media message with everything needed to save its file.
type Attachment struct {
	Message
	StanzaID  string
	LocalPath string // ZMEDIALOCALPATH, relative to the Message folder
	URL       string
	MediaKey  []byte
}

// AttachmentQuery limits an attachment listing. Empty Types means DefaultAttachmentTypes.
type AttachmentQuery struct {
	Query
	Types []int
}

// Attachments lists the real files of one chat in date order. With a limit it keeps
// the newest. A media item counts only with a size above 0 and a path or a link.
func (s *Store) Attachments(chat Chat, q AttachmentQuery) ([]Attachment, error) {
	types := q.Types
	if len(types) == 0 {
		types = DefaultAttachmentTypes
	}
	marks := make([]string, len(types))
	args := []any{chat.PK}
	for i, t := range types {
		marks[i] = "?"
		args = append(args, t)
	}
	where, dateArgs := dateFilter(q.Query)
	args = append(args, dateArgs...)
	query := `SELECT m.Z_PK, m.ZMESSAGEDATE, COALESCE(m.ZISFROMME, 0), COALESCE(m.ZFROMJID, ''),
		COALESCE(m.ZTEXT, ''), COALESCE(m.ZMESSAGETYPE, 0), COALESCE(m.ZSTANZAID, ''),
		COALESCE(gm.ZMEMBERJID, ''), COALESCE(gm.ZCONTACTNAME, ''),
		mi.Z_PK, COALESCE(mi.ZMEDIALOCALPATH, ''), COALESCE(mi.ZMEDIAURL, ''), mi.ZFILESIZE,
		COALESCE(mi.ZTITLE, ''), mi.ZMEDIAKEY
		FROM ZWAMESSAGE m
		JOIN ZWAMEDIAITEM mi ON mi.Z_PK = m.ZMEDIAITEM
		LEFT JOIN ZWAGROUPMEMBER gm ON gm.Z_PK = m.ZGROUPMEMBER
		WHERE m.ZCHATSESSION = ? AND m.ZMESSAGETYPE IN (` + strings.Join(marks, ",") + `)
		  AND mi.ZFILESIZE > 0
		  AND (COALESCE(mi.ZMEDIALOCALPATH, '') != '' OR COALESCE(mi.ZMEDIAURL, '') != '')` +
		where + " ORDER BY m.ZMESSAGEDATE DESC, m.Z_PK DESC"
	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
	}

	book, push, err := s.names()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []Attachment
	err = Retry(func() error {
		out = out[:0]
		rows, err := s.Chat.Query(query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				a                  Attachment
				date               sql.NullFloat64
				fromMe             int
				fromJID, memberJID string
				contactName        string
				mediaPK, size      int64
				title              string
				key                []byte
			)
			if err := rows.Scan(&a.PK, &date, &fromMe, &fromJID, &a.Text, &a.Type, &a.StanzaID,
				&memberJID, &contactName, &mediaPK, &a.LocalPath, &a.URL, &size, &title, &key); err != nil {
				return err
			}
			a.ChatPK, a.ChatJID, a.ChatName = chat.PK, chat.JID, chat.Name
			if date.Valid {
				a.SentAt = FromCoreData(date.Float64)
			}
			a.FromMe = fromMe != 0
			a.SenderJID, a.Sender = sender(chat, a.FromMe, a.Type, fromJID, memberJID, contactName, book, push)
			a.MediaKey = slices.Clone(key)
			a.Media = &Media{PK: mediaPK, Size: size, Title: title, State: s.mediaState(a.LocalPath, a.URL, now)}
			out = append(out, a)
		}
		return rows.Err()
	})
	slices.Reverse(out)
	return out, err
}
