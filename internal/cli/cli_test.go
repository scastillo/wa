package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
	"github.com/scastillo/wa/internal/policy"
	"github.com/scastillo/wa/internal/store"
)

var now = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

type harness struct {
	fx     *fixture.Set
	policy string
	tty    bool
	stdin  string
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
	h := &harness{fx: fx, policy: filepath.Join(t.TempDir(), "policy.json")}
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
		Now:          func() time.Time { return now },
		Location:     time.UTC,
		AppInstalled: func() bool { return true },
		AppRunning:   func() bool { return true },
		WacliPath:    filepath.Join(h.fx.Dir, "no-wacli"),
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

func TestAllowRefusesWithoutATerminal(t *testing.T) {
	h := newHarness(t)
	h.stdin = "y\n"
	code, out, errOut := h.run(t, "allow", "--match", "Ana")
	if code != 1 || !strings.Contains(errOut, "terminal") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	mustNotLeak(t, "allow without tty", out, errOut)
	p, err := policy.Load(h.policy)
	if err != nil || p.Allowed("5731@s.whatsapp.net") {
		t.Fatal("the allowlist must not change")
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
