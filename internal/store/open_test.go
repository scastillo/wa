package store

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/scastillo/wa/internal/fixture"
)

// TestMain doubles as a separate writer process: WhatsApp Desktop writes from
// another process, so an in-process writer would not prove the WAL is shared.
func TestMain(m *testing.M) {
	if path := os.Getenv("WA_TEST_WRITER_DB"); path != "" {
		runWriter(path)
		return
	}
	os.Exit(m.Run())
}

func runWriter(path string) {
	t := &testing.T{}
	db := fixture.OpenWritable(t, path+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if _, err := db.Exec("INSERT INTO ZWAMESSAGE (Z_PK, ZMESSAGETYPE) VALUES (42, 0)"); err != nil {
		println("writer insert:", err.Error())
		os.Exit(1)
	}
	os.Stdout.WriteString("ready\n")
	_, _ = io.ReadAll(os.Stdin) // hold the connection open until the test closes stdin
	_ = db.Close()
}

// startWriter launches the writer process and waits until its row sits in the WAL.
func startWriter(t *testing.T, path string) (stop func()) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "WA_TEST_WRITER_DB="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("writer did not become ready: %q %v", line, err)
	}
	return func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}
}

func TestOpenRejectsWritesWhenAppClosed(t *testing.T) {
	fx := fixture.New(t)
	db, err := Open(fx.ChatStorage)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("INSERT INTO ZWAMESSAGE (Z_PK) VALUES (1)")
	if err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("insert must fail with a readonly error, got %v", err)
	}
}

func TestOpenRejectsWritesWhileAppWrites(t *testing.T) {
	fx := fixture.New(t)
	stop := startWriter(t, fx.ChatStorage)
	defer stop()

	db, err := Open(fx.ChatStorage)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec("INSERT INTO ZWAMESSAGE (Z_PK) VALUES (1)")
	if err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Fatalf("insert must fail with a readonly error, got %v", err)
	}
}

func TestOpenReadsRowsStillInTheWAL(t *testing.T) {
	fx := fixture.New(t)
	stop := startWriter(t, fx.ChatStorage)
	defer stop()
	if !slices.Contains(fixture.Files(t, fx.Dir), "ChatStorage.sqlite-wal") {
		t.Fatal("precondition: the writer must keep a -wal file")
	}

	db, err := Open(fx.ChatStorage)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow("SELECT count(*) FROM ZWAMESSAGE WHERE Z_PK = 42").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("row written by the other process: got %d, want 1", n)
	}
}

func TestOpenLeavesNoNewFilesWhenAppClosed(t *testing.T) {
	fx := fixture.New(t)
	before := fixture.Files(t, fx.Dir)

	db, err := Open(fx.ChatStorage)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM ZWAMESSAGE").Scan(&n); err != nil {
		t.Fatal(err)
	}
	during := fixture.Files(t, fx.Dir)
	_ = db.Close()
	after := fixture.Files(t, fx.Dir)

	if !slices.Equal(before, during) || !slices.Equal(before, after) {
		t.Fatalf("wa changed the container:\n before %v\n during %v\n after  %v", before, during, after)
	}
}

func TestOpenLeavesNoNewFilesWhileAppWrites(t *testing.T) {
	fx := fixture.New(t)
	stop := startWriter(t, fx.ChatStorage)
	defer stop()
	before := fixture.Files(t, fx.Dir)

	db, err := Open(fx.ChatStorage)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM ZWAMESSAGE").Scan(&n); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	after := fixture.Files(t, fx.Dir)

	if !slices.Equal(before, after) {
		t.Fatalf("wa changed the container:\n before %v\n after  %v", before, after)
	}
}

func TestOpenNamesTheMissingFile(t *testing.T) {
	_, err := Open("/nonexistent/ChatStorage.sqlite")
	if err == nil || !strings.Contains(err.Error(), "ChatStorage.sqlite") {
		t.Fatalf("want an error naming the file, got %v", err)
	}
}
