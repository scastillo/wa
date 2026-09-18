package send

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

type call struct {
	args []string
	env  []string
}

type fake struct {
	t      *testing.T
	sender *Sender
	calls  []call
	sleeps []time.Duration
	clock  time.Time
	out    string
	errOut string
	code   int
	silent bool // the chat never wrote to us
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{t: t, clock: t0}
	f.sender = &Sender{
		Wacli:     "/bin/wacli",
		StoreDir:  "/store",
		StatePath: filepath.Join(t.TempDir(), "send-state.json"),
		Now:       func() time.Time { return f.clock },
		Sleep: func(d time.Duration) {
			f.sleeps = append(f.sleeps, d)
			f.clock = f.clock.Add(d)
		},
		Jitter:     func(min, max time.Duration) time.Duration { return min },
		HasInbound: func(int64) (bool, error) { return !f.silent, nil },
		Run: func(args, env []string) (string, string, int) {
			f.calls = append(f.calls, call{args: args, env: env})
			f.clock = f.clock.Add(time.Second)
			return f.out, f.errOut, f.code
		},
	}
	return f
}

func (f *fake) state() *state {
	f.t.Helper()
	data, err := os.ReadFile(f.sender.StatePath)
	if err != nil {
		return &state{}
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		f.t.Fatal(err)
	}
	return &st
}

func req(text string) Request {
	return Request{ChatPK: 1, JID: "111@g.us", Name: "Book club", Text: text}
}

