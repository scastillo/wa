package media

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
)

// ManifestName is the manifest file in each chat folder.
const ManifestName = "manifest.jsonl"

// Row is one manifest line. The last row for a media PK wins.
type Row struct {
	MediaPK   int64  `json:"media_pk"`
	MessagePK int64  `json:"message_pk"`
	StanzaID  string `json:"stanza_id,omitempty"`
	File      string `json:"file,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	At        string `json:"at"`
}

// LoadManifest reads a manifest. A missing file is empty. Lines that do not parse,
// such as a line cut short by a crash, are skipped and counted.
func LoadManifest(path string) (map[int64]Row, int, error) {
	rows := map[int64]Row{}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return rows, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	skipped := 0
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Row
		if err := json.Unmarshal(line, &r); err != nil || r.MediaPK == 0 || r.Status == "" {
			skipped++
			continue
		}
		rows[r.MediaPK] = r
	}
	return rows, skipped, sc.Err()
}

type manifestWriter struct{ f *os.File }

// openManifest opens a manifest for appending. When a crash left the last line
// unfinished, it first ends that line, so the next row starts on its own line.
func openManifest(path string) (*manifestWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err != nil && err != io.EOF {
			f.Close()
			return nil, err
		}
		if last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				f.Close()
				return nil, err
			}
		}
	}
	return &manifestWriter{f: f}, nil
}

func (m *manifestWriter) append(r Row) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := m.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return m.f.Sync()
}

func (m *manifestWriter) close() error { return m.f.Close() }
