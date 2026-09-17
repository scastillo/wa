// Package policy holds the allowlist of chats whose text wa may print.
//
// The allowlist fails closed: a missing file allows no chat, and a file that
// cannot be read or parsed is an error, never an empty or permissive policy.
package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const version = 1

// Policy is the allowlist file. All opens every chat, now and in the future;
// the Allow list stays as it is, so turning All off restores it.
type Policy struct {
	Version int     `json:"version"`
	All     bool    `json:"allow_all,omitempty"`
	Allow   []Entry `json:"allow"`
}

// Entry is one allowed chat, keyed by its JID.
type Entry struct {
	JID   string `json:"jid"`
	Added string `json:"added,omitempty"`
}

// DefaultPath is ~/.config/wa/policy.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "wa", "policy.json"), nil
}

// Load reads the allowlist. A missing file is an empty allowlist.
func Load(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Policy{Version: version}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("policy %s is unreadable: %w", path, err)
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("policy %s is not valid JSON: %w (fix it or delete it)", path, err)
	}
	if p.Version != version {
		return nil, fmt.Errorf("policy %s: unsupported version %d", path, p.Version)
	}
	for i, e := range p.Allow {
		if !strings.Contains(e.JID, "@") {
			return nil, fmt.Errorf("policy %s: entry %d has no valid chat JID", path, i+1)
		}
	}
	return &p, nil
}

// Allowed reports whether wa may print text from the chat.
func (p *Policy) Allowed(jid string) bool {
	return p.All || slices.ContainsFunc(p.Allow, func(e Entry) bool { return e.JID == jid })
}

// Add allows a chat. It reports whether the allowlist changed.
func (p *Policy) Add(jid, today string) bool {
	if p.Allowed(jid) {
		return false
	}
	p.Allow = append(p.Allow, Entry{JID: jid, Added: today})
	return true
}

// Remove disallows a chat. It reports whether the allowlist changed.
func (p *Policy) Remove(jid string) bool {
	n := len(p.Allow)
	p.Allow = slices.DeleteFunc(p.Allow, func(e Entry) bool { return e.JID == jid })
	return len(p.Allow) != n
}

// Save writes the allowlist atomically with mode 0600.
func (p *Policy) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".policy-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	p.Version = version
	if err := enc.Encode(p); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
