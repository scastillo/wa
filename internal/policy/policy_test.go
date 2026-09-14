package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMissingFileAllowsNothing(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Allowed("123@g.us") || len(p.Allow) != 0 {
		t.Fatal("a missing policy file must allow no chat")
	}
}

func TestBrokenFilesFailClosed(t *testing.T) {
	for name, body := range map[string]string{
		"not json":        "{allow:",
		"unknown version": `{"version": 2, "allow": []}`,
		"jid without @":   `{"version": 1, "allow": [{"jid": "family"}]}`,
		"empty jid":       `{"version": 1, "allow": [{"jid": ""}]}`,
	} {
		path := filepath.Join(t.TempDir(), "policy.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: want an error that names the file, got %v", name, err)
		}
	}
}

func TestUnreadableFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.Mkdir(path, 0o700); err != nil { // a directory cannot be read as a file
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("an unreadable policy must be an error, not an empty allowlist")
	}
}

func TestAddSaveLoadRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "policy.json")
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Add("123@g.us", "2026-09-14") {
		t.Fatal("first add must report a change")
	}
	if p.Add("123@g.us", "2026-09-15") {
		t.Fatal("second add of the same chat must report no change")
	}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("policy file mode: got %v, want 0600", info.Mode().Perm())
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Allowed("123@g.us") || again.Allowed("456@g.us") {
		t.Fatalf("allowlist after reload: %+v", again.Allow)
	}
	if !again.Remove("123@g.us") || again.Allowed("123@g.us") {
		t.Fatal("remove must drop the chat")
	}
	if again.Remove("123@g.us") {
		t.Fatal("removing an absent chat must report no change")
	}
}
