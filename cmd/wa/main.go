// Command wa reads WhatsApp Desktop data on this Mac, read-only.
package main

import (
	"fmt"
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
		Downloads:  filepath.Join(home, "Downloads", "whatsapp"),
		Sleep:      time.Sleep,
	}))
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
