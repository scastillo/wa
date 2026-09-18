package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
	"github.com/scastillo/wa/internal/link"
	"github.com/scastillo/wa/internal/policy"
	"github.com/scastillo/wa/internal/store"
)

var now = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

type harness struct {
	fx          *fixture.Set
	policy      string
	tty         bool
	stdin       string
	downloads   string
	appDown     bool
	allowCmd    string // what doctor and the hints tell the user to run
	wacli       string // where wa looks for wacli
	sendState   string
	execCalls   [][]string // what wa ran through Exec, for wacli
	execOut     string
	execErr     string
	execCode    int
	fetch       func(string) ([]byte, error)
	execStream  func(bin string, args, extraEnv []string) int
	updateCache string
	version     string

	clock   time.Time       // advanced only by Sleep
	sleeps  int             // how many times a command slept
	onSleep func(sleep int) // runs after each sleep, e.g. to write a new row
}

// newHarness builds three chats. Only "Family" is on the allowlist.
func newHarness(t *testing.T) *harness {
	t.Helper()
	fx := fixture.New(t)
	at := func(d time.Duration) float64 { return store.ToCoreData(now.Add(d)) }
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWACHATSESSION (Z_PK, ZCONTACTJID, ZPARTNERNAME, ZSESSIONTYPE, ZLASTMESSAGEDATE) VALUES
		(1, '111@g.us', 'Family', 1, ?),
		(2, '222@g.us', 'Family Doctor', 1, ?),
		(3, '5731@s.whatsapp.net', 'Ana', 0, ?)`, at(-time.Minute), at(-2*time.Minute), at(-3*time.Minute))
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAGROUPMEMBER (Z_PK, ZMEMBERJID, ZCONTACTNAME, ZCHATSESSION) VALUES (1, '5799@s.whatsapp.net', '', 1)`)
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAPROFILEPUSHNAME (Z_PK, ZJID, ZPUSHNAME) VALUES (1, '5799@s.whatsapp.net', 'Tio')`)
	fixture.Exec(t, fx.ChatStorage, `INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZMESSAGEDATE, ZISFROMME, ZFROMJID, ZGROUPMEMBER, ZPUSHNAME, ZTEXT, ZMESSAGETYPE) VALUES
		(10, 1, ?, 0, '111@g.us', 1, 'CMPPodUGIAA=', 'family hello', 0),
		(11, 2, ?, 0, '222@g.us', NULL, 'Dr', 'SECRET-DOCTOR hello', 0),
		(12, 3, ?, 0, '5731@s.whatsapp.net', NULL, 'Ana', 'SECRET-ANA hello', 0)`, at(-time.Minute), at(-2*time.Minute), at(-3*time.Minute))
	h := &harness{fx: fx, policy: filepath.Join(t.TempDir(), "policy.json"), clock: now,
		wacli: filepath.Join(fx.Dir, "no-wacli"), sendState: filepath.Join(t.TempDir(), "send-state.json")}
	p, _ := policy.Load(h.policy)
	p.Add("111@g.us", "2026-09-14")
	if err := p.Save(h.policy); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(args, Env{
		Dir:          h.fx.Dir,
		PolicyPath:   h.policy,
		Stdout:       &out,
		Stderr:       &errOut,
		Stdin:        strings.NewReader(h.stdin),
		IsTTY:        func() bool { return h.tty },
		Now:          func() time.Time { return h.clock },
		Location:     time.UTC,
		AppInstalled: func() bool { return true },
		AppRunning:   func() bool { return !h.appDown },
		Sleep: func(d time.Duration) {
			h.sleeps++
			h.clock = h.clock.Add(d)
			if h.onSleep != nil {
				h.onSleep(h.sleeps)
			}
		},
		WacliPath:   h.wacli,
		WacliStore:  h.wacli + "-store",
		SendState:   h.sendState,
		AllowCmd:    h.allowCmd,
		Downloads:   h.downloads,
		Jitter:      func(min, max time.Duration) time.Duration { return min },
		Version:     h.version,
		UpdateCache: h.updateCache,
		Fetch:       h.fetch,
		ExecStream:  h.execStream,
		Exec: func(bin string, args, extraEnv []string) (string, string, int) {
			h.execCalls = append(h.execCalls, append([]string{bin}, args...))
			return h.execOut, h.execErr, h.execCode
		},
	})
	return code, out.String(), errOut.String()
}

func mustNotLeak(t *testing.T, where string, texts ...string) {
	t.Helper()
	for _, s := range []string{"SECRET", "Ana", "Doctor", "5731", "222@g.us"} {
		for _, text := range texts {
			if strings.Contains(text, s) {
				t.Errorf("%s leaked %q:\n%s", where, s, text)
			}
		}
	}
}

func TestReadAllowedChat(t *testing.T) {
	h := newHarness(t)
	code, out, errOut := h.run(t, "read", "111@g.us")
	if code != 0 || !strings.Contains(out, "[2026-09-14 11:59 | Tio] family hello") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
}

func TestReadChatNotOnTheAllowlist(t *testing.T) {
	h := newHarness(t)
	for _, q := range []string{"Ana", "5731@s.whatsapp.net", "3"} {
		code, out, errOut := h.run(t, "read", q)
		if code != 3 {
			t.Errorf("%q: exit %d, want 3", q, code)
		}
		mustNotLeak(t, "read "+q, out, errOut)
	}
}

func TestReadAmbiguousListsOnlyAllowedCandidates(t *testing.T) {
	h := newHarness(t)
	code, out, errOut := h.run(t, "read", "family")
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	all := out + errOut
	if !strings.Contains(all, "111@g.us") || !strings.Contains(all, "1 more not on the allowlist") {
		t.Fatalf("candidates:\n%s", all)
	}
	mustNotLeak(t, "ambiguous read", out, errOut)
}

func TestChatsShowOnlyAllowedNames(t *testing.T) {
	h := newHarness(t)
	code, out, errOut := h.run(t, "chats")
	if code != 0 || !strings.Contains(out, "Family") || !strings.Contains(out, "2 chats not on the allowlist") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "chats", out, errOut)

	code, out, errOut = h.run(t, "chats", "--json")
	if code != 0 || !strings.Contains(out, `"jid":"111@g.us"`) {
		t.Fatalf("json exit %d\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "chats --json", out, errOut)
}

func TestSearchNeverReturnsOtherChats(t *testing.T) {
	h := newHarness(t)
	code, out, errOut := h.run(t, "search", "hello")
	if code != 0 || !strings.Contains(out, "family hello") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "search", out, errOut)

	code, out, errOut = h.run(t, "search", "SECRET", "--json")
	if code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("exit %d, want no results\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "search --json", out, errOut)

	code, out, errOut = h.run(t, "search", "hello", "--chat", "Ana")
	if code != 3 {
		t.Fatalf("search --chat on a chat not on the list: exit %d, want 3", code)
	}
	mustNotLeak(t, "search --chat", out, errOut)
}

func TestReadJSONCarriesFields(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run(t, "read", "Family", "--json")
	// "Family" matches two chats, but only one is allowed and the other must not be named.
	if code != 2 {
		t.Fatalf("exit %d, want 2 (ambiguous)", code)
	}
	code, out, _ = h.run(t, "read", "1", "--json")
	if code != 0 || !strings.Contains(out, `"sender":"Tio"`) || !strings.Contains(out, `"text":"family hello"`) ||
		!strings.Contains(out, `"sent_at":"2026-09-14T11:59:00Z"`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestMissingPolicyAllowsNothing(t *testing.T) {
	h := newHarness(t)
	h.policy = filepath.Join(t.TempDir(), "absent.json")
	code, out, errOut := h.run(t, "read", "111@g.us")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if !strings.Contains(errOut, "wa allow") {
		t.Errorf("stderr must say how to allow a chat: %s", errOut)
	}
	mustNotLeak(t, "missing policy", out, errOut)
}

func TestMalformedPolicyFailsClosed(t *testing.T) {
	h := newHarness(t)
	if err := os.WriteFile(h.policy, []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"read", "1"}, {"chats"}, {"search", "hello"}} {
		code, out, errOut := h.run(t, args...)
		if code != 1 || !strings.Contains(errOut, h.policy) || strings.Contains(out, "family hello") {
			t.Errorf("%v: exit %d\nstdout %s\nstderr %s", args, code, out, errOut)
		}
	}
}

func TestAllowWorksWithoutATerminal(t *testing.T) {
	h := newHarness(t)
	code, out, errOut := h.run(t, "allow", "--match", "Ana")
	if code != 0 {
		t.Fatalf("an agent must be able to allow a chat: exit %d\n%s%s", code, out, errOut)
	}
	p, err := policy.Load(h.policy)
	if err != nil || !p.Allowed("5731@s.whatsapp.net") {
		t.Fatalf("allowlist: %+v %v", p, err)
	}

	// Two matches and nobody to ask: name them and change nothing.
	code, out, errOut = h.run(t, "allow", "--match", "family")
	if code != exitAmbiguous || !strings.Contains(errOut, "222@g.us") || !strings.Contains(errOut, "111@g.us") {
		t.Fatalf("ambiguous: exit %d\n%s%s", code, out, errOut)
	}
	p, _ = policy.Load(h.policy)
	if p.Allowed("222@g.us") {
		t.Fatal("an ambiguous match must change nothing")
	}
}

func TestAllowAllOpensEveryChatAndDisallowAllCloses(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run(t, "read", "222@g.us"); code != exitNotAllowed {
		t.Fatal("the second chat must start blocked")
	}

	code, out, errOut := h.run(t, "allow", "--all")
	if code != 0 || !strings.Contains(out, "every chat") {
		t.Fatalf("allow --all: exit %d\n%s%s", code, out, errOut)
	}
	if code, out, _ = h.run(t, "read", "222@g.us"); code != 0 || !strings.Contains(out, "SECRET-DOCTOR hello") {
		t.Fatalf("read after allow --all: exit %d\n%s", code, out)
	}
	if _, out, _ = h.run(t, "doctor"); !strings.Contains(out, "every chat is readable") {
		t.Fatalf("doctor must say the allowlist is off:\n%s", out)
	}
	if _, out, errOut = h.run(t, "chats"); strings.Contains(out+errOut, "allowlist") {
		t.Fatalf("chats must hide nothing and must not ask for an allowlist:\n%s%s", out, errOut)
	}
	if _, out, errOut = h.run(t, "search", "hello"); strings.Contains(errOut, "allowlist") ||
		!strings.Contains(out, "SECRET-ANA hello") {
		t.Fatalf("search must cover every chat:\n%s%s", out, errOut)
	}

	if code, out, errOut = h.run(t, "disallow", "--all"); code != 0 || !strings.Contains(out, "1 chat") {
		t.Fatalf("disallow --all: exit %d\n%s%s", code, out, errOut)
	}
	if code, _, _ = h.run(t, "read", "222@g.us"); code != exitNotAllowed {
		t.Fatal("disallow --all must close the other chats again")
	}
	if code, _, _ = h.run(t, "read", "111@g.us"); code != 0 {
		t.Fatal("the chat that was on the list must stay readable")
	}
}

func TestAllowInATerminal(t *testing.T) {
	h := newHarness(t)
	h.tty = true

	h.stdin = "n\n"
	if code, _, _ := h.run(t, "allow", "--match", "Ana"); code != 1 {
		t.Fatalf("declined: exit %d, want 1", code)
	}

	h.stdin = "y\n"
	code, out, errOut := h.run(t, "allow", "--match", "Ana")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	h.stdin = "2\ny\n" // "family" matches Family and Family Doctor; pick the second
	if code, out, errOut := h.run(t, "allow", "--match", "family"); code != 0 {
		t.Fatalf("pick: exit %d\n%s%s", code, out, errOut)
	}
	p, err := policy.Load(h.policy)
	if err != nil || !p.Allowed("5731@s.whatsapp.net") || !p.Allowed("222@g.us") {
		t.Fatalf("allowlist: %+v %v", p, err)
	}

	if code, _, _ := h.run(t, "disallow", "5731@s.whatsapp.net"); code != 0 {
		t.Fatalf("disallow: exit %d", code)
	}
	p, _ = policy.Load(h.policy)
	if p.Allowed("5731@s.whatsapp.net") {
		t.Fatal("disallow must remove the chat")
	}
}

func TestDoctor(t *testing.T) {
	h := newHarness(t)
	// A real session can carry a far-future date; freshness must come from messages.
	fixture.Exec(t, h.fx.ChatStorage, "UPDATE ZWACHATSESSION SET ZLASTMESSAGEDATE = ? WHERE Z_PK = 3",
		store.ToCoreData(time.Date(9026, 1, 1, 0, 0, 0, 0, time.UTC)))
	code, out, errOut := h.run(t, "doctor")
	if code != 0 || !strings.Contains(out, "1 chat allowed") || !strings.Contains(out, "newest message 1m0s ago") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	fixture.Exec(t, h.fx.ChatStorage, "ALTER TABLE ZWAMESSAGE RENAME COLUMN ZTEXT TO ZTEXT2")
	code, out, errOut = h.run(t, "doctor")
	if code != 1 || !strings.Contains(out+errOut, "schema drift") || !strings.Contains(out+errOut, "ZWAMESSAGE.ZTEXT") {
		t.Fatalf("drift: exit %d\n%s%s", code, out, errOut)
	}
}

func TestHintsNameTheCommandTheUserCanRunAndDoctorSkipsWacli(t *testing.T) {
	h := newHarness(t)
	h.allowCmd = "/opt/wa/bin/wa"
	h.policy = filepath.Join(t.TempDir(), "none.json") // no chat is allowed
	hint := "/opt/wa/bin/wa allow --match <name>"

	code, out, errOut := h.run(t, "doctor")
	if code != 0 || !strings.Contains(out, hint) {
		t.Fatalf("doctor must name the command: exit %d\n%s%s", code, out, errOut)
	}
	if strings.Contains(out+errOut, "wacli") {
		t.Fatalf("a missing wacli is not worth a line; reading never needs it:\n%s", out)
	}
	if code, _, errOut = h.run(t, "read", "111@g.us"); code != exitNotAllowed || !strings.Contains(errOut, hint) {
		t.Fatalf("read: exit %d, stderr %q", code, errOut)
	}

	// Without the link, the hint falls back to the plain command.
	plain := newHarness(t)
	plain.policy = filepath.Join(t.TempDir(), "none.json")
	if _, out, _ = plain.run(t, "doctor"); !strings.Contains(out, "run  wa allow --match <name>") {
		t.Fatalf("default hint:\n%s", out)
	}

	// An installed wacli is still reported.
	withWacli := newHarness(t)
	withWacli.wacli = filepath.Join(t.TempDir(), "wacli")
	if err := os.WriteFile(withWacli.wacli, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, out, _ = withWacli.run(t, "doctor"); !strings.Contains(out, "wacli") {
		t.Fatalf("an installed wacli must be reported:\n%s", out)
	}
}

func TestSendShowsTheMessageAndNeedsYes(t *testing.T) {
	h := newHarness(t)

	// Without --yes wa shows the message and sends nothing.
	code, out, errOut := h.run(t, "send", "111@g.us", "hola familia")
	if code != exitError || !strings.Contains(out, "hola familia") || !strings.Contains(errOut, "--yes") {
		t.Fatalf("preview: exit %d\n%s%s", code, out, errOut)
	}
	if len(h.execCalls) != 0 {
		t.Fatalf("nothing may run without --yes: %v", h.execCalls)
	}

	// --dry-run is the same preview, but it is not an error.
	if code, out, _ = h.run(t, "send", "111@g.us", "hola", "--dry-run"); code != exitOK || !strings.Contains(out, "hola") {
		t.Fatalf("dry run: exit %d\n%s", code, out)
	}
	if len(h.execCalls) != 0 {
		t.Fatalf("a dry run must not run wacli: %v", h.execCalls)
	}

	// With --yes it calls wacli once, with the chat and the text.
	code, out, errOut = h.run(t, "send", "111@g.us", "hola familia", "--yes")
	if code != exitOK || !strings.Contains(out, "sent 1 message") {
		t.Fatalf("send: exit %d\n%s%s", code, out, errOut)
	}
	if len(h.execCalls) != 1 {
		t.Fatalf("calls: %v", h.execCalls)
	}
	got := strings.Join(h.execCalls[0], " ")
	for _, want := range []string{h.wacli, "send text", "--to 111@g.us", "--message hola familia"} {
		if !strings.Contains(got, want) {
			t.Fatalf("wacli call misses %q: %s", want, got)
		}
	}
}

func TestSendObeysTheAllowlistAndNeverWritesFirst(t *testing.T) {
	h := newHarness(t)
	code, out, errOut := h.run(t, "send", "222@g.us", "hola", "--yes")
	if code != exitNotAllowed {
		t.Fatalf("a chat that is not allowed: exit %d\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "send to a blocked chat", out, errOut)
	if len(h.execCalls) != 0 {
		t.Fatalf("nothing may run: %v", h.execCalls)
	}

	// An allowed chat that never wrote to us is still refused.
	fixture.Exec(t, h.fx.ChatStorage, `INSERT INTO ZWACHATSESSION (Z_PK, ZCONTACTJID, ZPARTNERNAME, ZSESSIONTYPE) VALUES (4, '444@s.whatsapp.net', 'Quiet', 0)`)
	fixture.Exec(t, h.fx.ChatStorage, `INSERT INTO ZWAMESSAGE (Z_PK, ZCHATSESSION, ZMESSAGEDATE, ZISFROMME, ZTEXT, ZMESSAGETYPE) VALUES (40, 4, ?, 1, 'only me', 0)`,
		store.ToCoreData(now))
	p, _ := policy.Load(h.policy)
	p.Add("444@s.whatsapp.net", "2026-09-18")
	if err := p.Save(h.policy); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = h.run(t, "send", "444@s.whatsapp.net", "hola", "--yes")
	if code != exitError || !strings.Contains(errOut, "never sends the first message") {
		t.Fatalf("first contact: exit %d\n%s%s", code, out, errOut)
	}
	if len(h.execCalls) != 0 {
		t.Fatalf("nothing may run: %v", h.execCalls)
	}
}

func TestSendReportsABanAndStops(t *testing.T) {
	h := newHarness(t)
	h.execCode = 1
	h.execErr = "wacli: temporary ban (code 101), expires in 12h0m0s"
	code, _, errOut := h.run(t, "send", "111@g.us", "hola", "--yes")
	if code != exitError || !strings.Contains(errOut, "temporary ban") {
		t.Fatalf("ban: exit %d\n%s", code, errOut)
	}
	h.execCode, h.execErr = 0, ""
	calls := len(h.execCalls)
	if code, _, errOut = h.run(t, "send", "111@g.us", "hola", "--yes"); code != exitError || !strings.Contains(errOut, "temporary ban") {
		t.Fatalf("the ban must hold: exit %d\n%s", code, errOut)
	}
	if len(h.execCalls) != calls {
		t.Fatal("a banned sender must not run wacli again")
	}
}

func TestParseWhen(t *testing.T) {
	loc := time.FixedZone("COT", -5*3600)
	cases := map[string]time.Time{
		"2026-09-13":           time.Date(2026, 9, 13, 0, 0, 0, 0, loc),
		"2026-09-13T08:30:00Z": time.Date(2026, 9, 13, 8, 30, 0, 0, time.UTC),
		"36h":                  now.Add(-36 * time.Hour),
		"7d":                   now.Add(-7 * 24 * time.Hour),
	}
	for in, want := range cases {
		got, err := parseWhen(in, now, loc)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: got %v %v, want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"yesterday", "13/09/2026", "-3d"} {
		if _, err := parseWhen(bad, now, loc); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// linkHarness gives the harness a fake wacli download and a fake pairing run.
type linkFakes struct {
	fetched  []string
	streamed [][]string
	tarball  []byte
	sums     string
	authOut  string
}

func TestLinkInstallsWacliThenPairs(t *testing.T) {
	h := newHarness(t)
	f := &linkFakes{tarball: fakeWacliTar(t), authOut: "Authenticated as 1@s.whatsapp.net"}
	f.sums = sha256Hex(f.tarball) + "  " + link.Asset(link.Version, runtime.GOARCH) + "\n"
	h.fetch = func(url string) ([]byte, error) {
		f.fetched = append(f.fetched, url)
		if strings.HasSuffix(url, "checksums.txt") {
			return []byte(f.sums), nil
		}
		return f.tarball, nil
	}
	h.execStream = func(bin string, args, extraEnv []string) int {
		f.streamed = append(f.streamed, append([]string{bin}, args...))
		return 0
	}
	h.execOut = f.authOut
	h.wacli = filepath.Join(t.TempDir(), "bin", "wacli")

	code, out, errOut := h.run(t, "link")
	if code != exitOK || !strings.Contains(out, "installed wacli") || !strings.Contains(out, "sending is ready") {
		t.Fatalf("link: exit %d\n%s%s", code, out, errOut)
	}
	if len(f.streamed) != 1 || !strings.Contains(strings.Join(f.streamed[0], " "), "auth --qr-format terminal") {
		t.Fatalf("pairing must stream the QR: %v", f.streamed)
	}
	if !strings.Contains(out, "Linked devices") {
		t.Fatalf("the user needs the steps on their phone:\n%s", out)
	}
	if _, err := os.Stat(h.wacli); err != nil {
		t.Fatalf("wacli must be installed: %v", err)
	}

	// A second link does not download again.
	before := len(f.fetched)
	if code, _, _ = h.run(t, "link", "--status"); code != exitOK {
		t.Fatalf("status: exit %d", code)
	}
	if len(f.fetched) != before {
		t.Fatalf("an installed wacli must not download again: %v", f.fetched)
	}
}

func TestLinkStatusSaysWhenNoPhoneIsPaired(t *testing.T) {
	h := newHarness(t)
	h.wacli = filepath.Join(t.TempDir(), "wacli")
	if err := os.WriteFile(h.wacli, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.execOut, h.execCode = "not authenticated", 1
	h.fetch = func(string) ([]byte, error) { return nil, errors.New("must not download") }
	code, out, _ := h.run(t, "link", "--status")
	if code != exitError || !strings.Contains(out, "not paired") {
		t.Fatalf("status: exit %d\n%s", code, out)
	}
}

func TestDoctorReportsSendingAndUpdates(t *testing.T) {
	h := newHarness(t)
	h.updateCache = filepath.Join(t.TempDir(), "update.json")
	h.version = "0.1.0"
	h.fetch = func(string) ([]byte, error) { return []byte(`{"tag_name":"v9.9.9"}`), nil }
	_, out, _ := h.run(t, "doctor")
	if !strings.Contains(out, "ok   sending   not set up") {
		t.Fatalf("doctor must say sending is not set up:\n%s", out)
	}
	if !strings.Contains(out, "wa 9.9.9 is out") || !strings.Contains(out, "claude plugin update wa") {
		t.Fatalf("doctor must offer the update:\n%s", out)
	}

	// On the newest version it says nothing about updates.
	h.version = "9.9.9"
	if _, out, _ = h.run(t, "doctor"); strings.Contains(out, "is out") {
		t.Fatalf("no update line on the newest version:\n%s", out)
	}
}

func fakeWacliTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho wacli\n")
	if err := tw.WriteHeader(&tar.Header{Name: "wacli", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
