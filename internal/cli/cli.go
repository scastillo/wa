// Package cli implements the wa commands.
//
// Privacy rule: a chat that is not on the allowlist never has its name, JID or
// text written to stdout or stderr. Commands only print a count of such chats.
// The one exception is `wa allow`, which must name the candidates it could add.
// `wa allow --all` turns the allowlist off, and then nothing is hidden.
package cli

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scastillo/wa/internal/policy"
	"github.com/scastillo/wa/internal/render"
	"github.com/scastillo/wa/internal/store"
)

// Exit codes.
const (
	exitOK         = 0
	exitError      = 1
	exitAmbiguous  = 2
	exitNotAllowed = 3
)

const (
	defaultLimit = 50
	staleAfter   = 3 * 24 * time.Hour
)

// allowHint tells the user how to add a chat. It names the command they can run
// in their own terminal: plain `wa` exists only inside the agent session, so the
// launcher passes the path of its own link instead.
func allowHint(env Env) string {
	return "run  " + cmdName(env) + " allow --match <name>"
}

// cmdName is how the user starts wa in their own terminal.
func cmdName(env Env) string {
	if env.AllowCmd == "" {
		return "wa"
	}
	return env.AllowCmd
}

const usage = `usage: wa <command> [flags]

commands:
  doctor                   check WhatsApp data, schema, allowlist and tools
  chats    [--match T] [--groups|--dms] [--limit N] [--json]
  allow    --match T|--all add one chat, or every chat, to the allowlist
  disallow <chat>|--all    remove one chat, or turn allow-all off
  read     <chat> [--since D] [--until D] [--limit N] [--full] [--json]
  search   <text> [--chat C] [--since D] [--until D] [--limit N] [--full] [--json]
  media    <chat> [--since D] [--until D|--before D] [--limit N] [--type image,video,audio,document,sticker]
           [--from S] [--dest DIR] [--dry-run] [--remote none|live|all] [--json]
           saves attachments; a chat not on the allowlist gets counts and name-free file names only
  send     <chat> <text>|--file F [--caption C] [--dry-run] [--yes]
           writes through the linked device; shows the message unless --yes is given
  watch    --state F [--seed] [--chat C]... [--wait S] [--poll S] [--full] [--json]
           one-shot: prints new messages and exits, or prints (no new messages) when --wait ends

<chat> is a JID, a chat number from "wa chats", or part of a chat name.
D is YYYY-MM-DD, RFC 3339, or an age such as 36h or 7d.
exit codes: 0 ok, 1 error, 2 more than one chat matches, 3 chat not on the allowlist`

// Env is everything a command touches outside its arguments, so tests can replace it.
type Env struct {
	Dir          string
	PolicyPath   string
	Stdout       io.Writer
	Stderr       io.Writer
	Stdin        io.Reader
	IsTTY        func() bool
	Now          func() time.Time
	Location     *time.Location
	AppInstalled func() bool
	AppRunning   func() bool
	WacliPath    string
	WacliStore   string // wacli's own store, where the linked-device session lives
	SendState    string // where wa keeps its send history and any ban
	AllowCmd     string // the command the user runs in their own terminal, for wa allow
	Downloads    string // default destination of wa media
	Sleep        func(time.Duration)
	Jitter       func(min, max time.Duration) time.Duration
	// Exec runs another program, for wacli. It returns its output and exit code.
	Exec func(bin string, args, extraEnv []string) (stdout, stderr string, code int)
}

// Run executes one wa command and returns its exit code.
func Run(args []string, env Env) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, usage)
		return exitError
	}
	switch args[0] {
	case "doctor":
		return doctor(env)
	case "chats":
		return chats(args[1:], env)
	case "allow":
		return allow(args[1:], env)
	case "disallow":
		return disallow(args[1:], env)
	case "read":
		return read(args[1:], env)
	case "search":
		return search(args[1:], env)
	case "media":
		return mediaCmd(args[1:], env)
	case "send":
		return sendCmd(args[1:], env)
	case "watch":
		return watchCmd(args[1:], env)
	case "help", "-h", "--help":
		fmt.Fprintln(env.Stdout, usage)
		return exitOK
	}
	fmt.Fprintf(env.Stderr, "wa: unknown command %q\n\n%s\n", args[0], usage)
	return exitError
}

