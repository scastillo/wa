package media

import (
	"testing"
	"time"

	"github.com/scastillo/wa/internal/store"
)

var cot = time.FixedZone("COT", -5*3600)

func att(msgType int, sender, title, local string) store.Attachment {
	return store.Attachment{
		Message: store.Message{
			PK:     123456,
			SentAt: time.Date(2026, 9, 14, 16, 30, 0, 0, time.UTC),
			Sender: sender,
			Type:   msgType,
			Media:  &store.Media{PK: 700001, Size: 204800, Title: title},
		},
		LocalPath: local,
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Tío Pepe / 🎉":               "tío-pepe",
		"../../etc/passwd":           "etc-passwd",
		"   ":                        "",
		"A\u202eB\nC":                "a-b-c",
		"Contrato   Final (v2)":      "contrato-final-v2",
		"abcdefghijklmnopqrstuvwxyz": "abcdefghijklmnopqrst",
	}
	for in, want := range cases {
		if got := Slug(in, 20); got != want {
			t.Errorf("Slug(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestFileNameForAnAllowedChat(t *testing.T) {
	cases := []struct {
		name string
		a    store.Attachment
		want string
	}{
		{"image on disk keeps its extension", att(1, "Ada", "", "Media/555000@g.us/a/b/c.JPEG"),
			"2026-09-14_113000_ada_700001.jpeg"},
		{"document names itself after its title", att(8, "Ana María", "Contrato Final.PDF", ""),
			"2026-09-14_113000_ana-maría_700001_contrato-final.pdf"},
		{"own message", att(3, "me", "", "Media/x/voice.opus"),
			"2026-09-14_113000_me_700001.opus"},
		{"no sender name", att(2, "", "", ""),
			"2026-09-14_113000_unknown_700001.mp4"},
	}
	for _, c := range cases {
		if got := FileName(c.a, true, nil, cot); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func TestFileNameForAChatNotOnTheAllowlistHasNoNames(t *testing.T) {
	a := att(8, "Ana María", "Contrato Final.PDF", "")
	if got, want := FileName(a, false, nil, cot), "2026-09-14_113000_700001.pdf"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExtensionFromContentWhenNothingElseTellsIt(t *testing.T) {
	cases := []struct {
		msgType int
		head    []byte
		want    string
	}{
		{1, []byte{0xff, 0xd8, 0xff, 0xe0}, "jpg"},
		{1, []byte("\x89PNG\r\n\x1a\n"), "png"},
		{15, []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), "webp"},
		{2, []byte("\x00\x00\x00\x18ftypmp42"), "mp4"},
		{3, []byte("OggS\x00\x02"), "opus"},
		{8, []byte("%PDF-1.7"), "pdf"},
		{8, []byte("PK\x03\x04"), "zip"},
		{8, []byte("????"), "bin"},
		{1, nil, "jpg"},
		{3, nil, "opus"},
	}
	for _, c := range cases {
		a := att(c.msgType, "x", "", "")
		if got := Ext(a, c.head); got != c.want {
			t.Errorf("type %d head %q: got %q, want %q", c.msgType, c.head, got, c.want)
		}
	}
	// A local path extension wins, but only when it is a plain short extension.
	if got := Ext(att(1, "x", "", "Media/a/b.jpeg"), []byte("\x89PNG")); got != "jpeg" {
		t.Errorf("local extension: got %q", got)
	}
	if got := Ext(att(1, "x", "", "Media/a/b.j/p\x00g"), []byte{0xff, 0xd8, 0xff}); got != "jpg" {
		t.Errorf("unsafe local extension must be ignored: got %q", got)
	}
}

func TestChatDir(t *testing.T) {
	c := store.Chat{PK: 4242, Name: "Book club"}
	if got := ChatDir(c, true); got != "book-club_4242" {
		t.Errorf("allowed: got %q", got)
	}
	if got := ChatDir(c, false); got != "chat_4242" {
		t.Errorf("not allowed: got %q", got)
	}
}
