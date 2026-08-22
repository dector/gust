package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStartAddsGustEnvironment(t *testing.T) {
	t.Setenv("GUST_TEST_INHERITED", "yes")

	var stdout bytes.Buffer
	p, err := Start(Options{
		Command:      `printf '%s|%s|%s|%s' "$GUST" "$GUST_APP_PORT" "$GUST_PROXY_PORT" "$GUST_TEST_INHERITED"`,
		HasAppPort:   true,
		AppPort:      8080,
		ProxyEnabled: true,
		ProxyPort:    5000,
		Stdout:       &stdout,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if event, err := p.Wait(context.Background()); err != nil || event.Code != 0 {
		t.Fatalf("Wait() = (%+v, %v), want code 0", event, err)
	}

	got := stdout.String()
	want := "1|8080|5000|yes"
	if got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestWaitReportsExitEvent(t *testing.T) {
	p, err := Start(Options{Command: `exit 7`})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	event, err := p.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if event.PID != p.PID() {
		t.Fatalf("event.PID = %d, want %d", event.PID, p.PID())
	}
	if event.Code != 7 {
		t.Fatalf("event.Code = %d, want 7", event.Code)
	}
	if event.Err == nil {
		t.Fatal("event.Err is nil, want exit error")
	}
}

func TestStopTerminatesProcessGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process integration test in short mode")
	}

	dir := t.TempDir()
	pidFile := filepath.Join(dir, "sleep.pid")
	script := `sleep 30 & echo $! > ` + shellQuote(pidFile) + `; wait`
	p, err := Start(Options{Command: script})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	sleepPID := waitForPIDFile(t, pidFile)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	assertNoProcessEventually(t, p.PID())
	assertNoProcessEventually(t, sleepPID)
}

func TestStopKillsAfterTimeoutAndReaps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping process integration test in short mode")
	}

	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	p, err := Start(Options{Command: `trap '' TERM; echo ready > ` + shellQuote(readyFile) + `; while :; do sleep 1; done`})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForFile(t, readyFile)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	event, err := p.Stop(ctx)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("Stop() returned before kill timeout: %v", elapsed)
	}
	if event.PID != p.PID() {
		t.Fatalf("event.PID = %d, want %d", event.PID, p.PID())
	}
	if event.Code != -1 {
		t.Fatalf("event.Code = %d, want -1 for signal", event.Code)
	}
	assertNoProcessEventually(t, p.PID())
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	waitForFile(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("invalid pid file: %q", data)
	}
	return pid
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for file %s", path)
}

func assertNoProcessEventually(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still exists", pid)
}

func shellQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}
