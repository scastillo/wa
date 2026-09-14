package media

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/mediakey"
	"github.com/scastillo/wa/internal/mediakey/mediakeytest"
	"github.com/scastillo/wa/internal/store"
)

var (
	sent  = time.Date(2026, 9, 14, 16, 30, 0, 0, time.UTC)
	chat  = store.Chat{PK: 4242, JID: "555000@g.us", Name: "Book club", Kind: store.KindGroup}
	jpeg  = append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte("photo"), 40)...)
	key32 = bytes.Repeat([]byte{0x42}, 32)
)

type env struct {
	t         *testing.T
	container string
	dest      string
	srv       *httptest.Server
	hits      atomic.Int32
	bodies    map[string][]byte
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, container: t.TempDir(), dest: filepath.Join(t.TempDir(), "out"), bodies: map[string][]byte{}}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.hits.Add(1)
		body, ok := e.bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) onDisk(name string, content []byte) string {
	e.t.Helper()
	full := filepath.Join(e.container, "Message", "Media", name)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(full, content, 0o600); err != nil {
		e.t.Fatal(err)
	}
	return "Media/" + name
}

// serve publishes an encrypted body and returns a link that expires at exp.
func (e *env) serve(path string, plain []byte, exp time.Time) string {
	e.t.Helper()
	body, err := mediakeytest.Encrypt(key32, mediakey.Image, plain)
	if err != nil {
		e.t.Fatal(err)
	}
	e.bodies[path] = body
	return link(e.srv.URL+path, exp)
}

func link(base string, exp time.Time) string { return fmt.Sprintf("%s?oe=%X", base, exp.Unix()) }

func (e *env) opts(allowed bool, remote Remote) Options {
	return Options{
		Dest:      e.dest,
		Allowed:   allowed,
		Remote:    remote,
		MaxFetch:  100 << 20,
		Client:    e.srv.Client(),
		Now:       func() time.Time { return sent.Add(time.Hour) },
		Location:  cot,
		LocalPath: (&store.Store{Dir: e.container}).LocalPath,
	}
}

func mk(mediaPK int64, msgType int, sender, local, url string, size int64) store.Attachment {
	return store.Attachment{
		Message: store.Message{
			PK: mediaPK + 1000, ChatPK: chat.PK, SentAt: sent, Sender: sender, Type: msgType,
			Media: &store.Media{PK: mediaPK, Size: size},
		},
		StanzaID: fmt.Sprintf("ST%d", mediaPK), LocalPath: local, URL: url, MediaKey: mediakeytest.Blob(key32),
	}
}

