package socket

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
)

type fakeControl struct {
	mu           sync.Mutex
	status       coordinator.Status
	triggers     int
	shuttingDown bool
}

func (f *fakeControl) Trigger(coordinator.TriggerSource, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shuttingDown {
		return false
	}
	f.triggers++
	return true
}

func (f *fakeControl) Status(context.Context) (coordinator.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shuttingDown {
		return coordinator.Status{}, errors.New("shutting down")
	}
	return f.status, nil
}

func TestStatusRequest(t *testing.T) {
	root := t.TempDir()
	ctl := &fakeControl{status: coordinator.Status{
		State:     coordinator.ExternalRunning,
		PID:       123,
		AppPort:   8080,
		ProxyPort: 5000,
		Version:   12,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := startTestServer(t, ctx, root, ctl)
	defer server.Close()

	resp := requestJSON(t, server.path, map[string]string{"action": "status"})
	if resp["ok"] != true {
		t.Fatalf("ok = %v, want true; resp=%v", resp["ok"], resp)
	}
	if resp["state"] != "running" || resp["pid"] != float64(123) || resp["app_port"] != float64(8080) || resp["proxy_port"] != float64(5000) || resp["version"] != float64(12) {
		t.Fatalf("unexpected status response: %v", resp)
	}
}

func TestRerunRequest(t *testing.T) {
	root := t.TempDir()
	ctl := &fakeControl{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := startTestServer(t, ctx, root, ctl)
	defer server.Close()

	resp := requestJSON(t, server.path, map[string]string{"action": "rerun"})
	if resp["ok"] != true || resp["status"] != "queued" {
		t.Fatalf("unexpected rerun response: %v", resp)
	}
	ctl.mu.Lock()
	triggers := ctl.triggers
	ctl.mu.Unlock()
	if triggers != 1 {
		t.Fatalf("triggers = %d, want 1", triggers)
	}
}

func TestInvalidRequest(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := startTestServer(t, ctx, root, &fakeControl{})
	defer server.Close()

	resp := requestJSON(t, server.path, map[string]string{"action": "unknown"})
	if resp["ok"] != false || resp["error"] != "invalid_request" {
		t.Fatalf("unexpected invalid response: %v", resp)
	}
}

func TestStaleSocketCleanup(t *testing.T) {
	root := t.TempDir()
	path, err := Path(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), socketDirMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := startTestServer(t, ctx, root, &fakeControl{})
	defer server.Close()

	if _, err := net.DialTimeout("unix", path, time.Second); err != nil {
		t.Fatalf("dial cleaned socket: %v", err)
	}
}

func TestShutdownResponse(t *testing.T) {
	root := t.TempDir()
	ctl := &fakeControl{shuttingDown: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := startTestServer(t, ctx, root, ctl)
	defer server.Close()

	resp := requestJSON(t, server.path, map[string]string{"action": "rerun"})
	if resp["ok"] != false || resp["error"] != "shutting_down" {
		t.Fatalf("unexpected shutdown response: %v", resp)
	}
}

func TestCloseRemovesSocket(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	server := startTestServer(t, ctx, root, &fakeControl{})
	path := server.path
	cancel()
	if err := server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket file after close err = %v, want not exist", err)
	}
}

func startTestServer(t *testing.T, ctx context.Context, root string, ctl *fakeControl) *Server {
	t.Helper()
	server, err := Start(ctx, config.Config{Root: root}, nil, ctl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func requestJSON(t *testing.T, path string, req any) map[string]any {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}
