package link

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archive builds a wacli release tarball with the binary inside a folder.
func archive(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body string
	}{{"wacli_0.18.2_darwin_arm64/LICENSE", "MIT"}, {"wacli_0.18.2_darwin_arm64/wacli", body}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// release serves one archive and its checksums, and records what was asked for.
func release(t *testing.T, tarball []byte, sums string) (Fetch, *[]string) {
	t.Helper()
	var asked []string
	return func(url string) ([]byte, error) {
		asked = append(asked, url)
		switch {
		case strings.HasSuffix(url, "checksums.txt"):
			return []byte(sums), nil
		case strings.HasSuffix(url, ".tar.gz"):
			return tarball, nil
		}
		return nil, fmt.Errorf("404 %s", url)
	}, &asked
}

func TestInstallDownloadsChecksAndUnpacksWacli(t *testing.T) {
	tarball := archive(t, "#!/bin/sh\necho wacli\n")
	sums := "deadbeef  wacli_0.18.2_darwin_amd64.tar.gz\n" + sum(tarball) + "  wacli_0.18.2_darwin_arm64.tar.gz\n"
	fetch, asked := release(t, tarball, sums)
	dest := filepath.Join(t.TempDir(), "bin", "wacli")

	got, err := Install(dest, "", "arm64", fetch)
	if err != nil || !got {
		t.Fatalf("install: %v %v", got, err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != "#!/bin/sh\necho wacli\n" {
		t.Fatalf("binary: %q %v", body, err)
	}
	fi, _ := os.Stat(dest)
	if fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("wacli must be executable: %v", fi.Mode())
	}
	want := "https://github.com/openclaw/wacli/releases/download/v0.18.2/wacli_0.18.2_darwin_arm64.tar.gz"
	if (*asked)[0] != want {
		t.Fatalf("url:\n got  %s\n want %s", (*asked)[0], want)
	}

	// A second call does nothing.
	before := len(*asked)
	if got, err := Install(dest, "", "arm64", fetch); err != nil || got {
		t.Fatalf("second install: %v %v", got, err)
	}
	if len(*asked) != before {
		t.Fatalf("an installed wacli must not download again: %v", *asked)
	}
}

func TestInstallRefusesAnArchiveTheChecksumsDoNotVouchFor(t *testing.T) {
	tarball := archive(t, "tampered")
	for name, sums := range map[string]string{
		"wrong sum":        strings.Repeat("ab", 32) + "  wacli_0.18.2_darwin_arm64.tar.gz\n",
		"no line for this": sum(tarball) + "  wacli_0.18.2_darwin_amd64.tar.gz\n",
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			fetch, _ := release(t, tarball, sums)
			dest := filepath.Join(t.TempDir(), "wacli")
			_, err := Install(dest, "", "arm64", fetch)
			if err == nil || !strings.Contains(err.Error(), "checksum") {
				t.Fatalf("want a checksum error, got %v", err)
			}
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("nothing may be installed")
			}
		})
	}
}

func TestInstallReportsADownloadFailure(t *testing.T) {
	fetch := func(string) ([]byte, error) { return nil, errors.New("no network") }
	dest := filepath.Join(t.TempDir(), "wacli")
	if _, err := Install(dest, "", "arm64", fetch); err == nil || !strings.Contains(err.Error(), "no network") {
		t.Fatalf("want the network error, got %v", err)
	}
}

func TestAssetNamesTheMacArchive(t *testing.T) {
	if got := Asset(Version, "amd64"); got != "wacli_0.18.2_darwin_amd64.tar.gz" {
		t.Fatalf("asset: %s", got)
	}
}