func fail(env Env, code int, format string, a ...any) int {
	fmt.Fprintf(env.Stderr, "wa: "+format+"\n", a...)
	return code
}

func newFlags(name string, env Env) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	return fs
}

// parse lets flags come before or after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// load reads the allowlist first, so a broken policy stops the command before any data is read.
func load(env Env) (*store.Store, *policy.Policy, int) {
	p, err := policy.Load(env.PolicyPath)
	if err != nil {
		return nil, nil, fail(env, exitError, "%v", err)
	}
	s, err := store.OpenStore(env.Dir)
	if err != nil {
		return nil, nil, fail(env, exitError, "%v", err)
	}
	if err := s.CheckSchema(); err != nil {
		_ = s.Close()
		return nil, nil, fail(env, exitError, "%v (run wa doctor)", err)
	}
	return s, p, exitOK
}

// pick resolves <chat> and applies the allowlist. It names allowed chats only.
func pick(env Env, all []store.Chat, p *policy.Policy, query string) (store.Chat, int) {
	matches := store.Resolve(all, query)
	var allowed []store.Chat
	for _, c := range matches {
		if p.Allowed(c.JID) {
			allowed = append(allowed, c)
		}
	}
	hidden := len(matches) - len(allowed)
	switch {
	case len(matches) == 0:
		return store.Chat{}, fail(env, exitError, "no chat matches; run wa chats to list allowed chats")
	case len(matches) == 1 && len(allowed) == 1:
		return allowed[0], exitOK
	case len(allowed) == 0 && len(p.Allow) == 0:
		return store.Chat{}, fail(env, exitNotAllowed, "no chat is on the allowlist yet; %s", allowHint(env))
	case len(allowed) == 0:
		return store.Chat{}, fail(env, exitNotAllowed, "that chat is not on the allowlist; %s", allowHint(env))
	}
	return store.Chat{}, listCandidates(env, allowed, hidden)
}

// listCandidates names allowed candidates and counts the rest. It returns exitAmbiguous.
func listCandidates(env Env, allowed []store.Chat, hidden int) int {
	fmt.Fprintln(env.Stderr, "wa: more than one chat matches; use a JID or a chat number:")
	for _, c := range allowed {
		fmt.Fprintf(env.Stderr, "  %s\n", chatLine(c, env))
	}
	if hidden > 0 {
		fmt.Fprintf(env.Stderr, "  (%d more not on the allowlist)\n", hidden)
	}
	return exitAmbiguous
}

