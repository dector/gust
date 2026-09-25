package ctl

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dector/gust/internal/comments"
	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/socket"
)

type fakeControl struct {
	status           coordinator.Status
	logs             coordinator.Logs
	hasLogs          bool
	triggers         int
	autoReloadPaused bool
}

func (f *fakeControl) Trigger(coordinator.TriggerSource, string) bool {
	f.triggers++
	return true
}

func (f *fakeControl) Status(context.Context) (coordinator.Status, error) {
	return f.status, nil
}

func (f *fakeControl) SetAutoReload(_ context.Context, paused bool) (bool, error) {
	f.autoReloadPaused = paused
	return paused, nil
}

func (f *fakeControl) Logs(context.Context) (coordinator.Logs, error) {
	if !f.hasLogs {
		return coordinator.Logs{}, coordinator.ErrNoLogs
	}
	return f.logs, nil
}

func startServer(t *testing.T, ctl *fakeControl) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server, err := socket.Start(ctx, config.Config{Root: t.TempDir()}, nil, ctl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server.Path()
}

func runCtl(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCommentsCommandsAndRecovery(t *testing.T) {
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	c1, err := store.Create(ctx, comments.Input{Path: "/", Text: "fix"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := store.Create(ctx, comments.Input{Path: "/two", Text: "also"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := store.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeControl{}
	serverCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	server, err := socket.Start(serverCtx, config.Config{Root: t.TempDir()}, nil, fake, store)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	code, out, stderr := runCtl(t, "-S", server.Path(), "comments", "--wait")
	if code != 0 {
		t.Fatalf("wait: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, `"id":"`+batch.ID+`"`) || !strings.Contains(out, `"state":"seen"`) {
		t.Fatalf("batch JSON: %s", out)
	}
	code, out, stderr = runCtl(t, "comments", "-S", server.Path(), "--pending")
	if code != 0 {
		t.Fatalf("pending: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, c1.ID) || !strings.Contains(out, c2.ID) {
		t.Fatalf("pending output: %s", out)
	}
	code, out, stderr = runCtl(t, "-S", server.Path(), "comments", "reply", c1.ID, "working on it")
	if code != 0 || !strings.Contains(out, `"state":"seen"`) || !strings.Contains(out, `"author":"agent"`) {
		t.Fatalf("reply: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runCtl(t, "-S", server.Path(), "comments", "review", c2.ID, "please verify")
	if code != 0 || !strings.Contains(out, `"state":"review"`) || !strings.Contains(out, "please verify") {
		t.Fatalf("review: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, out, stderr = runCtl(t, "-S", server.Path(), "comments", "done", c1.ID)
	if code != 0 || !strings.Contains(out, `"state":"done"`) {
		t.Fatalf("done: code=%d out=%q stderr=%q", code, out, stderr)
	}
	code, _, stderr = runCtl(t, "-S", server.Path(), "comments", "reply", c1.ID, "   ")
	if code != 2 || !strings.Contains(stderr, "invalid comments command") {
		t.Fatalf("blank reply: code=%d stderr=%q", code, stderr)
	}
}

func TestCommentsDefaultListsAllUnfinishedAndPendingOnlySeen(t *testing.T) {
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	// seen: submit and claim immediately.
	seen, err := store.Create(ctx, comments.Input{Path: "/seen", Text: "seen"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	// done: submit, claim, then finish.
	done, err := store.Create(ctx, comments.Input{Path: "/done", Text: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDone(ctx, done.ID); err != nil {
		t.Fatal(err)
	}
	// review: submit, claim, then mark review.
	review, err := store.Create(ctx, comments.Input{Path: "/review", Text: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NextBatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Review(ctx, review.ID, "please check"); err != nil {
		t.Fatal(err)
	}
	// submitted: submit and leave unclaimed.
	submitted, err := store.Create(ctx, comments.Input{Path: "/submitted", Text: "submitted"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitCreated(ctx); err != nil {
		t.Fatal(err)
	}
	// created: never submitted.
	created, err := store.Create(ctx, comments.Input{Path: "/created", Text: "created"})
	if err != nil {
		t.Fatal(err)
	}

	server, err := socket.Start(ctx, config.Config{Root: t.TempDir()}, nil, &fakeControl{}, store)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	code, out, stderr := runCtl(t, "-S", server.Path(), "comments")
	if code != 0 {
		t.Fatalf("default: code=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{created.ID, submitted.ID, seen.ID, review.ID} {
		if !strings.Contains(out, want) {
			t.Fatalf("default output %q missing %s", out, want)
		}
	}
	for _, unwanted := range []string{done.ID} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("default output %q includes %s", out, unwanted)
		}
	}

	code, out, stderr = runCtl(t, "-S", server.Path(), "comments", "--pending")
	if code != 0 {
		t.Fatalf("pending: code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(out, seen.ID) {
		t.Fatalf("pending output %q missing %s", out, seen.ID)
	}
	for _, unwanted := range []string{created.ID, submitted.ID, done.ID, review.ID} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("pending output %q includes %s", out, unwanted)
		}
	}
}

func TestCommentsWaitCancellationDoesNotClaim(t *testing.T) {
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	_, _ = store.Create(ctx, comments.Input{Path: "/", Text: "pending"})
	_, _ = store.SubmitCreated(ctx)
	serverCtx, stop := context.WithCancel(ctx)
	defer stop()
	server, err := socket.Start(serverCtx, config.Config{Root: t.TempDir()}, nil, &fakeControl{}, store)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	waitCtx, cancel := context.WithCancel(ctx)
	done := make(chan int, 1)
	go func() {
		done <- Run(waitCtx, []string{"-S", server.Path(), "comments", "--wait"}, &bytes.Buffer{}, &bytes.Buffer{})
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait did not cancel")
	}
	list, err := store.List(ctx, comments.StateSubmitted)
	if err != nil || len(list) != 1 {
		t.Fatalf("submitted comments=%d err=%v", len(list), err)
	}
}

func TestCommentsWaitReconnectsAfterRestart(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store1, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	srv1Ctx, stop1 := context.WithCancel(ctx)
	srv1, err := socket.Start(srv1Ctx, config.Config{Root: root}, nil, &fakeControl{}, store1)
	if err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"-S", srv1.Path(), "comments", "--wait"}, &out, &errOut)
	}()

	// Let the wait connect and block, then restart Gust on the same socket.
	time.Sleep(200 * time.Millisecond)
	stop1()
	_ = srv1.Close()
	_ = store1.Close()

	store2, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	if _, err := store2.Create(ctx, comments.Input{Path: "/", Text: "after restart"}); err != nil {
		t.Fatal(err)
	}
	batch, err := store2.SubmitCreated(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv2Ctx, stop2 := context.WithCancel(ctx)
	defer stop2()
	srv2, err := socket.Start(srv2Ctx, config.Config{Root: root}, nil, &fakeControl{}, store2)
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("wait code=%d stderr=%q", code, errOut.String())
		}
		if !strings.Contains(out.String(), batch.ID) {
			t.Fatalf("wait output after restart: %s", out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not reconnect after restart")
	}
}

func TestCommentsWaitTimesOutWhenGustUnreachable(t *testing.T) {
	prev := waitReconnectWindow
	waitReconnectWindow = 200 * time.Millisecond
	defer func() { waitReconnectWindow = prev }()

	path := filepath.Join(t.TempDir(), "missing.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out, errOut bytes.Buffer
	start := time.Now()
	code := Run(ctx, []string{"-S", path, "comments", "--wait"}, &out, &errOut)
	if code == 0 {
		t.Fatalf("wait unexpectedly succeeded: %q", out.String())
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("wait took %s, want bounded by the reconnect window", elapsed)
	}
	if !strings.Contains(errOut.String(), "cannot reach gust") {
		t.Fatalf("stderr=%q", errOut.String())
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}} {
		code, out, _ := runCtl(t, args...)
		if code != 0 {
			t.Fatalf("args %v code = %d, want 0", args, code)
		}
		if !strings.Contains(out, "Usage:") || !strings.Contains(out, "gust ctl") {
			t.Fatalf("args %v help output = %q", args, out)
		}
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantSocket string
		wantVerb   string
		wantErr    bool
	}{
		{name: "empty"},
		{name: "verb only", args: []string{"pause"}, wantVerb: "pause"},
		{name: "socket before verb", args: []string{"-S", "/tmp/x.sock", "pause"}, wantSocket: "/tmp/x.sock", wantVerb: "pause"},
		{name: "socket after verb", args: []string{"status", "-S", "/tmp/x.sock"}, wantSocket: "/tmp/x.sock", wantVerb: "status"},
		{name: "short equals", args: []string{"-S=/tmp/x.sock", "logs"}, wantSocket: "/tmp/x.sock", wantVerb: "logs"},
		{name: "long", args: []string{"--socket", "/tmp/x.sock", "status"}, wantSocket: "/tmp/x.sock", wantVerb: "status"},
		{name: "long equals", args: []string{"--socket=/tmp/x.sock", "status"}, wantSocket: "/tmp/x.sock", wantVerb: "status"},
		{name: "short help", args: []string{"-h"}, wantVerb: "help"},
		{name: "missing socket value", args: []string{"-S"}, wantErr: true},
		{name: "empty socket value", args: []string{"-S="}, wantErr: true},
		{name: "unexpected argument", args: []string{"status", "extra"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			socketPath, verb, err := parseArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseArgs(%v) err = nil, want error", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%v) err = %v", tt.args, err)
			}
			if socketPath != tt.wantSocket || verb != tt.wantVerb {
				t.Fatalf("parseArgs(%v) = %q, %q; want %q, %q", tt.args, socketPath, verb, tt.wantSocket, tt.wantVerb)
			}
		})
	}
}

func TestStatusCommand(t *testing.T) {
	ctl := &fakeControl{status: coordinator.Status{
		State:            coordinator.ExternalRunning,
		PID:              123,
		AutoReloadPaused: true,
		Version:          4,
		AppPort:          8080,
		ProxyPort:        5000,
		Process: coordinator.ProcessStatus{
			LastExit: &coordinator.LastExit{Code: 1, At: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Error: true},
		},
	}}
	path := startServer(t, ctl)

	code, out, errOut := runCtl(t, statusArgs(path, "status")...)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"state: running", "pid: 123", "auto_reload: paused", "version: 4", "app_port: 8080", "proxy_port: 5000", "last_exit: code=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output %q missing %q", out, want)
		}
	}
}

func TestPauseAndResumeCommands(t *testing.T) {
	ctl := &fakeControl{}
	path := startServer(t, ctl)

	code, out, errOut := runCtl(t, statusArgs(path, "pause")...)
	if code != 0 {
		t.Fatalf("pause code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "auto_reload: paused") || !ctl.autoReloadPaused {
		t.Fatalf("pause out = %q, paused = %t", out, ctl.autoReloadPaused)
	}

	code, out, errOut = runCtl(t, statusArgs(path, "resume")...)
	if code != 0 {
		t.Fatalf("resume code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "auto_reload: active") || ctl.autoReloadPaused {
		t.Fatalf("resume out = %q, paused = %t", out, ctl.autoReloadPaused)
	}
}

func TestRerunCommand(t *testing.T) {
	ctl := &fakeControl{}
	path := startServer(t, ctl)

	code, out, errOut := runCtl(t, statusArgs(path, "rerun")...)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "status: queued") || ctl.triggers != 1 {
		t.Fatalf("rerun out = %q, triggers = %d", out, ctl.triggers)
	}
}

func TestLogsCommand(t *testing.T) {
	ctl := &fakeControl{
		hasLogs: true,
		logs: coordinator.Logs{
			Code:   1,
			At:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			Stdout: "some stdout",
			Stderr: "some stderr",
		},
	}
	path := startServer(t, ctl)

	code, out, errOut := runCtl(t, statusArgs(path, "logs")...)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"last_exit: code=1", "--- stdout ---", "some stdout", "--- stderr ---", "some stderr"} {
		if !strings.Contains(out, want) {
			t.Fatalf("logs output %q missing %q", out, want)
		}
	}
}

func TestLogsCommandWithoutFailure(t *testing.T) {
	ctl := &fakeControl{}
	path := startServer(t, ctl)

	code, _, errOut := runCtl(t, statusArgs(path, "logs")...)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "no_failure_logs") {
		t.Fatalf("stderr = %q, want no_failure_logs", errOut)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := runCtl(t, "bogus")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestUnexpectedArgument(t *testing.T) {
	code, _, errOut := runCtl(t, "status", "extra")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unexpected argument") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestNoSocket(t *testing.T) {
	code, _, errOut := runCtl(t, "-S", "/nonexistent/gust.sock", "status")
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "cannot reach gust") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func statusArgs(path string, verb ...string) []string {
	args := []string{"-S", path}
	return append(args, verb...)
}
