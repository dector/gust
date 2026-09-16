package ctl

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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
