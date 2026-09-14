package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/scastillo/wa/internal/mediakey"
	"github.com/scastillo/wa/internal/store"
)

// Remote says which network sources wa may use.
type Remote int

const (
	RemoteNone Remote = iota // files on disk only
	RemoteLive               // also live CDN links
	RemoteAll                // also the phone (not set up before Phase 4)
)

// DefaultMaxFetch is the largest file wa downloads.
const DefaultMaxFetch = 100 << 20

// Options controls one run.
type Options struct {
	Dest      string
	Allowed   bool // the chat is on the allowlist, so file names may carry names
	Remote    Remote
	DryRun    bool
	MaxFetch  int64
	Client    *http.Client
	Now       func() time.Time
	Location  *time.Location
	LocalPath func(rel string) (string, bool)
}

// Result counts what happened. Saved is by source (disk, cdn, adopted) and
// Unavailable is by reason.
type Result struct {
	Dir          string
	Found        int
	Saved        map[string]int
	Already      int
	Unavailable  map[string]int
	BadHMAC      int
	TooLarge     int
	Conflicts    int
	PartsRemoved int
	WouldCopy    int
	WouldFetch   int
}

var errSizeMismatch = errors.New("size mismatch")

// Run saves the attachments of one chat into Dest/<chat folder>.
func Run(ctx context.Context, chat store.Chat, atts []store.Attachment, o Options) (Result, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Location == nil {
		o.Location = time.Local
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if o.MaxFetch == 0 {
		o.MaxFetch = DefaultMaxFetch
	}
	dir := filepath.Join(o.Dest, ChatDir(chat, o.Allowed))
	res := Result{Dir: dir, Found: len(atts), Saved: map[string]int{}, Unavailable: map[string]int{}}
	rows, _, err := LoadManifest(filepath.Join(dir, ManifestName))
	if err != nil {
		return res, err
	}

	if o.DryRun {
		for _, a := range atts {
			plan(&res, a, rows, dir, o)
		}
		return res, nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return res, err
	}
	if res.PartsRemoved, err = removeParts(dir); err != nil {
		return res, err
	}
	mw, err := openManifest(filepath.Join(dir, ManifestName))
	if err != nil {
		return res, err
	}
	defer mw.close()

	r := runner{res: &res, rows: rows, dir: dir, mw: mw, o: o}
	for _, a := range atts {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := r.save(ctx, a); err != nil {
			return res, err
		}
	}
	return res, nil
}

type runner struct {
	res  *Result
	rows map[int64]Row
	dir  string
	mw   *manifestWriter
	o    Options
}

// save handles one attachment. It returns an error only when the destination
// cannot be written; a missing source is a manifest row, not an error.
func (r *runner) save(ctx context.Context, a store.Attachment) error {
	if prior, ok := r.rows[a.Media.PK]; ok && prior.Status == "saved" && prior.File != "" {
		p := filepath.Join(r.dir, prior.File)
		if sum, err := fileSHA(p); err == nil && sum == prior.SHA256 {
			r.res.Already++
			return nil
		}
		if err := r.setAside(p); err != nil {
			return err
		}
	}

	if src, ok := localFile(a, r.o); ok {
		return r.saveFromDisk(a, src)
	}

	reason, live := linkState(a, r.o)
	switch {
	case !live:
		return r.unavailable(a, reason)
	case r.o.Remote == RemoteNone:
		return r.unavailable(a, "remote-disabled")
	case a.Media.Size > r.o.MaxFetch:
		r.res.TooLarge++
		return r.record(a, Row{Status: "too-large"})
	}
	return r.saveFromCDN(ctx, a)
}

func (r *runner) saveFromDisk(a store.Attachment, src string) error {
	want, err := fileSHA(src)
	if err != nil {
		return r.unavailable(a, "unreadable-on-disk")
	}
	name := FileName(a, r.o.Allowed, readHead(src), r.o.Location)
	target := filepath.Join(r.dir, name)
	if adopted, err := r.adopt(a, target, name, want); adopted || err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return r.unavailable(a, "unreadable-on-disk")
	}
	sum, size, err := writeAtomic(target, a.SentAt, info.Size(), func(w io.Writer) error {
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
	if errors.Is(err, errSizeMismatch) {
		return r.unavailable(a, "changed-while-copying")
	}
	if err != nil {
		return err
	}
	r.res.Saved["disk"]++
	return r.record(a, Row{File: name, SHA256: sum, Size: size, Source: "disk", Status: "saved"})
}

func (r *runner) saveFromCDN(ctx context.Context, a store.Attachment) error {
	body, status, err := get(ctx, r.o.Client, a.URL, a.Media.Size+aesBlock+macBytes+1)
	switch {
	case err != nil:
		return r.unavailable(a, "fetch-failed")
	case status == http.StatusForbidden || status == http.StatusNotFound || status == http.StatusGone:
		return r.unavailable(a, "expired-link")
	case status != http.StatusOK:
		return r.unavailable(a, fmt.Sprintf("http-%d", status))
	}
	plain, _, err := mediakey.Open(a.MediaKey, keyType(a.Type), body)
	switch {
	case errors.Is(err, mediakey.ErrBadMAC):
		r.res.BadHMAC++
		return r.record(a, Row{Status: "bad-hmac"})
	case err != nil:
		return r.unavailable(a, "key-unreadable")
	case int64(len(plain)) != a.Media.Size:
		return r.unavailable(a, "size-mismatch")
	}
	sum := sha256.Sum256(plain)
	want := hex.EncodeToString(sum[:])
	name := FileName(a, r.o.Allowed, plain, r.o.Location)
	target := filepath.Join(r.dir, name)
	if adopted, err := r.adopt(a, target, name, want); adopted || err != nil {
		return err
	}
	got, size, err := writeAtomic(target, a.SentAt, int64(len(plain)), func(w io.Writer) error {
		_, err := w.Write(plain)
		return err
	})
	if err != nil {
		return err
	}
	r.res.Saved["cdn"]++
	return r.record(a, Row{File: name, SHA256: got, Size: size, Source: "cdn", Status: "saved"})
}

// adopt keeps a final file that a crash left without a manifest row, when its
// content is the attachment. A different file under that name is set aside.
func (r *runner) adopt(a store.Attachment, target, name, want string) (bool, error) {
	sum, err := fileSHA(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err == nil && sum == want {
		info, _ := os.Stat(target)
		r.res.Saved["adopted"]++
		return true, r.record(a, Row{File: name, SHA256: sum, Size: info.Size(), Source: "adopted", Status: "saved"})
	}
	return false, r.setAside(target)
}

// setAside renames a file that is not the attachment to <name>.conflict[N].
func (r *runner) setAside(p string) error {
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	for i := 1; ; i++ {
		dst := p + ".conflict"
		if i > 1 {
			dst = fmt.Sprintf("%s.conflict%d", p, i)
		}
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(p, dst); err != nil {
				return err
			}
			r.res.Conflicts++
			return nil
		}
	}
}

func (r *runner) unavailable(a store.Attachment, reason string) error {
	r.res.Unavailable[reason]++
	return r.record(a, Row{Status: "unavailable", Reason: reason})
}

// record appends a manifest row unless the last row already says the same.
func (r *runner) record(a store.Attachment, row Row) error {
	row.MediaPK, row.MessagePK, row.StanzaID = a.Media.PK, a.PK, a.StanzaID
	row.At = r.o.Now().UTC().Format(time.RFC3339)
	if prior, ok := r.rows[a.Media.PK]; ok && prior.Status == row.Status && prior.Reason == row.Reason &&
		prior.File == row.File && prior.SHA256 == row.SHA256 {
		return nil
	}
	r.rows[a.Media.PK] = row
	return r.mw.append(row)
}

// plan fills a dry-run result without touching the network or the destination.
func plan(res *Result, a store.Attachment, rows map[int64]Row, dir string, o Options) {
	if prior, ok := rows[a.Media.PK]; ok && prior.Status == "saved" && prior.File != "" {
		if sum, err := fileSHA(filepath.Join(dir, prior.File)); err == nil && sum == prior.SHA256 {
			res.Already++
			return
		}
	}
	if _, ok := localFile(a, o); ok {
		res.WouldCopy++
		return
	}
	reason, live := linkState(a, o)
	switch {
	case !live:
		res.Unavailable[reason]++
	case o.Remote == RemoteNone:
		res.Unavailable["remote-disabled"]++
	case a.Media.Size > o.MaxFetch:
		res.TooLarge++
	default:
		res.WouldFetch++
	}
}

// linkState reports whether the CDN link is live, or why not.
func linkState(a store.Attachment, o Options) (reason string, live bool) {
	exp, ok := store.LinkExpiry(a.URL)
	switch {
	case ok && exp.After(o.Now().Add(time.Minute)):
		return "", true
	case o.Remote == RemoteAll:
		return "phone-fetch-not-set-up", false
	case !ok:
		return "no-link", false
	}
	return "expired-link", false
}

func localFile(a store.Attachment, o Options) (string, bool) {
	if o.LocalPath == nil {
		return "", false
	}
	full, ok := o.LocalPath(a.LocalPath)
	if !ok {
		return "", false
	}
	info, err := os.Stat(full)
	return full, err == nil && info.Mode().IsRegular()
}

func keyType(msgType int) mediakey.Type {
	switch msgType {
	case 2:
		return mediakey.Video
	case 3:
		return mediakey.Audio
	case 8:
		return mediakey.Document
	case 15:
		return mediakey.Sticker
	}
	return mediakey.Image
}

const (
	aesBlock = 16
	macBytes = 10
)

func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Origin", "https://web.whatsapp.com")
	req.Header.Set("Referer", "https://web.whatsapp.com/")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return body, resp.StatusCode, err
}

// writeAtomic writes to <target>.part, syncs, checks the size, and renames. It never
// replaces an existing file.
func writeAtomic(target string, mtime time.Time, wantSize int64, write func(io.Writer) error) (string, int64, error) {
	part := target + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n := &counter{}
	err = errors.Join(write(io.MultiWriter(f, h, n)), f.Sync(), f.Close())
	if err == nil && n.n != wantSize {
		err = errSizeMismatch
	}
	if err == nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			err = fmt.Errorf("refusing to overwrite %s", target)
		}
	}
	if err == nil {
		err = os.Rename(part, target)
	}
	if err != nil {
		_ = os.Remove(part)
		return "", 0, err
	}
	_ = os.Chtimes(target, mtime, mtime)
	return hex.EncodeToString(h.Sum(nil)), n.n, nil
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func removeParts(dir string) (int, error) {
	parts, err := filepath.Glob(filepath.Join(dir, "*.part"))
	if err != nil {
		return 0, err
	}
	for _, p := range parts {
		if err := os.Remove(p); err != nil {
			return 0, err
		}
	}
	return len(parts), nil
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readHead(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	return bytes.Clone(head[:n])
}
