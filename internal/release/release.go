// Package release tells wa whether a newer version of itself is out.
//
// The check runs at most once a day and keeps its answer in a small cache, so
// wa doctor stays fast and works offline: any failure means "no news".
package release

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LatestURL is the GitHub API for the newest wa release.
const LatestURL = "https://api.github.com/repos/scastillo/wa/releases/latest"

// Every is how long an answer stays good.
const Every = 24 * time.Hour

type cache struct {
	CheckedAt time.Time `json:"checked_at"`
	Tag       string    `json:"tag"`
}

// Latest returns the newest released version, such as "0.2.0". It fetches at
// most once a day and returns an empty string when it does not know.
func Latest(cachePath string, now time.Time, fetch func(url string) ([]byte, error)) string {
	if c, err := read(cachePath); err == nil && now.Sub(c.CheckedAt) < Every {
		return c.Tag
	}
	tag := ""
	if body, err := fetch(LatestURL); err == nil {
		var payload struct {
			TagName string `json:"tag_name"`
		}
		if json.Unmarshal(body, &payload) == nil {
			tag = strings.TrimPrefix(payload.TagName, "v")
		}
	}
	// Write the cache even for a failure, so a broken network is asked once a day.
	_ = write(cachePath, cache{CheckedAt: now, Tag: tag})
	return tag
}

// Newer reports whether latest is above current. Anything unparsable is "no".
func Newer(current, latest string) bool {
	c, okC := parse(current)
	l, okL := parse(latest)
	if !okC || !okL {
		return false
	}
	for i := range c {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	fields := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
	if len(fields) != 3 {
		return out, false
	}
	for i, f := range fields {
		n, err := strconv.Atoi(strings.SplitN(f, "-", 2)[0])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func read(path string) (cache, error) {
	var c cache
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	return c, err
}

func write(path string, c cache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
