package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
	"github.com/scastillo/wa/internal/store"
)

var photo = append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte("jpeg"), 64)...)

// seedMedia gives the allowed chat Family (1) and the chat Ana (3), which is not on
// the allowlist, one on-disk photo each.
func seedMedia(t *testing.T, h *harness) string {
	t.Helper()
	for _, name := range []string{"fam.jpg", "ana.jpg"} {
		p := filepath.Join(h.fx.Dir, "Message", "Media", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, photo, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := func(d time.Duration) float64 { return store.ToCoreData(now.Add(d)) }
	fixture.Exec(t, h.fx.ChatStorage, `INSERT INTO ZWAMEDIAITEM (Z_PK, ZMEDIALOCALPATH, ZFILESIZE) VALUES
		(1, 'Media/fam.jpg', ?), (2, 'Media/ana.jpg', ?)`, len(photo), len(photo))
	fixture.Exec(t, h.fx.ChatStorage, `INSERT INTO ZWAMESSAGE
		(Z_PK, ZCHATSESSION, ZMESSAGEDATE, ZISFROMME, ZFROMJID, ZGROUPMEMBER, ZMESSAGETYPE, ZMEDIAITEM, ZSTANZAID) VALUES
		(20, 1, ?, 0, '111@g.us', 1, 1, 1, 'S20'),
		(21, 3, ?, 0, '5731@s.whatsapp.net', NULL, 1, 2, 'S21')`, at(-30*time.Second), at(-40*time.Second))
	return t.TempDir()
}

func tree(t *testing.T, dir string) []string {
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

func TestMediaSavesAnAllowedChatWithNames(t *testing.T) {
	h := newHarness(t)
	dest := seedMedia(t, h)

	code, out, errOut := h.run(t, "media", "111@g.us", "--dest", dest)
	if code != 0 || !strings.Contains(out, "found 1") || !strings.Contains(out, "saved 1 (disk 1)") || !strings.Contains(out, "family_1") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	want := []string{"family_1/2026-09-14_115930_tio_1.jpg", "family_1/manifest.jsonl"}
	if got := tree(t, dest); !slices.Equal(got, want) {
		t.Fatalf("files: %v", got)
	}

	code, out, _ = h.run(t, "media", "111@g.us", "--dest", dest)
	if code != 0 || !strings.Contains(out, "already saved 1") {
		t.Fatalf("rerun: exit %d\n%s", code, out)
	}
}

func TestMediaForAChatNotOnTheAllowlistPrintsCountsOnly(t *testing.T) {
	h := newHarness(t)
	dest := seedMedia(t, h)

	code, out, errOut := h.run(t, "media", "Ana", "--dest", dest)
	if code != 0 || !strings.Contains(out, "saved 1 (disk 1)") || !strings.Contains(out, "chat_3") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	mustNotLeak(t, "media on a chat not on the allowlist", out, errOut)
	got := tree(t, dest)
	if !slices.Equal(got, []string{"chat_3/2026-09-14_115920_2.jpg", "chat_3/manifest.jsonl"}) {
		t.Fatalf("files: %v", got)
	}
	for _, f := range got {
		mustNotLeak(t, "file name", f)
	}

	code, out, errOut = h.run(t, "media", "Ana", "--dest", dest, "--json")
	if code != 0 || !strings.Contains(out, `"already":1`) {
		t.Fatalf("json: exit %d\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "media --json", out, errOut)
}

func TestMediaDryRunWritesNothing(t *testing.T) {
	h := newHarness(t)
	dest := filepath.Join(seedMedia(t, h), "out")

	code, out, errOut := h.run(t, "media", "111@g.us", "--dest", dest, "--dry-run")
	if code != 0 || !strings.Contains(out, "dry run") || !strings.Contains(out, "would copy 1") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("dry run created %s", dest)
	}
}

func TestMediaAmbiguousChatNamesOnlyAllowedCandidates(t *testing.T) {
	h := newHarness(t)
	seedMedia(t, h)
	code, out, errOut := h.run(t, "media", "family")
	if code != 2 || !strings.Contains(out+errOut, "111@g.us") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "ambiguous media", out, errOut)
}

func TestMediaRejectsBadFlags(t *testing.T) {
	h := newHarness(t)
	dest := seedMedia(t, h)
	for _, args := range [][]string{
		{"media", "111@g.us", "--type", "photo"},
		{"media", "111@g.us", "--remote", "maybe"},
		{"media", "111@g.us", "--before", "2026-09-01", "--until", "2026-09-02"},
		{"media"},
	} {
		if code, _, _ := h.run(t, append(args, "--dest", dest)...); code != 1 {
			t.Errorf("%v: exit %d, want 1", args, code)
		}
	}
	if got := tree(t, dest); len(got) != 0 {
		t.Fatalf("rejected runs must not write: %v", got)
	}
}

func TestMediaDefaultsToTheDownloadsFolder(t *testing.T) {
	h := newHarness(t)
	seedMedia(t, h)
	h.downloads = filepath.Join(t.TempDir(), "whatsapp")
	if code, out, errOut := h.run(t, "media", "111@g.us"); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if got := tree(t, h.downloads); len(got) != 2 {
		t.Fatalf("files under the default folder: %v", got)
	}
}
