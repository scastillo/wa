package render

import (
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/store"
)

var at = time.Date(2026, 9, 14, 16, 41, 0, 0, time.UTC)

func TestLineShowsTimeSenderTextAndMedia(t *testing.T) {
	cases := []struct {
		name string
		msg  store.Message
		want string
	}{
		{"text", store.Message{SentAt: at, Sender: "Ana", Text: "hola"},
			"[2026-09-14 11:41 | Ana] hola"},
		{"image with caption", store.Message{SentAt: at, Sender: "Carlos", Text: "see", Type: 1,
			Media: &store.Media{PK: 7, Size: 1258291, State: store.MediaOnDisk}},
			"[2026-09-14 11:41 | Carlos] see [file #7 image 1.2 MB on-disk]"},
		{"document title", store.Message{SentAt: at, Sender: "me", Type: 8,
			Media: &store.Media{PK: 9, Size: 2048, Title: "contract.pdf", State: store.MediaPhoneOnly}},
			"[2026-09-14 11:41 | me] [file #9 document 2.0 KB phone-only \"contract.pdf\"]"},
		{"unknown type", store.Message{SentAt: at, Sender: "unknown", Type: 42},
			"[2026-09-14 11:41 | unknown] [type 42]"},
		{"group event JSON", store.Message{SentAt: at, Sender: "system", Type: 6, Text: `{"subject":"Book club"}`},
			"[2026-09-14 11:41 | system] [system event]"},
		{"system notice", store.Message{SentAt: at, Sender: "system", Type: 10, Text: "notice"},
			"[2026-09-14 11:41 | system] [system event]"},
	}
	loc := time.FixedZone("COT", -5*3600)
	for _, c := range cases {
		if got := Line(c.msg, Options{Location: loc}); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func TestLineStripsControlAndBidiCharacters(t *testing.T) {
	msg := store.Message{SentAt: at, Sender: "Eve\u202e", Text: "a\nb\tc\x1b[31mred\u2066x\r"}
	got := Line(msg, Options{Location: time.UTC})
	want := "[2026-09-14 16:41 | Eve] a b c[31mredx"
	if got != want {
		t.Fatalf("\n got  %q\n want %q", got, want)
	}
}

func TestLineCutsLongTextUnlessFull(t *testing.T) {
	msg := store.Message{SentAt: at, Sender: "Ana", Text: strings.Repeat("ñ", 1000)}
	short := Line(msg, Options{Location: time.UTC})
	if n := strings.Count(short, "ñ"); n != 900 || !strings.HasSuffix(short, "…") {
		t.Fatalf("cut: %d runes kept, suffix %q", n, short[len(short)-3:])
	}
	full := Line(msg, Options{Location: time.UTC, Full: true})
	if strings.Count(full, "ñ") != 1000 {
		t.Fatal("full must keep every rune")
	}
}

func TestSearchLineNamesTheChat(t *testing.T) {
	msg := store.Message{SentAt: at, ChatName: "Family", Sender: "Ana", Text: "hola"}
	got := Line(msg, Options{Location: time.UTC, WithChat: true})
	if got != "[2026-09-14 16:41 | Family | Ana] hola" {
		t.Fatalf("got %q", got)
	}
}
