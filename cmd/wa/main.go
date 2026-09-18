// Command wa reads WhatsApp Desktop data on this Mac, read-only.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/scastillo/wa/internal/cli"
	"github.com/scastillo/wa/internal/policy"
	"github.com/scastillo/wa/internal/store"
)

func main() {
	dir := os.Getenv("WA_DIR")
	if dir == "" {
		d, err := store.DefaultDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "wa:", err)
			os.Exit(1)
		}
		dir = d
	}
	policyPath := os.Getenv("WA_POLICY")
	if policyPath == "" {
		p, err := policy.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "wa:", err)
			os.Exit(1)
		}
		policyPath = p
	}
	home, _ := os.UserHomeDir()

	os.Exit(cli.Run(os.Args[1:], cli.Env{
		Dir:        dir,
		PolicyPath: policyPath,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Stdin:      os.Stdin,
		IsTTY:      func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) },
		Now:        time.Now,
		Location:   time.Local,
		AppInstalled: func() bool {
			_, err := os.Stat("/Applications/WhatsApp.app")
			return err == nil
		},
		AppRunning: func() bool { return exec.Command("pgrep", "-x", "WhatsApp").Run() == nil },
		WacliPath:  filepath.Join(home, ".local", "share", "wa", "bin", "wacli"),
		WacliStore: filepath.Join(home, ".local", "share", "wa", "wacli"),
		SendState:  filepath.Join(home, ".local", "share", "wa", "send-state.json"),
		AllowCmd:   allowCmd(home),
		Jitter:     jitter,
		Exec:       run,
		Downloads:  filepath.Join(home, "Downloads", "whatsapp"),
		Sleep:      time.Sleep,
	}))
}

// run executes another program and collects its output. wa uses it for wacli.
func run(bin string, args, extraEnv []string) (string, string, int) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			return out.String(), errOut.String() + err.Error(), 1
		}
	}
	return out.String(), errOut.String(), code
}

// jitter picks a pause between min and max, so sends do not fall on a fixed beat.
func jitter(min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int64N(int64(max-min)))
}

// allowCmd is what the user types in their own terminal to run wa. The plugin
// launcher keeps a link there; plain "wa" works only inside the agent session.
func allowCmd(home string) string {
	link := filepath.Join(home, ".local", "share", "wa", "bin", "wa")
	if _, err := os.Stat(link); err == nil {
		return link
	}
	return "wa"
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
