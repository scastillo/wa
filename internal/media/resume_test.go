package media

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLeftoverPartFilesAreRemoved(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dest, "book-club_4242")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-09-14_113000_x_1.jpg.part"), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, e, e.opts(true, RemoteLive), mk(1, 1, "x", e.onDisk("a.jpg", jpeg), "", int64(len(jpeg))))
	if res.PartsRemoved != 1 || res.Saved["disk"] != 1 {
		t.Fatalf("result: %+v", res)
	}
	for _, f := range files(t, e.dest) {
		if filepath.Ext(f) == ".part" {
			t.Fatalf("leftover part file: %s", f)
		}
	}
}

func TestFinalFileWithoutAManifestRow(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.dest, "book-club_4242")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	good := mk(1, 1, "x", e.onDisk("a.jpg", jpeg), "", int64(len(jpeg)))
	bad := mk(2, 1, "x", e.onDisk("b.jpg", jpeg), "", int64(len(jpeg)))
	// A crash after the rename but before the manifest row leaves a complete file (good).
	// A different file under the same name must not be trusted (bad).
	if err := os.WriteFile(filepath.Join(dir, "2026-09-14_113000_x_1.jpg"), jpeg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-09-14_113000_x_2.jpg"), []byte("someone else's file"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := run(t, e, e.opts(true, RemoteLive), good, bad)
	if res.Saved["adopted"] != 1 || res.Saved["disk"] != 1 || res.Conflicts != 1 {
		t.Fatalf("result: %+v", res)
	}
	want := []string{
		"book-club_4242/2026-09-14_113000_x_1.jpg",
		"book-club_4242/2026-09-14_113000_x_2.jpg",
		"book-club_4242/2026-09-14_113000_x_2.jpg.conflict",
		"book-club_4242/manifest.jsonl",
	}
	if got := files(t, e.dest); !slices.Equal(got, want) {
		t.Fatalf("files:\n got  %v\n want %v", got, want)
	}
	content, _ := os.ReadFile(filepath.Join(dir, "2026-09-14_113000_x_2.jpg"))
	if !bytes.Equal(content, jpeg) {
		t.Fatal("the conflicting file must be replaced by the real attachment")
	}
}

func TestSavedRowWithAMissingOrChangedFileIsFetchedAgain(t *testing.T) {
	e := newEnv(t)
	a := mk(1, 1, "x", e.onDisk("a.jpg", jpeg), "", int64(len(jpeg)))
	b := mk(2, 1, "x", e.onDisk("b.jpg", jpeg), "", int64(len(jpeg)))
	run(t, e, e.opts(true, RemoteLive), a, b)
	dir := filepath.Join(e.dest, "book-club_4242")
	if err := os.Remove(filepath.Join(dir, "2026-09-14_113000_x_1.jpg")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-09-14_113000_x_2.jpg"), []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := run(t, e, e.opts(true, RemoteLive), a, b)
	if res.Saved["disk"] != 2 || res.Already != 0 || res.Conflicts != 1 {
		t.Fatalf("result: %+v", res)
	}
	for _, name := range []string{"2026-09-14_113000_x_1.jpg", "2026-09-14_113000_x_2.jpg"} {
		content, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(content, jpeg) {
			t.Fatalf("%s must hold the real attachment again", name)
		}
	}
}

func TestAnUnfinishedLastManifestLineIsIgnored(t *testing.T) {
	e := newEnv(t)
	a := mk(1, 1, "x", e.onDisk("a.jpg", jpeg), "", int64(len(jpeg)))
	run(t, e, e.opts(true, RemoteLive), a)
	manifest := filepath.Join(e.dest, "book-club_4242", ManifestName)
	f, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"media_pk":2,"status":"sav`)
	_ = f.Close()

	rows, skipped, err := LoadManifest(manifest)
	if err != nil || skipped != 1 || len(rows) != 1 {
		t.Fatalf("rows %d skipped %d err %v", len(rows), skipped, err)
	}
	if res := run(t, e, e.opts(true, RemoteLive), a); res.Already != 1 {
		t.Fatalf("rerun: %+v", res)
	}
}
