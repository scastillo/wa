// Package render turns messages into the lines wa prints.
//
// Message text is untrusted: it can carry newlines, terminal escapes and bidi
// overrides that disguise what a line says. Line removes all of them.
package render

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/scastillo/wa/internal/store"
)

// MaxRunes is the text length kept in a line unless Options.Full is set.
const MaxRunes = 900

// Options controls one rendering.
type Options struct {
	Location *time.Location
	Full     bool
	WithChat bool
}

// Line renders one message: [time | chat | sender] text [file …].
func Line(m store.Message, o Options) string {
	loc := o.Location
	if loc == nil {
		loc = time.Local
	}
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(m.SentAt.In(loc).Format("2006-01-02 15:04"))
	b.WriteString(" | ")
	if o.WithChat {
		b.WriteString(Clean(m.ChatName))
		b.WriteString(" | ")
	}
	b.WriteString(Clean(m.Sender))
	b.WriteString("]")
	if store.IsSystem(m.Type) {
		// Group events carry JSON metadata and notices carry app text; neither is a message.
		b.WriteString(" [system event]")
		return b.String()
	}

	text := Clean(m.Text)
	if !o.Full {
		text = cut(text, MaxRunes)
	}
	if text != "" {
		b.WriteString(" ")
		b.WriteString(text)
	}
	switch {
	case m.Media != nil:
		b.WriteString(" ")
		b.WriteString(mediaTag(m))
	case text == "":
		fmt.Fprintf(&b, " [type %d]", m.Type)
	}
	return b.String()
}

// TypeName names the message types wa knows; others print as type(N).
func TypeName(t int) string {
	switch t {
	case 0:
		return "text"
	case 1:
		return "image"
	case 2:
		return "video"
	case 3:
		return "audio"
	case 8:
		return "document"
	case 15:
		return "sticker"
	case 6, 10:
		return "system"
	}
	return fmt.Sprintf("type(%d)", t)
}

func mediaTag(m store.Message) string {
	tag := fmt.Sprintf("[file #%d %s %s %s", m.Media.PK, TypeName(m.Type), Size(m.Media.Size), m.Media.State)
	if title := Clean(m.Media.Title); title != "" {
		tag += ` "` + title + `"`
	}
	return tag + "]"
}

// Size formats a byte count as B, KB or MB with one decimal.
func Size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// Clean flattens whitespace to spaces and drops control and bidi characters.
func Clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r) || isBidi(r) || r == utf8.RuneError:
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func isBidi(r rune) bool {
	return r == '‎' || r == '‏' || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩')
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}