func chatLine(c store.Chat, env Env) string {
	last := "-"
	if !c.LastMessageAt.IsZero() {
		last = c.LastMessageAt.In(env.Location).Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("#%-5d %-6s %s  (%s)  last %s  unread %d", c.PK, c.Kind, render.Clean(c.Name), c.JID, last, c.Unread)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func chats(args []string, env Env) int {
	fs := newFlags("chats", env)
	match := fs.String("match", "", "part of a chat name, a JID or a chat number")
	groups := fs.Bool("groups", false, "groups only")
	dms := fs.Bool("dms", false, "direct chats only")
	limit := fs.Int("limit", 0, "maximum chats to list")
	asJSON := fs.Bool("json", false, "print JSON lines")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	if len(pos) > 0 {
		return fail(env, exitError, "chats takes no arguments; use --match")
	}
	s, p, code := load(env)
	if code != exitOK {
		return code
	}
	defer s.Close()
	all, err := s.Chats()
	if err != nil {
		return fail(env, exitError, "list chats: %v", err)
	}
	list := all
	if *match != "" {
		list = store.Resolve(all, *match)
	}
	var shown []store.Chat
	hidden := 0
	for _, c := range list {
		if (*groups && c.Kind != store.KindGroup) || (*dms && c.Kind != store.KindDM) {
			continue
		}
		if !p.Allowed(c.JID) {
			hidden++
			continue
		}
		shown = append(shown, c)
	}
	if *limit > 0 && len(shown) > *limit {
		shown = shown[:*limit]
	}
	enc := jsonEncoder(env.Stdout)
	for _, c := range shown {
		if *asJSON {
			_ = enc.Encode(chatJSON{PK: c.PK, JID: c.JID, Name: render.Clean(c.Name), Kind: string(c.Kind),
				LastMessageAt: timeString(c.LastMessageAt, env), Unread: c.Unread})
		} else {
			fmt.Fprintln(env.Stdout, chatLine(c, env))
		}
	}
	out := env.Stdout
	if *asJSON {
		out = env.Stderr
	}
	if hidden > 0 {
		fmt.Fprintf(out, "(%s not on the allowlist)\n", plural(hidden, "chat"))
	}
	if !p.All && len(p.Allow) == 0 {
		fmt.Fprintf(env.Stderr, "wa: no chat is on the allowlist yet; %s\n", allowHint(env))
	}
	return exitOK
}

func read(args []string, env Env) int {
	fs := newFlags("read", env)
	since := fs.String("since", "", "oldest message time")
	until := fs.String("until", "", "newest message time (exclusive)")
	limit := fs.Int("limit", 0, "maximum messages (the newest are kept)")
	full := fs.Bool("full", false, "do not cut long messages")
	asJSON := fs.Bool("json", false, "print JSON lines")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	if len(pos) != 1 {
		return fail(env, exitError, "read needs exactly one <chat>")
	}
	q, code := query(env, *since, *until, *limit)
	if code != exitOK {
		return code
	}
	s, p, code := load(env)
	if code != exitOK {
		return code
	}
	defer s.Close()
	all, err := s.Chats()
	if err != nil {
		return fail(env, exitError, "list chats: %v", err)
	}
	chat, code := pick(env, all, p, pos[0])
	if code != exitOK {
		return code
	}
	if q.Limit == 0 && q.Since.IsZero() {
		q.Limit = defaultLimit
	}
	msgs, err := s.Messages(chat, q)
	if err != nil {
		return fail(env, exitError, "read messages: %v", err)
	}
	emit(env, msgs, render.Options{Location: env.Location, Full: *full}, *asJSON)
	return exitOK
}

func search(args []string, env Env) int {
	fs := newFlags("search", env)
	chatQuery := fs.String("chat", "", "search one allowed chat only")
	since := fs.String("since", "", "oldest message time")
	until := fs.String("until", "", "newest message time (exclusive)")
	limit := fs.Int("limit", 0, "maximum results (default 50)")
	full := fs.Bool("full", false, "do not cut long messages")
	asJSON := fs.Bool("json", false, "print JSON lines")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	text := strings.TrimSpace(strings.Join(pos, " "))
	if text == "" {
		return fail(env, exitError, "search needs <text>")
	}
	q, code := query(env, *since, *until, *limit)
	if code != exitOK {
		return code
	}
	s, p, code := load(env)
	if code != exitOK {
		return code
	}
	defer s.Close()
	all, err := s.Chats()
	if err != nil {
		return fail(env, exitError, "list chats: %v", err)
	}
	var scope []store.Chat
	if *chatQuery != "" {
		chat, code := pick(env, all, p, *chatQuery)
		if code != exitOK {
			return code
		}
		scope = []store.Chat{chat}
	} else {
		for _, c := range all {
			if p.Allowed(c.JID) {
				scope = append(scope, c)
			}
		}
	}
	if !p.All && len(p.Allow) == 0 {
		fmt.Fprintf(env.Stderr, "wa: no chat is on the allowlist yet; %s\n", allowHint(env))
	}
	if q.Limit == 0 {
		q.Limit = defaultLimit
	}
	hits, err := s.Search(text, scope, q)
	if err != nil {
		return fail(env, exitError, "search: %v", err)
	}
	emit(env, hits, render.Options{Location: env.Location, Full: *full, WithChat: true}, *asJSON)
	return exitOK
}

func allow(args []string, env Env) int {
	fs := newFlags("allow", env)
	match := fs.String("match", "", "part of a chat name, a JID or a chat number")
	every := fs.Bool("all", false, "allow every chat, now and in the future")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	if *match == "" && len(pos) == 1 {
		*match = pos[0]
	}
	if *every {
		if *match != "" {
			return fail(env, exitError, "use either --all or --match <name>, not both")
		}
		return allowEvery(env)
	}
	if *match == "" {
		return fail(env, exitError, "allow needs --match <name>, or --all for every chat")
	}
	s, p, code := load(env)
	if code != exitOK {
		return code
	}
	defer s.Close()
	all, err := s.Chats()
	if err != nil {
		return fail(env, exitError, "list chats: %v", err)
	}
	matches := store.Resolve(all, *match)
	if len(matches) == 0 {
		return fail(env, exitError, "no chat matches")
	}
	in := bufio.NewReader(env.Stdin)
	chosen := matches[0]
	if len(matches) > 1 {
		// Without a terminal there is nobody to answer, so name the candidates
		// and let the caller pick one by JID.
		if !env.IsTTY() {
			fmt.Fprintln(env.Stderr, "wa: more than one chat matches; allow one by JID:")
			for _, c := range matches {
				fmt.Fprintf(env.Stderr, "  %s\n", chatLine(c, env))
			}
			return exitAmbiguous
		}
		for i, c := range matches {
			fmt.Fprintf(env.Stdout, "%d) %s\n", i+1, chatLine(c, env))
		}
		fmt.Fprint(env.Stdout, "pick a number: ")
		n, err := strconv.Atoi(readLine(in))
		if err != nil || n < 1 || n > len(matches) {
			return fail(env, exitError, "no chat picked; nothing changed")
		}
		chosen = matches[n-1]
	}
	if p.Allowed(chosen.JID) {
		fmt.Fprintf(env.Stdout, "%s is already allowed\n", chosen.JID)
		return exitOK
	}
	if env.IsTTY() {
		fmt.Fprintf(env.Stdout, "Allow wa to print messages from %s (%s)? [y/N] ", render.Clean(chosen.Name), chosen.JID)
		if answer := strings.ToLower(readLine(in)); answer != "y" && answer != "yes" {
			fmt.Fprintln(env.Stdout, "nothing changed")
			return exitError
		}
	}
	p.Add(chosen.JID, env.Now().In(env.Location).Format("2006-01-02"))
	if err := p.Save(env.PolicyPath); err != nil {
		return fail(env, exitError, "save allowlist: %v", err)
	}
	fmt.Fprintf(env.Stdout, "allowed %s\n", chosen.JID)
	return exitOK
}

// allowEvery turns the allowlist off: every chat becomes readable. The per-chat
// list stays, so disallow --all puts it back in charge.
func allowEvery(env Env) int {
	p, err := policy.Load(env.PolicyPath)
	if err != nil {
		return fail(env, exitError, "%v", err)
	}
	if p.All {
		fmt.Fprintln(env.Stdout, "every chat is already allowed")
		return exitOK
	}
	if env.IsTTY() {
		fmt.Fprint(env.Stdout, "Allow wa to print messages from EVERY chat, now and in the future? [y/N] ")
		if answer := strings.ToLower(readLine(bufio.NewReader(env.Stdin))); answer != "y" && answer != "yes" {
			fmt.Fprintln(env.Stdout, "nothing changed")
			return exitError
		}
	}
	p.All = true
	if err := p.Save(env.PolicyPath); err != nil {
		return fail(env, exitError, "save allowlist: %v", err)
	}
	fmt.Fprintf(env.Stdout, "every chat is now readable by an AI session. Undo with  %s disallow --all\n", cmdName(env))
	return exitOK
}

func disallow(args []string, env Env) int {
	fs := newFlags("disallow", env)
	every := fs.Bool("all", false, "turn allow-all off, so only listed chats stay readable")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	if *every {
		if len(pos) != 0 {
			return fail(env, exitError, "use either --all or a <chat>, not both")
		}
		p, err := policy.Load(env.PolicyPath)
		if err != nil {
			return fail(env, exitError, "%v", err)
		}
		p.All = false
		if err := p.Save(env.PolicyPath); err != nil {
			return fail(env, exitError, "save allowlist: %v", err)
		}
		fmt.Fprintf(env.Stdout, "allow-all is off; %s stay readable\n", plural(len(p.Allow), "chat"))
		return exitOK
	}
	if len(pos) != 1 {
		return fail(env, exitError, "disallow needs exactly one <chat>")
	}
	p, err := policy.Load(env.PolicyPath)
	if err != nil {
		return fail(env, exitError, "%v", err)
	}
	target := pos[0]
	if !p.Allowed(target) {
		s, _, code := load(env)
		if code != exitOK {
			return code
		}
		defer s.Close()
		all, err := s.Chats()
		if err != nil {
			return fail(env, exitError, "list chats: %v", err)
		}
		var allowed []store.Chat
		for _, c := range all {
			if p.Allowed(c.JID) {
				allowed = append(allowed, c)
			}
		}
		matches := store.Resolve(allowed, target)
		switch len(matches) {
		case 0:
			return fail(env, exitError, "no allowed chat matches")
		case 1:
			target = matches[0].JID
		default:
			fmt.Fprintln(env.Stderr, "wa: more than one allowed chat matches; use a JID:")
			for _, c := range matches {
				fmt.Fprintf(env.Stderr, "  %s\n", chatLine(c, env))
			}
			return exitAmbiguous
		}
	}
	p.Remove(target)
	if err := p.Save(env.PolicyPath); err != nil {
		return fail(env, exitError, "save allowlist: %v", err)
	}
	fmt.Fprintf(env.Stdout, "removed %s\n", target)
	return exitOK
}

func doctor(env Env) int {
	failed := false
	report := func(status, area, format string, a ...any) {
		fmt.Fprintf(env.Stdout, "%-4s %-9s %s\n", status, area, fmt.Sprintf(format, a...))
		if status == "FAIL" {
			failed = true
		}
	}

	if env.AppInstalled() {
		report("ok", "app", "WhatsApp Desktop is installed")
	} else {
		report("FAIL", "app", "WhatsApp Desktop is not in /Applications")
	}
	running := env.AppRunning()
	if running {
		report("ok", "app", "WhatsApp Desktop is running")
	} else {
		report("warn", "app", "WhatsApp Desktop is not running; new messages arrive only while it runs")
	}

	if s, err := store.OpenStore(env.Dir); err != nil {
		report("FAIL", "data", "%v", err)
	} else {
		defer s.Close()
		mode := "read-only (mode=ro, query_only)"
		if s.Chat.Immutable {
			mode += " and immutable (app closed)"
		}
		report("ok", "data", "ChatStorage.sqlite opens %s", mode)
		if s.Contacts == nil {
			report("warn", "data", "ContactsV2.sqlite is missing; senders show push names")
		}
		if err := s.CheckSchema(); err != nil {
			report("FAIL", "schema", "%v", err)
		} else if all, err := s.Chats(); err != nil {
			report("FAIL", "data", "list chats: %v", err)
		} else {
			report("ok", "schema", "every required column is present (built against %s)", store.SchemaVersion)
			newest, err := s.NewestMessage(env.Now())
			if err != nil {
				report("FAIL", "data", "read the newest message: %v", err)
			}
			switch age := env.Now().Sub(newest); {
			case newest.IsZero():
				report("warn", "data", "%s, no messages", plural(len(all), "chat"))
			case running && age > staleAfter:
				report("warn", "data", "%s; newest message is %s old, so the data may be stale", plural(len(all), "chat"), age.Round(time.Hour))
			default:
				report("ok", "data", "%s; newest message %s ago", plural(len(all), "chat"), age.Round(time.Minute))
			}
		}
	}

	if p, err := policy.Load(env.PolicyPath); err != nil {
		report("FAIL", "allowlist", "%v", err)
	} else if p.All {
		report("warn", "allowlist", "every chat is readable; undo with  %s disallow --all", cmdName(env))
	} else if len(p.Allow) == 0 {
		report("warn", "allowlist", "no chat allowed; %s", allowHint(env))
	} else {
		report("ok", "allowlist", "%s allowed (%s)", plural(len(p.Allow), "chat"), env.PolicyPath)
	}

	// Reading needs no wacli, so a missing one is not worth a line.
	if _, err := os.Stat(env.WacliPath); err == nil {
		report("ok", "wacli", "installed at %s (phone link not checked yet)", env.WacliPath)
	}

	if failed {
		return exitError
	}
	return exitOK
}

func query(env Env, since, until string, limit int) (store.Query, int) {
	q := store.Query{Limit: limit}
	if limit < 0 {
		return q, fail(env, exitError, "--limit must be 0 or more")
	}
	var err error
	if since != "" {
		if q.Since, err = parseWhen(since, env.Now(), env.Location); err != nil {
			return q, fail(env, exitError, "--since: %v", err)
		}
	}
	if until != "" {
		if q.Until, err = parseWhen(until, env.Now(), env.Location); err != nil {
			return q, fail(env, exitError, "--until: %v", err)
		}
	}
	return q, exitOK
}

// parseWhen reads YYYY-MM-DD (local midnight), RFC 3339, or an age such as 36h or 7d.
func parseWhen(s string, now time.Time, loc *time.Location) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%q is not YYYY-MM-DD, RFC 3339, or an age such as 36h or 7d", s)
}

type chatJSON struct {
	PK            int64  `json:"pk"`
	JID           string `json:"jid"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	LastMessageAt string `json:"last_message_at,omitempty"`
	Unread        int    `json:"unread"`
}

type mediaJSON struct {
	PK    int64  `json:"pk"`
	Size  int64  `json:"size"`
	State string `json:"state"`
	Title string `json:"title,omitempty"`
}

type messageJSON struct {
	Chat      string     `json:"chat"`
	ChatJID   string     `json:"chat_jid"`
	PK        int64      `json:"pk"`
	SentAt    string     `json:"sent_at"`
	FromMe    bool       `json:"from_me"`
	Sender    string     `json:"sender"`
	SenderJID string     `json:"sender_jid,omitempty"`
	Type      string     `json:"type"`
	Text      string     `json:"text,omitempty"`
	Media     *mediaJSON `json:"media,omitempty"`
}

func emit(env Env, msgs []store.Message, o render.Options, asJSON bool) {
	if !asJSON {
		for _, m := range msgs {
			fmt.Fprintln(env.Stdout, render.Line(m, o))
		}
		return
	}
	enc := jsonEncoder(env.Stdout)
	for _, m := range msgs {
		text := render.Clean(m.Text)
		if !o.Full && utf8.RuneCountInString(text) > render.MaxRunes {
			text = string([]rune(text)[:render.MaxRunes]) + "…"
		}
		if store.IsSystem(m.Type) {
			text = ""
		}
		j := messageJSON{Chat: render.Clean(m.ChatName), ChatJID: m.ChatJID, PK: m.PK, SentAt: timeString(m.SentAt, env),
			FromMe: m.FromMe, Sender: render.Clean(m.Sender), SenderJID: m.SenderJID, Type: render.TypeName(m.Type), Text: text}
		if m.Media != nil {
			j.Media = &mediaJSON{PK: m.Media.PK, Size: m.Media.Size, State: string(m.Media.State), Title: render.Clean(m.Media.Title)}
		}
		_ = enc.Encode(j)
	}
}

func jsonEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

func timeString(t time.Time, env Env) string {
	if t.IsZero() {
		return ""
	}
	return t.In(env.Location).Format(time.RFC3339)
}

func readLine(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}
