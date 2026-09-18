package release

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

func TestLatestAsksOnceADay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	calls := 0
	fetch := func(url string) ([]byte, error) {
		calls++
		if url != LatestURL {
			t.Fatalf("url: %s", url)
		}
		return []byte(`{"tag_name":"v0.9.1","name":"wa 0.9.1"}`), nil
	}
	if got := Latest(path, t0, fetch); got != "0.9.1" || calls != 1 {
		t.Fatalf("first call: %q after %d fetches", got, calls)
	}
	if got := Latest(path, t0.Add(23*time.Hour), fetch); got != "0.9.1" || calls != 1 {
		t.Fatalf("within a day it must use the cache: %q after %d fetches", got, calls)
	}
	if got := Latest(path, t0.Add(25*time.Hour), fetch); got != "0.9.1" || calls != 2 {
		t.Fatalf("after a day it must ask again: %q after %d fetches", got, calls)
	}
}

func TestLatestStaysQuietWhenItCannotAsk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	calls := 0
	fetch := func(string) ([]byte, error) {
		calls++
		return nil, errors.New("no network")
	}
	if got := Latest(path, t0, fetch); got != "" {
		t.Fatalf("offline must say nothing, got %q", got)
	}
	// And it must not retry on every command.
	if got := Latest(path, t0.Add(time.Minute), fetch); got != "" || calls != 1 {
		t.Fatalf("got %q after %d fetches", got, calls)
	}
}

func TestNewerComparesVersions(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.3", "0.2.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.2.0", "0.1.9", false},
		{"0.9.9", "1.0.0", true},
		{"dev", "0.2.0", false},
		{"0.2.0", "", false},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.current, c.latest, got)
		}
	}
}
