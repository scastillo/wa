package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scastillo/wa/internal/policy"
	"github.com/scastillo/wa/internal/render"
	"github.com/scastillo/wa/internal/store"
)

const watchBatch = 500

type watchState struct {
	Version   int    `json:"version"`
	Cursor    int64  `json:"cursor"`
	Heartbeat string `json:"heartbeat"`
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// watchCmd is a one-shot watcher. It prints the first new messages in allowed chats
// and exits, or prints "(no new messages)" when --wait runs out. Messages in chats
// not on the allowlist only add to a count. The cursor is Z_PK.
func watchCmd(args []string, env Env) int {
	fs := newFlags("watch", env)
	statePath := fs.String("state", "", "cursor file (required)")
	seed := fs.Bool("seed", false, "set the cursor to the newest message and exit")
	wait := fs.Int("wait", 0, "seconds to wait for a new message (0 checks once)")
	poll := fs.Int("poll", 15, "seconds between checks")
	full := fs.Bool("full", false, "do not cut long messages")
	asJSON := fs.Bool("json", false, "print JSON lines")
	var chatQueries multiFlag
	fs.Var(&chatQueries, "chat", "watch only this allowed chat (repeatable)")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	switch {
	case len(pos) > 0:
		return fail(env, exitError, "watch takes no arguments; use --chat")
	case *statePath == "":
		return fail(env, exitError, "watch needs --state <file>")
	case *poll < 1 || *wait < 0:
		return fail(env, exitError, "--poll must be 1 or more and --wait 0 or more")
	}
	sleep := env.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	p, err := policy.Load(env.PolicyPath)
	if err != nil {
		return fail(env, exitError, "%v", err)
	}
	var only map[int64]bool
	if len(chatQueries) > 0 {
		s, _, code := load(env)
		if code != exitOK {
			return code
		}
		all, err := s.Chats()
		_ = s.Close()
		if err != nil {
			return fail(env, exitError, "list chats: %v", err)
		}
		only = map[int64]bool{}
		for _, q := range chatQueries {
			chat, code := pick(env, all, p, q)
			if code != exitOK {
				return code
			}
			only[chat.PK] = true
		}
	}

	if *seed {
		cursor, code := seedCursor(env)
		if code != exitOK {
			return code
		}
		if code := saveState(env, *statePath, watchState{Cursor: cursor}); code != exitOK {
			return code
		}
		fmt.Fprintf(env.Stdout, "seeded at message #%d\n", cursor)
		return exitOK
	}

	st, err := readState(*statePath)
	if err != nil {
		cursor, code := seedCursor(env)
		if code != exitOK {
			return code
		}
		fmt.Fprintf(env.Stdout, "[wa-warn] no usable state (%v); watching from message #%d\n", err, cursor)
		st = watchState{Cursor: cursor}
	}

	opts := render.Options{Location: env.Location, Full: *full, WithChat: true}
	deadline := env.Now().Add(time.Duration(*wait) * time.Second)
	warnedApp := false
	hiddenTotal := 0
	for {
		if !warnedApp && !env.AppRunning() {
			fmt.Fprintln(env.Stdout, "[wa-warn] WhatsApp Desktop is not running; new messages arrive only while it runs")
			warnedApp = true
		}
		shown, hidden, cursor, code := checkOnce(env, p, only, st.Cursor, opts, *asJSON)
		if code != exitOK {
			return code
		}
		st.Cursor = cursor
		hiddenTotal += hidden
		// Only a message wa may print ends the wait. A busy chat that is not on the
		// allowlist is counted, never a reason to wake the reader.
		if shown > 0 || !env.Now().Before(deadline) {
			if shown == 0 {
				fmt.Fprintln(env.Stdout, "(no new messages)")
			}
			switch {
			case hiddenTotal == 1:
				fmt.Fprintln(env.Stdout, "(1 new message in a chat not on the allowlist)")
			case hiddenTotal > 1:
				fmt.Fprintf(env.Stdout, "(%d new messages in chats not on the allowlist)\n", hiddenTotal)
			}
			return saveState(env, *statePath, st)
		}
		sleep(time.Duration(*poll) * time.Second)
	}
}

// checkOnce opens the data fresh, so a change in WhatsApp Desktop's state between
// polls (closed → running) is always seen; an immutable connection would miss it.
func checkOnce(env Env, p *policy.Policy, only map[int64]bool, cursor int64, opts render.Options, asJSON bool) (shown, hidden int, next int64, code int) {
	next = cursor
	s, err := store.OpenStore(env.Dir)
	if err != nil {
		return 0, 0, next, fail(env, exitError, "%v", err)
	}
	defer s.Close()
	if err := s.CheckSchema(); err != nil {
		return 0, 0, next, fail(env, exitError, "%v (run wa doctor)", err)
	}
	all, err := s.Chats()
	if err != nil {
		return 0, 0, next, fail(env, exitError, "list chats: %v", err)
	}
	byPK := make(map[int64]store.Chat, len(all))
	for _, c := range all {
		byPK[c.PK] = c
	}
	msgs, err := s.MessagesAfter(cursor, byPK, watchBatch)
	if err != nil {
		return 0, 0, next, fail(env, exitError, "read new messages: %v", err)
	}
	var show []store.Message
	for _, m := range msgs {
		if m.PK > next {
			next = m.PK
		}
		c, known := byPK[m.ChatPK]
		switch {
		case !known || !p.Allowed(c.JID):
			hidden++
		case only != nil && !only[c.PK]:
			// an allowed chat this watcher does not follow
		default:
			show = append(show, m)
		}
	}
	emit(env, show, opts, asJSON)
	return len(show), hidden, next, exitOK
}

func seedCursor(env Env) (int64, int) {
	s, err := store.OpenStore(env.Dir)
	if err != nil {
		return 0, fail(env, exitError, "%v", err)
	}
	defer s.Close()
	if err := s.CheckSchema(); err != nil {
		return 0, fail(env, exitError, "%v (run wa doctor)", err)
	}
	cursor, err := s.SeedCursor()
	if err != nil {
		return 0, fail(env, exitError, "read the newest message: %v", err)
	}
	return cursor, exitOK
}

func readState(path string) (watchState, error) {
	var st watchState
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, errors.New("no state file yet")
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil || st.Version != 1 || st.Cursor <= 0 {
		return watchState{}, errors.New("state file is unreadable")
	}
	return st, nil
}

// saveState writes the cursor and a heartbeat atomically. A failure is loud: a
// watcher that cannot keep its cursor would print the same messages forever.
func saveState(env Env, path string, st watchState) int {
	st.Version = 1
	st.Heartbeat = env.Now().UTC().Format(time.RFC3339)
	err := func() error {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".watch-*.json")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if err := json.NewEncoder(tmp).Encode(st); err != nil {
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
	}()
	if err != nil {
		fmt.Fprintf(env.Stdout, "[wa-warn] cannot write state: %v\n", err)
		return exitError
	}
	return exitOK
}
