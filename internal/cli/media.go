package cli

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/scastillo/wa/internal/media"
	"github.com/scastillo/wa/internal/policy"
	"github.com/scastillo/wa/internal/render"
	"github.com/scastillo/wa/internal/store"
)

var mediaTypeNames = map[string]int{"image": 1, "video": 2, "audio": 3, "document": 8, "sticker": 15}

// mediaCmd saves the attachments of one chat. It works for every chat, but a chat
// not on the allowlist gets counts only and file names without names.
func mediaCmd(args []string, env Env) int {
	fs := newFlags("media", env)
	since := fs.String("since", "", "oldest message time")
	until := fs.String("until", "", "newest message time (exclusive)")
	before := fs.String("before", "", "same as --until")
	limit := fs.Int("limit", 0, "newest attachments only")
	types := fs.String("type", "image,video,audio,document", "comma-separated: image, video, audio, document, sticker")
	from := fs.String("from", "", "part of a sender name, or a sender JID")
	dest := fs.String("dest", "", "destination folder (default ~/Downloads/whatsapp)")
	dryRun := fs.Bool("dry-run", false, "count only; write and download nothing")
	remote := fs.String("remote", "live", "none (disk only), live (also live CDN links) or all (also the phone)")
	asJSON := fs.Bool("json", false, "print the summary as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	if len(pos) != 1 {
		return fail(env, exitError, "media needs exactly one <chat>")
	}
	if *before != "" && *until != "" {
		return fail(env, exitError, "use --until or --before, not both")
	}
	if *before != "" {
		*until = *before
	}
	typeList, err := parseTypes(*types)
	if err != nil {
		return fail(env, exitError, "--type: %v", err)
	}
	mode, err := parseRemote(*remote)
	if err != nil {
		return fail(env, exitError, "--remote: %v", err)
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
	chat, allowed, code := pickAny(env, all, p, pos[0])
	if code != exitOK {
		return code
	}
	root := *dest
	if root == "" {
		root = env.Downloads
	}
	if root == "" {
		return fail(env, exitError, "no destination folder; use --dest")
	}

	aq := store.AttachmentQuery{Query: q, Types: typeList}
	if *from != "" {
		aq.Limit = 0 // filter first, then keep the newest
	}
	atts, err := s.Attachments(chat, aq)
	if err != nil {
		return fail(env, exitError, "list attachments: %v", err)
	}
	if *from != "" {
		needle := strings.ToLower(*from)
		atts = slices.DeleteFunc(atts, func(a store.Attachment) bool {
			return a.SenderJID != *from && !strings.Contains(strings.ToLower(a.Sender), needle)
		})
		if q.Limit > 0 && len(atts) > q.Limit {
			atts = atts[len(atts)-q.Limit:]
		}
	}
	if mode == media.RemoteAll {
		fmt.Fprintln(env.Stderr, "wa: phone fetch is not set up yet; phone-only files stay unavailable")
	}

	res, err := media.Run(context.Background(), chat, atts, media.Options{
		Dest: root, Allowed: allowed, Remote: mode, DryRun: *dryRun,
		Now: env.Now, Location: env.Location, LocalPath: s.LocalPath,
	})
	if err != nil {
		return fail(env, exitError, "save attachments: %v", err)
	}
	if *asJSON {
		out := mediaResultJSON{ChatPK: chat.PK, Dir: res.Dir, DryRun: *dryRun, Found: res.Found, Saved: res.Saved,
			Already: res.Already, Unavailable: res.Unavailable, BadHMAC: res.BadHMAC, TooLarge: res.TooLarge,
			Conflicts: res.Conflicts, PartsRemoved: res.PartsRemoved, WouldCopy: res.WouldCopy, WouldFetch: res.WouldFetch}
		if allowed {
			out.Chat, out.ChatJID = render.Clean(chat.Name), chat.JID
		}
		_ = jsonEncoder(env.Stdout).Encode(out)
		return exitOK
	}
	fmt.Fprintln(env.Stdout, mediaSummary(res, chat, allowed, *dryRun))
	return exitOK
}

// pickAny resolves <chat> for media. One match proceeds whether or not it is
// allowed; several matches list the allowed ones only.
func pickAny(env Env, all []store.Chat, p *policy.Policy, query string) (store.Chat, bool, int) {
	matches := store.Resolve(all, query)
	switch len(matches) {
	case 0:
		return store.Chat{}, false, fail(env, exitError, "no chat matches")
	case 1:
		return matches[0], p.Allowed(matches[0].JID), exitOK
	}
	var allowed []store.Chat
	for _, c := range matches {
		if p.Allowed(c.JID) {
			allowed = append(allowed, c)
		}
	}
	return store.Chat{}, false, listCandidates(env, allowed, len(matches)-len(allowed))
}

func parseTypes(s string) ([]int, error) {
	var out []int
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(strings.ToLower(name))
		t, ok := mediaTypeNames[name]
		if !ok {
			return nil, fmt.Errorf("%q is not image, video, audio, document or sticker", name)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func parseRemote(s string) (media.Remote, error) {
	switch s {
	case "none":
		return media.RemoteNone, nil
	case "live":
		return media.RemoteLive, nil
	case "all":
		return media.RemoteAll, nil
	}
	return 0, fmt.Errorf("%q is not none, live or all", s)
}

func mediaSummary(res media.Result, chat store.Chat, allowed, dryRun bool) string {
	label := fmt.Sprintf("chat #%d", chat.PK)
	if allowed {
		label = fmt.Sprintf("%s (%s)", render.Clean(chat.Name), chat.JID)
	}
	parts := []string{fmt.Sprintf("found %d", res.Found)}
	if dryRun {
		parts = append(parts, fmt.Sprintf("would copy %d", res.WouldCopy), fmt.Sprintf("would fetch %d", res.WouldFetch))
	} else {
		parts = append(parts, fmt.Sprintf("saved %d%s", sum(res.Saved), breakdown(res.Saved)))
	}
	parts = append(parts, fmt.Sprintf("already saved %d", res.Already),
		fmt.Sprintf("unavailable %d%s", sum(res.Unavailable), breakdown(res.Unavailable)))
	if res.BadHMAC > 0 {
		parts = append(parts, fmt.Sprintf("bad MAC %d", res.BadHMAC))
	}
	if res.TooLarge > 0 {
		parts = append(parts, fmt.Sprintf("too large %d", res.TooLarge))
	}
	if res.Conflicts > 0 {
		parts = append(parts, fmt.Sprintf("set aside %d", res.Conflicts))
	}
	if res.PartsRemoved > 0 {
		parts = append(parts, fmt.Sprintf("unfinished files removed %d", res.PartsRemoved))
	}
	head := label + ": "
	if dryRun {
		head = "dry run, " + head
	}
	return head + strings.Join(parts, " · ") + "\nfolder: " + res.Dir
}

func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func breakdown(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, m[k])
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

type mediaResultJSON struct {
	ChatPK       int64          `json:"chat_pk"`
	Chat         string         `json:"chat,omitempty"`
	ChatJID      string         `json:"chat_jid,omitempty"`
	Dir          string         `json:"dir"`
	DryRun       bool           `json:"dry_run"`
	Found        int            `json:"found"`
	Saved        map[string]int `json:"saved"`
	Already      int            `json:"already"`
	Unavailable  map[string]int `json:"unavailable"`
	BadHMAC      int            `json:"bad_hmac"`
	TooLarge     int            `json:"too_large"`
	Conflicts    int            `json:"conflicts"`
	PartsRemoved int            `json:"parts_removed"`
	WouldCopy    int            `json:"would_copy"`
	WouldFetch   int            `json:"would_fetch"`
}
