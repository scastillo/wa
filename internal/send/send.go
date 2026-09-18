// Package send writes WhatsApp messages through wacli, a linked device.
//
// Sending breaks WhatsApp's terms, and a ban hits the phone number, not the
// app. The guards here map to the ban codes WhatsApp itself returns:
//   - 101, too many messages to people who do not have you in their contacts:
//     wa never sends the first message in a chat.
//   - 104, the same message too many times: one text goes to few chats a day.
//   - 102/103/106: no broadcast lists, no status, no new groups.
//
// A temporary ban or a logout stops every later send until the user acts.
package send

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits are the guards. A zero field takes the default.
type Limits struct {
	PerDay        int           // messages in 24 hours (default 50)
	MinGap        time.Duration // shortest pause between messages (default 3s)
	MaxGap        time.Duration // longest pause (default 8s)
	SameTextChats int           // chats that may get the same text in 24 hours (default 2)
	Chunk         int           // characters per message (default 4000)
}

func (l Limits) withDefaults() Limits {
	if l.PerDay == 0 {
		l.PerDay = 50
	}
	if l.MinGap == 0 {
		l.MinGap = 3 * time.Second
	}
	if l.MaxGap == 0 {
		l.MaxGap = 8 * time.Second
	}
	if l.SameTextChats == 0 {
		l.SameTextChats = 2
	}
	if l.Chunk == 0 {
		l.Chunk = 4000
	}
	return l
}

// Sender runs wacli under the guards.
type Sender struct {
	Wacli     string // path to the wacli binary
	StoreDir  string // wacli store, passed as WACLI_STORE_DIR
	StatePath string // where the send history lives
	Limits    Limits
	Now       func() time.Time
	Sleep     func(time.Duration)
	Jitter    func(min, max time.Duration) time.Duration
	// Run executes wacli. It returns its output and exit code.
	Run func(args, env []string) (stdout, stderr string, code int)
	// HasInbound reports whether the chat ever wrote to the user.
	HasInbound func(chatPK int64) (bool, error)
	LockWait   time.Duration // how long to wait for wacli's store lock (default 30s)
	Out        io.Writer
}

// Request is one send.
type Request struct {
	ChatPK  int64
	JID     string
	Name    string
	Text    string
	File    string
	Caption string
	DryRun  bool
}

// Result says what went out, or what a dry run would send.
type Result struct {
	Parts   []string // the message parts, in order
	Sent    int      // how many actually went out
	Planned bool     // true for a dry run
}

// Errors the caller reports as they are.
var (
	ErrFirstContact = errors.New("this chat never wrote to you, and wa never sends the first message")
	ErrNothing      = errors.New("nothing to send: give a message or a file")
	ErrNotLinked    = errors.New("wacli is not linked; pair the device again")
	ErrBusy         = errors.New("another wacli is running and holds the store; wait for it to finish, then try again")
)

// BanError stops every send until the ban expires.
type BanError struct{ Until time.Time }

func (e *BanError) Error() string {
	return fmt.Sprintf("WhatsApp put a temporary ban on this number until %s; wa will not send before then",
		e.Until.Format(time.RFC3339))
}

// LimitError is a guard refusing a send.
type LimitError struct{ Reason string }

func (e *LimitError) Error() string { return e.Reason }

type entry struct {
	At   time.Time `json:"at"`
	Chat string    `json:"chat"`
	Hash string    `json:"hash"`
}

type state struct {
	Version     int       `json:"version"`
	BannedUntil time.Time `json:"banned_until,omitzero"`
	LoggedOut   bool      `json:"logged_out,omitempty"`
	Sent        []entry   `json:"sent"`
}

const stateVersion = 1

// Send applies the guards, then runs wacli once per message part.
func (s *Sender) Send(r Request) (Result, error) {
	lim := s.Limits.withDefaults()
	now := s.Now()
	if strings.TrimSpace(r.Text) == "" && r.File == "" {
		return Result{}, ErrNothing
	}

	st, err := s.load()
	if err != nil {
		return Result{}, err
	}
	st.prune(now)
	if st.LoggedOut {
		return Result{}, ErrNotLinked
	}
	if now.Before(st.BannedUntil) {
		return Result{}, &BanError{Until: st.BannedUntil}
	}

	inbound, err := s.HasInbound(r.ChatPK)
	if err != nil {
		return Result{}, err
	}
	if !inbound {
		return Result{}, ErrFirstContact
	}

	parts := []string{r.File}
	if r.File == "" {
		parts = chunk(r.Text, lim.Chunk)
	}
	if n := len(st.Sent) + len(parts); n > lim.PerDay {
		return Result{}, &LimitError{Reason: fmt.Sprintf(
			"that would be %d messages in 24 hours, over the limit of %d", n, lim.PerDay)}
	}
	if r.File == "" {
		if chats := st.chatsWithText(r.Text); len(chats) >= lim.SameTextChats && !chats[r.JID] {
			return Result{}, &LimitError{Reason: fmt.Sprintf(
				"the same text already went to %d chats today, which is the limit", len(chats))}
		}
	}

	res := Result{Parts: parts}
	if r.DryRun {
		res.Planned = true
		return res, nil
	}

	for i, part := range parts {
		if i > 0 || !st.lastSentAt().IsZero() {
			s.pause(st.lastSentAt(), now, lim)
			now = s.Now()
		}
		args := s.args(r, part)
		stdout, stderr, code := s.Run(args, []string{"WACLI_STORE_DIR=" + s.StoreDir})
		if trouble := readTrouble(stdout+"\n"+stderr, code, now); trouble != nil {
			// A busy store says nothing about the account, so it changes no state.
			if !trouble.until.IsZero() || trouble.loggedOut {
				st.BannedUntil, st.LoggedOut = trouble.until, trouble.loggedOut
				_ = s.save(st)
			}
			return res, trouble.err
		}
		if code != 0 {
			return res, fmt.Errorf("wacli exited %d: %s", code, firstLine(stderr, stdout))
		}
		st.Sent = append(st.Sent, entry{At: now, Chat: r.JID, Hash: hash(r.Text)})
		if err := s.save(st); err != nil {
			return res, err
		}
		res.Sent++
	}
	return res, nil
}

