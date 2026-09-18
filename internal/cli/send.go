package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/scastillo/wa/internal/render"
	"github.com/scastillo/wa/internal/send"
	"github.com/scastillo/wa/internal/store"
)

// sendCmd writes a message to an allowed chat through the linked device.
// It shows what it would send and needs --yes before anything leaves the Mac.
func sendCmd(args []string, env Env) int {
	fs := newFlags("send", env)
	message := fs.String("message", "", "the text to send")
	file := fs.String("file", "", "a file to send (image, video, audio or document)")
	caption := fs.String("caption", "", "caption for a file")
	yes := fs.Bool("yes", false, "really send; without it wa only shows the message")
	dryRun := fs.Bool("dry-run", false, "show what would be sent and stop")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	if len(pos) == 0 {
		return fail(env, exitError, "send needs a chat, then the text or --file")
	}
	if *message == "" && len(pos) > 1 {
		*message = strings.Join(pos[1:], " ")
	}
	if env.WacliPath == "" || env.Exec == nil {
		return fail(env, exitError, "sending is not set up on this machine")
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

	sender := &send.Sender{
		Wacli:      env.WacliPath,
		StoreDir:   env.WacliStore,
		StatePath:  env.SendState,
		Now:        env.Now,
		Sleep:      env.Sleep,
		Jitter:     env.Jitter,
		HasInbound: s.HasInbound,
		Run: func(args, extraEnv []string) (string, string, int) {
			return env.Exec(env.WacliPath, args, extraEnv)
		},
		Out: env.Stdout,
	}
	req := send.Request{
		ChatPK: chat.PK, JID: chat.JID, Name: chat.Name,
		Text: *message, File: *file, Caption: *caption,
		DryRun: *dryRun || !*yes,
	}
	res, err := sender.Send(req)
	if err != nil {
		return sendError(env, err)
	}
	if res.Planned {
		preview(env, chat, req, res)
		if !*dryRun {
			fmt.Fprintln(env.Stderr, "wa: nothing was sent. Add --yes to send it.")
			return exitError
		}
		return exitOK
	}
	fmt.Fprintf(env.Stdout, "sent %s to %s (%s)\n", plural(res.Sent, "message"), render.Clean(chat.Name), chat.JID)
	return exitOK
}

// preview prints exactly what would go out, so nobody sends by accident.
func preview(env Env, chat store.Chat, req send.Request, res send.Result) {
	fmt.Fprintf(env.Stdout, "to %s (%s)\n", render.Clean(chat.Name), chat.JID)
	if req.File != "" {
		fmt.Fprintf(env.Stdout, "file: %s\n", req.File)
		if req.Caption != "" {
			fmt.Fprintf(env.Stdout, "caption: %s\n", req.Caption)
		}
		return
	}
	for i, part := range res.Parts {
		if len(res.Parts) > 1 {
			fmt.Fprintf(env.Stdout, "part %d of %d:\n", i+1, len(res.Parts))
		}
		fmt.Fprintln(env.Stdout, part)
	}
}

func sendError(env Env, err error) int {
	var ban *send.BanError
	var limit *send.LimitError
	switch {
	case errors.As(err, &ban):
		return fail(env, exitError, "%v", ban)
	case errors.As(err, &limit):
		return fail(env, exitError, "%v", limit)
	case errors.Is(err, send.ErrFirstContact):
		return fail(env, exitError, "%v", err)
	case errors.Is(err, send.ErrNotLinked):
		return fail(env, exitError, "%v", err)
	}
	return fail(env, exitError, "send: %v", err)
}