func run(t *testing.T, e *env, o Options, atts ...store.Attachment) Result {
	t.Helper()
	res, err := Run(context.Background(), chat, atts, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func TestDiskCopySavesFilesAndARerunAddsNothing(t *testing.T) {
	e := newEnv(t)
	a := mk(1, 1, "Ada", e.onDisk("g/a.jpg", jpeg), "", int64(len(jpeg)))
	b := mk(2, 3, "me", e.onDisk("g/v.opus", []byte("OggS voice")), "", 10)

	res := run(t, e, e.opts(true, RemoteLive), a, b)
	if res.Found != 2 || res.Saved["disk"] != 2 {
		t.Fatalf("first run: %+v", res)
	}
	want := []string{
		"book-club_4242/2026-09-14_113000_ada_1.jpg",
		"book-club_4242/2026-09-14_113000_me_2.opus",
		"book-club_4242/manifest.jsonl",
	}
	if got := files(t, e.dest); !slices.Equal(got, want) {
		t.Fatalf("files:\n got  %v\n want %v", got, want)
	}
	saved := filepath.Join(e.dest, want[0])
	info, err := os.Stat(saved)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(saved)
	if !bytes.Equal(content, jpeg) || info.Mode().Perm() != 0o600 || !info.ModTime().Equal(sent) {
		t.Fatalf("saved file: equal=%v mode=%v mtime=%v", bytes.Equal(content, jpeg), info.Mode().Perm(), info.ModTime())
	}
	rows, skipped, err := LoadManifest(filepath.Join(e.dest, want[2]))
	if err != nil || skipped != 0 || len(rows) != 2 || rows[1].Status != "saved" || rows[1].Source != "disk" || rows[1].SHA256 == "" {
		t.Fatalf("manifest: %+v skipped=%d err=%v", rows, skipped, err)
	}

	again := run(t, e, e.opts(true, RemoteLive), a, b)
	if again.Already != 2 || len(again.Saved) != 0 || !slices.Equal(files(t, e.dest), want) {
		t.Fatalf("rerun: %+v files %v", again, files(t, e.dest))
	}
	manifest, _ := os.ReadFile(filepath.Join(e.dest, want[2]))
	if n := bytes.Count(manifest, []byte("\n")); n != 2 {
		t.Fatalf("a rerun must not append rows: %d lines", n)
	}
}

func TestLiveLinkIsFetchedDecryptedAndNamedByContent(t *testing.T) {
	e := newEnv(t)
	a := mk(3, 1, "Ada", "", e.serve("/ok", jpeg, sent.Add(48*time.Hour)), int64(len(jpeg)))

	res := run(t, e, e.opts(true, RemoteLive), a)
	if res.Saved["cdn"] != 1 {
		t.Fatalf("result: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(e.dest, "book-club_4242", "2026-09-14_113000_ada_3.jpg"))
	if err != nil || !bytes.Equal(got, jpeg) {
		t.Fatalf("decrypted file: %v equal=%v", err, bytes.Equal(got, jpeg))
	}
}

func TestBadMACLeavesNoFile(t *testing.T) {
	e := newEnv(t)
	url := e.serve("/bad", jpeg, sent.Add(48*time.Hour))
	e.bodies["/bad"][5] ^= 0x01
	a := mk(4, 1, "x", "", url, int64(len(jpeg)))

	res := run(t, e, e.opts(true, RemoteLive), a)
	if res.BadHMAC != 1 || len(res.Saved) != 0 {
		t.Fatalf("result: %+v", res)
	}
	for _, f := range files(t, e.dest) {
		if !strings.HasSuffix(f, "manifest.jsonl") {
			t.Fatalf("no media file may remain, found %s", f)
		}
	}
}

func TestUnavailableReasons(t *testing.T) {
	e := newEnv(t)
	live := sent.Add(48 * time.Hour)
	expired := mk(5, 1, "x", "", link(e.srv.URL+"/any", sent.Add(-time.Hour)), 10)
	gone := mk(6, 1, "x", "", link(e.srv.URL+"/gone", live), 10)
	noLink := mk(7, 1, "x", "Media/missing.jpg", "", 10)
	tooBig := mk(8, 2, "x", "", e.serve("/big", jpeg, live), 200<<20)

	res := run(t, e, e.opts(true, RemoteLive), expired, gone, noLink, tooBig)
	want := map[string]int{"expired-link": 2, "no-link": 1}
	if fmt.Sprint(res.Unavailable) != fmt.Sprint(want) || res.TooLarge != 1 {
		t.Fatalf("result: %+v", res)
	}
	if n := e.hits.Load(); n != 1 { // only /gone is requested; expired and too-large links are not
		t.Fatalf("CDN requests: got %d, want 1", n)
	}

	e.hits.Store(0)
	fresh := newEnv(t)
	res = run(t, fresh, fresh.opts(true, RemoteNone), mk(9, 1, "x", "", fresh.serve("/ok", jpeg, live), int64(len(jpeg))))
	if res.Unavailable["remote-disabled"] != 1 || fresh.hits.Load() != 0 {
		t.Fatalf("remote none: %+v hits=%d", res, fresh.hits.Load())
	}
	res = run(t, fresh, fresh.opts(true, RemoteAll), expired)
	if res.Unavailable["phone-fetch-not-set-up"] != 1 {
		t.Fatalf("remote all before phase 4: %+v", res)
	}
}

func TestMissingLocalFileFallsBackToALiveLink(t *testing.T) {
	e := newEnv(t)
	a := mk(10, 1, "x", "Media/deleted.jpg", e.serve("/ok", jpeg, sent.Add(48*time.Hour)), int64(len(jpeg)))
	if res := run(t, e, e.opts(true, RemoteLive), a); res.Saved["cdn"] != 1 {
		t.Fatalf("result: %+v", res)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	o := e.opts(true, RemoteLive)
	o.DryRun = true
	res := run(t, e, o,
		mk(11, 1, "x", e.onDisk("a.jpg", jpeg), "", int64(len(jpeg))),
		mk(12, 1, "x", "", e.serve("/ok", jpeg, sent.Add(48*time.Hour)), int64(len(jpeg))),
		mk(13, 1, "x", "", link(e.srv.URL+"/old", sent.Add(-time.Hour)), 10))
	if res.WouldCopy != 1 || res.WouldFetch != 1 || res.Unavailable["expired-link"] != 1 || e.hits.Load() != 0 {
		t.Fatalf("dry run: %+v hits=%d", res, e.hits.Load())
	}
	if _, err := os.Stat(e.dest); !os.IsNotExist(err) {
		t.Fatalf("dry run created %s", e.dest)
	}
}

func TestChatNotOnTheAllowlistGetsNoNamesOnDisk(t *testing.T) {
	e := newEnv(t)
	a := mk(14, 8, "Ana María", e.onDisk("d/contract.pdf", []byte("%PDF-1.7")), "", 8)
	a.Media.Title = "Contrato Final.pdf"
	run(t, e, e.opts(false, RemoteLive), a)
	got := files(t, e.dest)
	want := []string{"chat_4242/2026-09-14_113000_14.pdf", "chat_4242/manifest.jsonl"}
	if !slices.Equal(got, want) {
		t.Fatalf("files: %v", got)
	}
	manifest, _ := os.ReadFile(filepath.Join(e.dest, want[1]))
	for _, leak := range []string{"Ana", "Contrato", "Book club", "book-club"} {
		if bytes.Contains(manifest, []byte(leak)) {
			t.Fatalf("manifest leaked %q: %s", leak, manifest)
		}
	}
}