// args builds one wacli command. --lock-wait lets a send queue behind a sync
// that holds wacli's store lock, instead of failing at once.
func (s *Sender) args(r Request, part string) []string {
	wait := s.LockWait
	if wait == 0 {
		wait = 30 * time.Second
	}
	lock := []string{"--lock-wait", wait.String()}
	if r.File != "" {
		args := []string{"send", "file", "--to", r.JID, "--file", r.File}
		if r.Caption != "" {
			args = append(args, "--caption", r.Caption)
		}
		return append(args, lock...)
	}
	return append([]string{"send", "text", "--to", r.JID, "--message", part}, lock...)
}

// pause keeps a human pace between messages.
func (s *Sender) pause(last, now time.Time, lim Limits) {
	gap := lim.MinGap
	if s.Jitter != nil {
		gap = s.Jitter(lim.MinGap, lim.MaxGap)
	}
	if !last.IsZero() {
		if waited := now.Sub(last); waited >= gap {
			return
		} else {
			gap -= waited
		}
	}
	if gap > 0 {
		s.Sleep(gap)
	}
}

type trouble struct {
	until     time.Time
	loggedOut bool
	err       error
}

// readTrouble looks for a ban or a logout in wacli's output. Both stop wa until
// the user acts: a ban has an expiry, a logout needs a new pairing.
func readTrouble(out string, code int, now time.Time) *trouble {
	low := strings.ToLower(out)
	switch {
	case strings.Contains(low, "temporary ban") || strings.Contains(low, "temporarily banned"):
		until := now.Add(24 * time.Hour)
		if d, ok := banExpiry(low); ok {
			until = now.Add(d)
		}
		return &trouble{until: until, err: &BanError{Until: until}}
	case strings.Contains(low, "logged out") || strings.Contains(low, "not logged in") ||
		strings.Contains(low, "not authenticated") || strings.Contains(low, "device_removed"):
		return &trouble{loggedOut: true, err: ErrNotLinked}
	case strings.Contains(low, "store is locked") || strings.Contains(low, "store locked"):
		// Nothing is wrong with the account, so no state changes.
		return &trouble{err: ErrBusy}
	}
	return nil
}

// banExpiry reads a duration such as "expires in 24h0m0s" from wacli's message.
func banExpiry(low string) (time.Duration, bool) {
	for _, field := range strings.Fields(low) {
		field = strings.Trim(field, ".,;)")
		if d, err := time.ParseDuration(field); err == nil && d > 0 {
			return d, true
		}
	}
	return 0, false
}

// chunk splits text into messages of at most n characters, on line ends where it can.
func chunk(text string, n int) []string {
	text = strings.TrimRight(text, "\n")
	if utf8.RuneCountInString(text) <= n {
		return []string{text}
	}
	var out []string
	runes := []rune(text)
	for len(runes) > 0 {
		if len(runes) <= n {
			out = append(out, string(runes))
			break
		}
		cut := n
		if i := strings.LastIndex(string(runes[:n]), "\n"); i > 0 {
			cut = utf8.RuneCountInString(string(runes[:n])[:i])
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == '\n' {
			runes = runes[1:]
		}
	}
	return out
}

func hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func firstLine(parts ...string) string {
	for _, p := range parts {
		if line := strings.TrimSpace(strings.SplitN(p, "\n", 2)[0]); line != "" {
			return line
		}
	}
	return "no output"
}

func (s *state) prune(now time.Time) {
	var keep []entry
	for _, e := range s.Sent {
		if now.Sub(e.At) < 24*time.Hour {
			keep = append(keep, e)
		}
	}
	s.Sent = keep
}

func (s *state) lastSentAt() time.Time {
	var last time.Time
	for _, e := range s.Sent {
		if e.At.After(last) {
			last = e.At
		}
	}
	return last
}

// chatsWithText lists the chats that already got this text in the last 24 hours.
func (s *state) chatsWithText(text string) map[string]bool {
	want := hash(text)
	chats := map[string]bool{}
	for _, e := range s.Sent {
		if e.Hash == want {
			chats[e.Chat] = true
		}
	}
	return chats
}

func (s *Sender) load() (*state, error) {
	data, err := os.ReadFile(s.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return &state{Version: stateVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("send state %s is unreadable: %w", s.StatePath, err)
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("send state %s is not valid JSON: %w (fix it or delete it)", s.StatePath, err)
	}
	return &st, nil
}

// save writes the send history atomically with mode 0600.
func (s *Sender) save(st *state) error {
	st.Version = stateVersion
	dir := filepath.Dir(s.StatePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".send-*.json")
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
	if err := enc.Encode(st); err != nil {
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
	return os.Rename(tmp.Name(), s.StatePath)
}