func TestSendRunsWacliWithTheChatAndTheText(t *testing.T) {
	f := newFake(t)
	res, err := f.sender.Send(req("hola"))
	if err != nil || res.Sent != 1 {
		t.Fatalf("send: %+v %v", res, err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls: %+v", f.calls)
	}
	want := "send text --to 111@g.us --message hola --lock-wait 30s"
	if got := strings.Join(f.calls[0].args, " "); got != want {
		t.Fatalf("args:\n got  %s\n want %s", got, want)
	}
	if len(f.calls[0].env) != 1 || !strings.HasPrefix(f.calls[0].env[0], "WACLI_STORE_DIR=") {
		t.Fatalf("the wacli store must be passed: %v", f.calls[0].env)
	}
	if st := f.state(); len(st.Sent) != 1 || st.Sent[0].Chat != "111@g.us" {
		t.Fatalf("history: %+v", st)
	}
	if fi, err := os.Stat(f.sender.StatePath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v %v", fi.Mode(), err)
	}
}

func TestSendNeverWritesFirst(t *testing.T) {
	f := newFake(t)
	f.silent = true
	if _, err := f.sender.Send(req("hola")); !errors.Is(err, ErrFirstContact) {
		t.Fatalf("want a first-contact refusal, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing may run: %+v", f.calls)
	}
}

func TestSendSplitsLongTextAndPausesBetweenParts(t *testing.T) {
	f := newFake(t)
	f.sender.Limits = Limits{Chunk: 10, MinGap: 4 * time.Second, MaxGap: 4 * time.Second}
	res, err := f.sender.Send(req("line one\nline two\nline three"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"line one", "line two", "line three"}
	if len(res.Parts) != 3 || res.Parts[0] != want[0] || res.Parts[2] != want[2] {
		t.Fatalf("parts: %q", res.Parts)
	}
	if res.Sent != 3 || len(f.calls) != 3 {
		t.Fatalf("sent %d, calls %d", res.Sent, len(f.calls))
	}
	if len(f.sleeps) != 2 {
		t.Fatalf("one pause between parts: %v", f.sleeps)
	}
	for _, d := range f.sleeps {
		if d <= 0 || d > 4*time.Second {
			t.Fatalf("pause out of range: %v", f.sleeps)
		}
	}
}

func TestSendStopsAtTheDailyLimit(t *testing.T) {
	f := newFake(t)
	f.sender.Limits = Limits{PerDay: 2}
	for i := range 2 {
		if _, err := f.sender.Send(req("hola")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	var limit *LimitError
	_, err := f.sender.Send(req("hola"))
	if !errors.As(err, &limit) || !strings.Contains(err.Error(), "24 hours") {
		t.Fatalf("want a daily-limit refusal, got %v", err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("the third send must not run: %d calls", len(f.calls))
	}

	// A day later the count starts again.
	f.clock = t0.Add(25 * time.Hour)
	if _, err := f.sender.Send(req("hola")); err != nil {
		t.Fatalf("after 25 hours: %v", err)
	}
}

func TestSendRefusesTheSameTextToAThirdChat(t *testing.T) {
	f := newFake(t)
	f.sender.Limits = Limits{SameTextChats: 2}
	for i, jid := range []string{"111@g.us", "222@g.us"} {
		r := req("same words")
		r.JID, r.ChatPK = jid, int64(i+1)
		if _, err := f.sender.Send(r); err != nil {
			t.Fatalf("%s: %v", jid, err)
		}
	}
	r := req("same words")
	r.JID, r.ChatPK = "333@g.us", 3
	var limit *LimitError
	if _, err := f.sender.Send(r); !errors.As(err, &limit) {
		t.Fatalf("want a repeat refusal, got %v", err)
	}
	// The same chat may still get it again, and other text is free.
	r.JID, r.ChatPK = "111@g.us", 1
	if _, err := f.sender.Send(r); err != nil {
		t.Fatalf("a chat that already got it: %v", err)
	}
	other := req("other words")
	other.JID, other.ChatPK = "333@g.us", 3
	if _, err := f.sender.Send(other); err != nil {
		t.Fatalf("different text: %v", err)
	}
}

func TestSendStopsOnABanUntilItExpires(t *testing.T) {
	f := newFake(t)
	f.code = 1
	f.errOut = "wacli: temporary ban (code 101), expires in 48h0m0s"
	var ban *BanError
	_, err := f.sender.Send(req("hola"))
	if !errors.As(err, &ban) || !ban.Until.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("want a ban until %v, got %v", t0.Add(48*time.Hour), err)
	}
	if st := f.state(); !st.BannedUntil.Equal(t0.Add(48*time.Hour)) || len(st.Sent) != 0 {
		t.Fatalf("the ban must be written down and nothing counted: %+v", st)
	}

	// Later sends refuse without running wacli.
	f.code, f.errOut = 0, ""
	before := len(f.calls)
	if _, err := f.sender.Send(req("hola")); !errors.As(err, &ban) {
		t.Fatalf("want the ban to hold, got %v", err)
	}
	if len(f.calls) != before {
		t.Fatal("a banned sender must not run wacli")
	}

	// After it expires, sending works again.
	f.clock = t0.Add(49 * time.Hour)
	if _, err := f.sender.Send(req("hola")); err != nil {
		t.Fatalf("after the ban: %v", err)
	}
}

func TestSendStopsWhenTheDeviceIsLoggedOut(t *testing.T) {
	f := newFake(t)
	f.code = 1
	f.errOut = "error: not logged in (device_removed)"
	if _, err := f.sender.Send(req("hola")); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("want a not-linked error, got %v", err)
	}
	if st := f.state(); !st.LoggedOut {
		t.Fatalf("the logout must be written down: %+v", st)
	}
	f.code, f.errOut = 0, ""
	before := len(f.calls)
	if _, err := f.sender.Send(req("hola")); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("want the logout to hold, got %v", err)
	}
	if len(f.calls) != before {
		t.Fatal("a logged-out sender must not run wacli")
	}
}

func TestDryRunPlansWithoutSending(t *testing.T) {
	f := newFake(t)
	r := req("hola")
	r.DryRun = true
	res, err := f.sender.Send(r)
	if err != nil || !res.Planned || res.Sent != 0 || len(res.Parts) != 1 {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("a dry run must not run wacli: %+v", f.calls)
	}
	if _, err := os.Stat(f.sender.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a dry run must not write the state file")
	}
}

func TestSendFileBuildsTheFileCommand(t *testing.T) {
	f := newFake(t)
	r := req("")
	r.File, r.Caption = "/tmp/photo.jpg", "look"
	res, err := f.sender.Send(r)
	if err != nil || res.Sent != 1 {
		t.Fatalf("file: %+v %v", res, err)
	}
	want := "send file --to 111@g.us --file /tmp/photo.jpg --caption look --lock-wait 30s"
	if got := strings.Join(f.calls[0].args, " "); got != want {
		t.Fatalf("args:\n got  %s\n want %s", got, want)
	}
}

func TestSendNeedsSomethingToSend(t *testing.T) {
	f := newFake(t)
	if _, err := f.sender.Send(req("   ")); !errors.Is(err, ErrNothing) {
		t.Fatalf("want a nothing-to-send error, got %v", err)
	}
}

func TestSendWaitsForTheStoreLockAndReportsABusyStore(t *testing.T) {
	f := newFake(t)
	if _, err := f.sender.Send(req("hola")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.calls[0].args, " "); !strings.Contains(got, "--lock-wait 30s") {
		t.Fatalf("a send must queue behind a sync: %s", got)
	}

	// A locked store is not an account problem: it must not touch the ban state.
	banned := f.sender
	f.code = 1
	f.errOut = "store is locked (another wacli is running?): resource temporarily unavailable"
	if _, err := banned.Send(req("hola")); !errors.Is(err, ErrBusy) {
		t.Fatalf("want a busy error, got %v", err)
	}
	st := f.state()
	if !st.BannedUntil.IsZero() || st.LoggedOut {
		t.Fatalf("a busy store must not be written down as a ban: %+v", st)
	}
	if len(st.Sent) != 1 {
		t.Fatalf("a failed send must not count: %+v", st.Sent)
	}
}

func TestStateFileLeavesOutAnEmptyBan(t *testing.T) {
	f := newFake(t)
	if _, err := f.sender.Send(req("hola")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.sender.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "banned_until") {
		t.Fatalf("a clean state must not carry a ban field:\n%s", data)
	}
}
