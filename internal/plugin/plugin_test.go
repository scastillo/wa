// Package plugin_test checks the Claude Code plugin files: the bin/wa launcher
// and the manifests. It has no Go code of its own.
package plugin_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const testVersion = "9.9.9"

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

type shim struct {
	root, home, fakeBin, ghLog string
	env                        []string
}

// newShim copies bin/wa and the real plugin.json (with a test version) into a
// fresh plugin root, so a local dist/wa in this checkout cannot answer for the
// release path. uname is faked; PATH has no gh unless a test adds one.
func newShim(t *testing.T, sysName, machine string) *shim {
	t.Helper()
	dir := t.TempDir()
	s := &shim{
		root:    filepath.Join(dir, "plugin"),
		home:    filepath.Join(dir, "home"),
		fakeBin: filepath.Join(dir, "fakebin"),
		ghLog:   filepath.Join(dir, "gh.log"),
	}
	launcher, err := os.ReadFile(filepath.Join(repoRoot(t), "bin", "wa"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(s.root, "bin", "wa"), string(launcher), 0o755)

	manifest, err := os.ReadFile(filepath.Join(repoRoot(t), ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p struct{ Version string }
	if err := json.Unmarshal(manifest, &p); err != nil || p.Version == "" {
		t.Fatalf("plugin.json version: %q %v", p.Version, err)
	}
	withTest := strings.Replace(string(manifest), `"`+p.Version+`"`, `"`+testVersion+`"`, 1)
	if withTest == string(manifest) {
		t.Fatal("could not set the test version in plugin.json")
	}
	write(t, filepath.Join(s.root, ".claude-plugin", "plugin.json"), withTest, 0o644)

	write(t, filepath.Join(s.fakeBin, "uname"),
		"#!/bin/sh\ncase $1 in -s) echo "+sysName+" ;; -m) echo "+machine+" ;; esac\n", 0o755)
	if err := os.MkdirAll(s.home, 0o700); err != nil {
		t.Fatal(err)
	}
	s.env = []string{"HOME=" + s.home, "PATH=" + s.fakeBin + ":/usr/bin:/bin", "GH_LOG=" + s.ghLog}
	return s
}

const fakeGh = `#!/bin/sh
echo "$*" >> "$GH_LOG"
pat= out=
while [ $# -gt 0 ]; do
  case $1 in -p) pat=$2; shift ;; -O) out=$2; shift ;; esac
  shift
done
case $pat in
  SHA256SUMS) cat "$FAKE_SUMS" ;;
  wa-darwin-*) cp "$FAKE_ASSET" "$out" ;;
  *) exit 1 ;;
esac
`

// withRelease adds a fake gh that serves asset as the binary and sums(sha of
// asset) as SHA256SUMS.
func (s *shim) withRelease(t *testing.T, asset string, sums func(sha string) string) {
	t.Helper()
	dir := filepath.Dir(s.ghLog)
	assetPath := filepath.Join(dir, "asset")
	write(t, assetPath, asset, 0o755)
	sum := sha256.Sum256([]byte(asset))
	sumsPath := filepath.Join(dir, "SHA256SUMS")
	write(t, sumsPath, sums(hex.EncodeToString(sum[:])), 0o644)
	write(t, filepath.Join(s.fakeBin, "gh"), fakeGh, 0o755)
	s.env = append(s.env, "FAKE_ASSET="+assetPath, "FAKE_SUMS="+sumsPath)
}

func (s *shim) run(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(filepath.Join(s.root, "bin", "wa"), args...)
	cmd.Env = env
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

func (s *shim) cache() string { return filepath.Join(s.home, ".local", "share", "wa", "bin") }

func (s *shim) ghCalls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(s.ghLog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func (s *shim) cacheEntries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(s.cache())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestShimDownloadsVerifiesAndCachesTheReleaseOnFirstRun(t *testing.T) {
	s := newShim(t, "Darwin", "x86_64")
	s.withRelease(t, "#!/bin/sh\necho release \"$@\"\n", func(sha string) string {
		// The arm64 line must not be picked for an x86_64 Mac.
		return strings.Repeat("0", 64) + "  wa-darwin-arm64\n" + sha + "  wa-darwin-amd64\n"
	})

	out, errOut, code := s.run(t, s.env, "read", "two words")
	if code != 0 || out != "release read two words\n" {
		t.Fatalf("first run: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	calls := s.ghCalls(t)
	for _, want := range []string{"release download v9.9.9", "-R scastillo/wa", "-p wa-darwin-amd64", "-p SHA256SUMS"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("gh calls miss %q:\n%s", want, calls)
		}
	}
	fi, err := os.Stat(filepath.Join(s.cache(), "wa-9.9.9-darwin-amd64"))
	if err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("cached binary: %v %v", fi, err)
	}
	if link, err := os.Readlink(filepath.Join(s.cache(), "wa")); err != nil || link != "wa-9.9.9-darwin-amd64" {
		t.Fatalf("stable link for the owner's terminal: %q %v", link, err)
	}
	if got := strings.Join(s.cacheEntries(t), " "); got != "wa wa-9.9.9-darwin-amd64" {
		t.Fatalf("cache must hold only the binary and the link, got %q", got)
	}

	write(t, s.ghLog, "", 0o644)
	out, errOut, code = s.run(t, s.env, "doctor")
	if code != 0 || out != "release doctor\n" || s.ghCalls(t) != "" {
		t.Fatalf("second run must use the cache: code %d, stdout %q, stderr %q, gh %q", code, out, errOut, s.ghCalls(t))
	}
}

func TestShimRejectsABinaryThatTheChecksumsDoNotVouchFor(t *testing.T) {
	for name, sums := range map[string]func(string) string{
		"wrong sum":        func(string) string { return strings.Repeat("ab", 32) + "  wa-darwin-arm64\n" },
		"no line for arch": func(sha string) string { return sha + "  wa-darwin-amd64\n" },
		"empty sums":       func(string) string { return "" },
	} {
		t.Run(name, func(t *testing.T) {
			s := newShim(t, "Darwin", "arm64")
			s.withRelease(t, "#!/bin/sh\necho tampered\n", sums)
			out, errOut, code := s.run(t, s.env, "doctor")
			if code != 1 || out != "" || !strings.Contains(errOut, "checksum") {
				t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
			}
			if got := s.cacheEntries(t); len(got) != 0 {
				t.Fatalf("nothing may stay in the cache, got %v", got)
			}
		})
	}
}

func TestShimReportsADownloadFailure(t *testing.T) {
	s := newShim(t, "Darwin", "arm64")
	write(t, filepath.Join(s.fakeBin, "gh"), "#!/bin/sh\necho \"$*\" >> \"$GH_LOG\"\necho 'HTTP 404: Not Found' >&2\nexit 1\n", 0o755)
	out, errOut, code := s.run(t, s.env, "doctor")
	if code != 1 || out != "" || !strings.Contains(errOut, "HTTP 404") || !strings.Contains(errOut, "gh auth status") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if got := s.cacheEntries(t); len(got) != 0 {
		t.Fatalf("nothing may stay in the cache, got %v", got)
	}
}

func TestShimExplainsHowToGetGh(t *testing.T) {
	if _, err := exec.LookPath("/usr/bin/gh"); err == nil {
		t.Skip("gh is in /usr/bin, so PATH cannot hide it")
	}
	s := newShim(t, "Darwin", "arm64")
	_, errOut, code := s.run(t, s.env, "doctor")
	if code != 1 || !strings.Contains(errOut, "gh auth login") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}

func TestShimRunsOnlyOnMacs(t *testing.T) {
	for _, c := range []struct{ sys, machine, want string }{
		{"Linux", "x86_64", "macOS"},
		{"Darwin", "ppc", "ppc"},
	} {
		s := newShim(t, c.sys, c.machine)
		s.withRelease(t, "#!/bin/sh\necho no\n", func(sha string) string { return sha + "  wa-darwin-arm64\n" })
		out, errOut, code := s.run(t, s.env, "doctor")
		if code != 1 || out != "" || !strings.Contains(errOut, c.want) || s.ghCalls(t) != "" {
			t.Fatalf("%s %s: code %d, stdout %q, stderr %q, gh %q", c.sys, c.machine, code, out, errOut, s.ghCalls(t))
		}
	}
}

func TestShimPrefersWABinThenALocalBuildThenTheCache(t *testing.T) {
	s := newShim(t, "Darwin", "arm64")
	write(t, filepath.Join(s.cache(), "wa-9.9.9-darwin-arm64"), "#!/bin/sh\necho cache \"$@\"\n", 0o755)
	local := filepath.Join(s.root, "dist", "wa")
	write(t, local, "#!/bin/sh\necho local \"$@\"\n", 0o755)
	custom := filepath.Join(s.home, "custom-wa")
	write(t, custom, "#!/bin/sh\necho custom \"$@\"\n", 0o755)

	steps := []struct {
		env  []string
		want string
	}{
		{append(append([]string{}, s.env...), "WA_BIN="+custom), "custom chats\n"},
		{s.env, "local chats\n"},
	}
	for _, st := range steps {
		if out, errOut, code := s.run(t, st.env, "chats"); code != 0 || out != st.want {
			t.Fatalf("want %q: code %d, stdout %q, stderr %q", st.want, code, out, errOut)
		}
	}
	if err := os.Rename(local, local+".off"); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := s.run(t, s.env, "chats"); code != 0 || out != "cache chats\n" {
		t.Fatalf("cache: code %d, stdout %q, stderr %q", code, out, errOut)
	}
	if calls := s.ghCalls(t); calls != "" {
		t.Fatalf("no step may download: %q", calls)
	}
}

func TestManifestsAgreeAndTheSkillShips(t *testing.T) {
	root := repoRoot(t)
	var plugin struct{ Name, Version string }
	readJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), &plugin)
	var market struct {
		Name    string
		Plugins []struct{ Name, Source, Version string }
	}
	readJSON(t, filepath.Join(root, ".claude-plugin", "marketplace.json"), &market)

	if plugin.Name != "wa" || !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(plugin.Version) {
		t.Fatalf("plugin.json: %+v", plugin)
	}
	if market.Name != "wa" || len(market.Plugins) != 1 || market.Plugins[0].Name != "wa" ||
		market.Plugins[0].Source != "./" || market.Plugins[0].Version != plugin.Version {
		t.Fatalf("marketplace.json must list this plugin at ./ with version %s: %+v", plugin.Version, market)
	}

	skill, err := os.ReadFile(filepath.Join(root, "skills", "whatsapp", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(skill), "---\nname: whatsapp\n") {
		t.Fatal("SKILL.md must start with frontmatter naming the whatsapp skill")
	}
	for _, banned := range []string{"~/projects", "go build", "GOTOOLCHAIN"} {
		if strings.Contains(string(skill), banned) {
			t.Fatalf("SKILL.md must call wa on PATH, not a checkout: found %q", banned)
		}
	}
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changelog), "## ["+plugin.Version+"]") {
		t.Fatalf("CHANGELOG.md has no entry for %s", plugin.Version)
	}
}
