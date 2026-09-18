// Package link installs wacli, the linked device wa sends through.
//
// wacli is MIT and ships macOS binaries, so wa downloads the pinned version
// from wacli's own release and checks it against that release's checksums.
// wa never redistributes it.
package link

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is the wacli release wa installs. wa depends on its flags
// (send text --to/--message, --lock-wait), so the version is pinned.
const Version = "0.18.2"

const releaseURL = "https://github.com/openclaw/wacli/releases/download"

// Fetch reads a URL. The caller supplies it, so tests never touch the network.
type Fetch func(url string) ([]byte, error)

// Asset is the wacli archive for this Mac.
func Asset(version, goarch string) string {
	arch := goarch
	if arch == "" {
		arch = runtime.GOARCH
	}
	return fmt.Sprintf("wacli_%s_darwin_%s.tar.gz", version, arch)
}

// Install puts the pinned wacli at dest, unless it is already there. It returns
// whether it downloaded anything.
func Install(dest, version, goarch string, fetch Fetch) (bool, error) {
	if version == "" {
		version = Version
	}
	if fi, err := os.Stat(dest); err == nil && fi.Mode()&0o111 != 0 {
		return false, nil
	}
	asset := Asset(version, goarch)
	base := fmt.Sprintf("%s/v%s", releaseURL, version)

	archive, err := fetch(base + "/" + asset)
	if err != nil {
		return false, fmt.Errorf("download wacli %s: %w", version, err)
	}
	sums, err := fetch(base + "/checksums.txt")
	if err != nil {
		return false, fmt.Errorf("download the wacli checksums: %w", err)
	}
	want, ok := checksum(string(sums), asset)
	if !ok {
		return false, fmt.Errorf("the wacli checksums name no %s", asset)
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != want {
		return false, fmt.Errorf("%s does not match the wacli checksum; nothing was installed", asset)
	}
	bin, err := unpack(archive)
	if err != nil {
		return false, err
	}
	if err := write(dest, bin); err != nil {
		return false, err
	}
	return true, nil
}

// checksum reads the line for one asset from a checksums.txt file.
func checksum(sums, asset string) (string, bool) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			return fields[0], true
		}
	}
	return "", false
}

// unpack returns the wacli binary from the release archive.
func unpack(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, fmt.Errorf("the wacli archive is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		head, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read the wacli archive: %w", err)
		}
		if head.Typeflag == tar.TypeReg && filepath.Base(head.Name) == "wacli" {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
	return nil, errors.New("the wacli archive holds no wacli binary")
}

// write installs the binary atomically, executable for the user only.
func write(dest string, bin []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	tmp := dest + ".part"
	if err := os.WriteFile(tmp, bin, 0o700); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
