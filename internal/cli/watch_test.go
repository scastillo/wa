package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scastillo/wa/internal/fixture"
	"github.com/scastillo/wa/internal/policy"
	"github.com/scastillo/wa/internal/store"
)

func stateFile(t *testing.T) string { return filepath.Join(t.TempDir(), "watch.json") }

func cursorOf(t *testing.T, path string) (int64, string) {
	t.Helper()
	var st struct {
		Cursor    int64  `json:"cursor"`
		Heartbeat string `json:"heartbeat"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("state is not JSON: %s", data)
	}
	return st.Cursor, st.Heartbeat
}

// arrive writes a message row after the given sleep, as WhatsApp Desktop would.
func (h *harness) arrive(t *testing.T, afterSleep int, pk, chatPK int64, sentAgo time.Duration, text string) {
	t.Helper()
	prev := h.onSleep
	h.onSleep = func(n int) {
		if prev != nil {
			prev(n)
		}
		if n != afterSleep {
			return
		}
		var member any // only Family (chat 1) has a group member row in the harness
		if chatPK == 1 {
			member = 1
		}
		fixture.Exec(t, h.fx.ChatStorage, `INSERT INTO ZWAMESSAGE
			(Z_PK, ZCHATSESSION, ZMESSAGEDATE, ZISFROMME, ZGROUPMEMBER, ZTEXT, ZMESSAGETYPE) VALUES (?, ?, ?, 0, ?, ?, 0)`,
			pk, chatPK, store.ToCoreData(now.Add(-sentAgo)), member, text)
	}
}

func TestWatchSeedsThenPrintsTheNextMessageAndExits(t *testing.T) {
	h := newHarness(t)
	st := stateFile(t)
	if code, out, errOut := h.run(t, "watch", "--state", st, "--seed"); code != 0 || !strings.Contains(out, "seeded") {
		t.Fatalf("seed: exit %d\n%s%s", code, out, errOut)
	}
	if c, _ := cursorOf(t, st); c != 12 {
		t.Fatalf("seed cursor: got %d, want 12", c)
	}

	h.arrive(t, 1, 50, 1, 0, "new hello")
	code, out, errOut := h.run(t, "watch", "--state", st, "--wait", "60", "--poll", "10")
	if code != 0 || !strings.Contains(out, "| Family | Tio] new hello") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", code, out, errOut)
	}
	if strings.Contains(out, "family hello") {
		t.Fatal("a message older than the cursor must not print")
	}
	if h.sleeps != 1 {
		t.Fatalf("the watcher must exit on the first new message: slept %d times", h.sleeps)
	}
	if c, _ := cursorOf(t, st); c != 50 {
		t.Fatalf("cursor: got %d, want 50", c)
	}
}

func TestWatchPrintsALateArrivalWithAnOldDate(t *testing.T) {
	h := newHarness(t)
	st := stateFile(t)
	h.run(t, "watch", "--state", st, "--seed")
	h.arrive(t, 1, 51, 1, 48*time.Hour, "late history row")
	if code, out, _ := h.run(t, "watch", "--state", st, "--wait", "60", "--poll", "10"); code != 0 || !strings.Contains(out, "late history row") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// A busy chat that is not on the allowlist must not wake the watcher: it would
// spend a whole wait cycle, and the reader's attention, on a line it cannot use.
func TestWatchCountsChatsNotOnTheAllowlistWithoutWaking(t *testing.T) {
	h := newHarness(t)
	st := stateFile(t)
	h.run(t, "watch", "--state", st, "--seed")
	h.arrive(t, 1, 52, 3, 0, "SECRET-ANA new")
	code, out, errOut := h.run(t, "watch", "--state", st, "--wait", "30", "--poll", "10")
	if code != 0 || h.sleeps != 3 || !strings.Contains(out, "(no new messages)") ||
		!strings.Contains(out, "1 new message in a chat not on the allowlist") {
		t.Fatalf("exit %d sleeps %d\n%s%s", code, h.sleeps, out, errOut)
	}
	mustNotLeak(t, "watch", out, errOut)
	if c, _ := cursorOf(t, st); c != 52 {
		t.Fatalf("the cursor must move past rows it does not print: got %d", c)
	}
}

func TestWatchWakesOnAnAllowedMessageAndReportsTheOtherCount(t *testing.T) {
	h := newHarness(t)
	st := stateFile(t)
	h.run(t, "watch", "--state", st, "--seed")
	h.arrive(t, 1, 56, 3, 0, "SECRET-ANA one")
	h.arrive(t, 1, 57, 2, 0, "SECRET-DOCTOR two")
	h.arrive(t, 2, 58, 1, 0, "family three")
	code, out, errOut := h.run(t, "watch", "--state", st, "--wait", "60", "--poll", "10")
	if code != 0 || h.sleeps != 2 || !strings.Contains(out, "family three") ||
		!strings.Contains(out, "2 new messages in chats not on the allowlist") {
		t.Fatalf("exit %d sleeps %d\n%s%s", code, h.sleeps, out, errOut)
	}
	mustNotLeak(t, "watch", out, errOut)
}

func TestWatchTimesOutWithNoNews(t *testing.T) {
	h := newHarness(t)
	st := stateFile(t)
	h.run(t, "watch", "--state", st, "--seed")
	code, out, _ := h.run(t, "watch", "--state", st, "--wait", "30", "--poll", "10")
	if code != 0 || !strings.Contains(out, "(no new messages)") || h.sleeps != 3 {
		t.Fatalf("exit %d sleeps %d\n%s", code, h.sleeps, out)
	}
	if c, beat := cursorOf(t, st); c != 12 || beat != "2026-09-14T12:00:30Z" {
		t.Fatalf("state after timeout: cursor %d heartbeat %q", c, beat)
	}
}

func TestWatchWarnsOnceWhenTheAppIsNotRunning(t *testing.T) {
	h := newHarness(t)
	st := stateFile(t)
	h.run(t, "watch", "--state", st, "--seed")
	h.appDown = true
	_, out, errOut := h.run(t, "watch", "--state", st, "--wait", "30", "--poll", "10")
	if n := strings.Count(out+errOut, "[wa-warn] WhatsApp Desktop is not running"); n != 1 {
		t.Fatalf("warnings: %d\n%s%s", n, out, errOut)
	}
}

func TestWatchWithoutAUsableStateSeedsInsteadOfDumpingHistory(t *testing.T) {
	for name, body := range map[string]string{"missing": "", "corrupt": "{garbage"} {
		h := newHarness(t)
		st := stateFile(t)
		if body != "" {
			if err := os.WriteFile(st, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		code, out, errOut := h.run(t, "watch", "--state", st, "--wait", "10", "--poll", "10")
		if code != 0 || !strings.Contains(out, "[wa-warn]") || !strings.Contains(out, "(no new messages)") || strings.Contains(out, "family hello") {
			t.Fatalf("%s: exit %d\n%s%s", name, code, out, errOut)
		}
		if c, _ := cursorOf(t, st); c != 12 {
			t.Fatalf("%s: cursor %d, want 12", name, c)
		}
	}
}

func TestWatchFiltersByChat(t *testing.T) {
	h := newHarness(t)
	p, _ := policy.Load(h.policy)
	p.Add("5731@s.whatsapp.net", "2026-09-14")
	if err := p.Save(h.policy); err != nil {
		t.Fatal(err)
	}
	st := stateFile(t)
	h.run(t, "watch", "--state", st, "--seed")
	h.arrive(t, 1, 53, 3, 0, "ana update")
	h.arrive(t, 2, 54, 1, 0, "family update")

	code, out, _ := h.run(t, "watch", "--state", st, "--chat", "111@g.us", "--wait", "60", "--poll", "10")
	if code != 0 || !strings.Contains(out, "family update") || strings.Contains(out, "ana update") || h.sleeps != 2 {
		t.Fatalf("exit %d sleeps %d\n%s", code, h.sleeps, out)
	}
	if c, _ := cursorOf(t, st); c != 54 {
		t.Fatalf("cursor: got %d", c)
	}
}

func TestWatchJSON(t *testing.T) {
	h := newHarness(t)
	st := stateFile(t)
	h.run(t, "watch", "--state", st, "--seed")
	h.arrive(t, 1, 55, 1, 0, "json hello")
	if code, out, _ := h.run(t, "watch", "--state", st, "--wait", "60", "--poll", "10", "--json"); code != 0 || !strings.Contains(out, `"text":"json hello"`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestWatchFailsLoudWhenStateCannotBeWritten(t *testing.T) {
	h := newHarness(t)
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := h.run(t, "watch", "--state", filepath.Join(notADir, "watch.json"), "--seed")
	if code != 1 || !strings.Contains(out+errOut, "cannot write state") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if code, _, _ := h.run(t, "watch"); code != 1 {
		t.Fatalf("watch without --state: exit %d, want 1", code)
	}
}
