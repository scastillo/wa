package cli

import (
	"fmt"
	"strings"

	"github.com/scastillo/wa/internal/link"
)

// linkCmd installs wacli and pairs it with the user's phone. Everything here
// runs without a terminal except the scan itself: the QR goes to stdout, and
// the user scans it with their phone.
func linkCmd(args []string, env Env) int {
	fs := newFlags("link", env)
	status := fs.Bool("status", false, "say whether the phone is paired, and stop")
	phone := fs.String("phone", "", "pair with a code for this number instead of a QR")
	version := fs.String("wacli-version", "", "wacli version to install")
	pos, err := parse(fs, args)
	if err != nil {
		return exitError
	}
	if len(pos) > 0 {
		return fail(env, exitError, "link takes no arguments")
	}
	if env.WacliPath == "" || env.Fetch == nil || env.Exec == nil {
		return fail(env, exitError, "linking is not set up on this machine")
	}

	installed, err := link.Install(env.WacliPath, *version, "", env.Fetch)
	if err != nil {
		return fail(env, exitError, "%v", err)
	}
	if installed {
		fmt.Fprintf(env.Stdout, "installed wacli %s at %s\n", link.Version, env.WacliPath)
	}
	if *status {
		return linkStatus(env)
	}

	fmt.Fprintln(env.Stdout, "On your phone: WhatsApp, then Settings, then Linked devices, then Link a device.")
	fmt.Fprintln(env.Stdout, "Then scan the code below. It is good for about a minute.")
	wacliArgs := []string{"auth", "--qr-format", "terminal"}
	if *phone != "" {
		wacliArgs = []string{"auth", "--phone", *phone}
		fmt.Fprintln(env.Stdout, "Or pick Link with phone number, and type the code below.")
	}
	if env.ExecStream == nil {
		return fail(env, exitError, "pairing needs a terminal wa cannot reach")
	}
	if code := env.ExecStream(env.WacliPath, wacliArgs, env.wacliEnv()); code != exitOK {
		return fail(env, exitError, "pairing did not finish; run wa link again")
	}
	return linkStatus(env)
}

// linkStatus reports whether wacli holds a session.
func linkStatus(env Env) int {
	out, errOut, code := env.Exec(env.WacliPath, []string{"auth", "status", "--lock-wait", "5s"}, env.wacliEnv())
	text := strings.TrimSpace(out + errOut)
	switch {
	case code == 0 && strings.Contains(strings.ToLower(text), "authenticated"):
		fmt.Fprintf(env.Stdout, "sending is ready: %s\n", firstLine(text))
		return exitOK
	case strings.Contains(strings.ToLower(text), "locked"):
		return fail(env, exitError, "another wacli is running; wait for it to finish, then try again")
	}
	fmt.Fprintln(env.Stdout, "not paired yet; run wa link and scan the code")
	return exitError
}

// unlinkCmd logs the linked device out. Reading is not affected.
func unlinkCmd(args []string, env Env) int {
	fs := newFlags("unlink", env)
	if _, err := parse(fs, args); err != nil {
		return exitError
	}
	if env.WacliPath == "" || env.Exec == nil {
		return fail(env, exitError, "linking is not set up on this machine")
	}
	out, errOut, code := env.Exec(env.WacliPath, []string{"auth", "logout", "--lock-wait", "5s"}, env.wacliEnv())
	if code != exitOK {
		return fail(env, exitError, "wacli could not log out: %s", firstLine(strings.TrimSpace(errOut+out)))
	}
	fmt.Fprintln(env.Stdout, "the linked device is logged out; wa send is off until you run wa link again")
	return exitOK
}

func (env Env) wacliEnv() []string {
	return []string{"WACLI_STORE_DIR=" + env.WacliStore}
}

func firstLine(s string) string {
	return strings.TrimSpace(strings.SplitN(s, "\n", 2)[0])
}
