// Package media saves WhatsApp attachments to a folder.
package media

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/scastillo/wa/internal/store"
)

const (
	senderRunes = 40
	titleRunes  = 60
	chatRunes   = 40
)

// Slug keeps lowercase letters and digits and turns every other run of characters
// into one hyphen. It never keeps a path separator, a dot or a control character.
func Slug(s string, max int) string {
	var b strings.Builder
	hyphen := false
	n := 0
	for _, r := range s {
		if n >= max {
			break
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			hyphen = false
			n++
			continue
		}
		if !hyphen && b.Len() > 0 {
			b.WriteRune('-')
			hyphen = true
			n++
		}
	}
	return strings.Trim(b.String(), "-")
}

// FileName is <local time>_<sender>_<media PK>[_<document title>].<ext>. For a chat
// not on the allowlist it is <local time>_<media PK>.<ext>, with no names at all.
func FileName(a store.Attachment, allowed bool, head []byte, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	stamp := a.SentAt.In(loc).Format("2006-01-02_150405")
	ext := Ext(a, head)
	if !allowed {
		return fmt.Sprintf("%s_%d.%s", stamp, a.Media.PK, ext)
	}
	who := Slug(a.Sender, senderRunes)
	if who == "" {
		who = "unknown"
	}
	name := fmt.Sprintf("%s_%s_%d", stamp, who, a.Media.PK)
	if a.Type == 8 && a.Media.Title != "" {
		if t := Slug(strings.TrimSuffix(a.Media.Title, filepath.Ext(a.Media.Title)), titleRunes); t != "" {
			name += "_" + t
		}
	}
	return name + "." + ext
}

// Ext picks a file extension: the local file's, then the document title's, then one
// sniffed from the first bytes, then a default for the message type.
func Ext(a store.Attachment, head []byte) string {
	if e := safeExt(filepath.Ext(a.LocalPath)); e != "" {
		return e
	}
	if a.Media != nil {
		if e := safeExt(filepath.Ext(a.Media.Title)); e != "" {
			return e
		}
	}
	switch {
	case bytes.HasPrefix(head, []byte{0xff, 0xd8, 0xff}):
		return "jpg"
	case bytes.HasPrefix(head, []byte("\x89PNG")):
		return "png"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "webp"
	case len(head) >= 8 && string(head[4:8]) == "ftyp":
		return "mp4"
	case bytes.HasPrefix(head, []byte("OggS")):
		if a.Type == 3 {
			return "opus"
		}
		return "ogg"
	case bytes.HasPrefix(head, []byte("%PDF")):
		return "pdf"
	case bytes.HasPrefix(head, []byte("PK\x03\x04")):
		return "zip"
	}
	switch a.Type {
	case 1:
		return "jpg"
	case 2:
		return "mp4"
	case 3:
		return "opus"
	case 15:
		return "webp"
	}
	return "bin"
}

func safeExt(ext string) string {
	e := strings.ToLower(strings.TrimPrefix(ext, "."))
	if len(e) == 0 || len(e) > 5 {
		return ""
	}
	for _, r := range e {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return e
}

// ChatDir is <chat name>_<chat PK> for an allowed chat and chat_<chat PK> otherwise.
func ChatDir(c store.Chat, allowed bool) string {
	if allowed {
		if s := Slug(c.Name, chatRunes); s != "" {
			return fmt.Sprintf("%s_%d", s, c.PK)
		}
	}
	return fmt.Sprintf("chat_%d", c.PK)
}
