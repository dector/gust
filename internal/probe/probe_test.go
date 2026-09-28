package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/protocol"
	"github.com/dector/gust/internal/socket"
)

func serveStatus(t *testing.T, dir, name string, resp protocol.Response, hang bool) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", filepath.Join(dir, name+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var req protocol.Request
				if json.NewDecoder(conn).Decode(&req) != nil || req.Action != protocol.ActionStatus {
					return
				}
				if hang {
					<-time.After(2 * probeTimeout)
					return
				}
				_ = json.NewEncoder(conn).Encode(resp)
			}()
		}
	}()
	return ln
}

func TestProbeListsLiveInstancesAndIgnoresStaleSockets(t *testing.T) {
	dir := t.TempDir()
	serveStatus(t, dir, "b", protocol.Response{OK: true, Root: "/work/b", State: "running", AppPort: 3001, ProxyPort: 4001, TailscaleURL: "https://b.ts.net/"}, false)
	serveStatus(t, dir, "a", protocol.Response{OK: true, Root: "/work/a", State: "restarting"}, false)
	serveStatus(t, dir, "hung", protocol.Response{OK: true}, true)
	stale := serveStatus(t, dir, "stale", protocol.Response{}, false)
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if err := os.WriteFile(filepath.Join(dir, "not-a-socket.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), dir, &out); err != nil {
		t.Fatal(err)
	}
	want := "/work/a (restarting)\n  local: unknown (no -p)\n\n/work/b (running)\n  local: http://127.0.0.1:4001/\n  tailscale: https://b.ts.net/\n"
	if out.String() != want {
		t.Fatalf("output:\n%q\nwant:\n%q", out.String(), want)
	}
	if _, err := os.Lstat(filepath.Join(dir, "stale.sock")); err != nil {
		t.Fatalf("probe removed stale socket: %v", err)
	}
}

type probeControl struct{}

func (probeControl) Trigger(coordinator.TriggerSource, string) bool { return true }
func (probeControl) Status(context.Context) (coordinator.Status, error) {
	return coordinator.Status{}, nil
}
func (probeControl) SetAutoReload(context.Context, bool) (bool, error) { return false, nil }
func (probeControl) Logs(context.Context) (coordinator.Logs, error) {
	return coordinator.Logs{}, nil
}

func TestProbeTwoInstancesInSameWorkdir(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := socket.Start(ctx, config.Config{Root: root}, nil, probeControl{})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := socket.Start(ctx, config.Config{Root: root}, nil, probeControl{})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	primary, err := socket.Path(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path() != primary || second.Path() == primary {
		t.Fatalf("socket paths: first=%s second=%s primary=%s", first.Path(), second.Path(), primary)
	}
	check := func(want map[string]bool) {
		t.Helper()
		instances, err := Scan(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, instance := range instances {
			if instance.Root == root {
				if !want[instance.SocketPath] {
					t.Fatalf("unexpected instance: %+v", instance)
				}
				delete(want, instance.SocketPath)
			}
		}
		if len(want) != 0 {
			t.Fatalf("missing instances: %v", want)
		}
	}
	check(map[string]bool{first.Path(): true, second.Path(): true})
	var out bytes.Buffer
	if err := run(context.Background(), filepath.Dir(primary), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "  socket: "+first.Path()+"\n") || !strings.Contains(out.String(), "  socket: "+second.Path()+"\n") {
		t.Fatalf("probe did not identify both sockets:\n%s", out.String())
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{second.Path(): true})
	// A new instance can reclaim the primary path without affecting the secondary.
	third, err := socket.Start(ctx, config.Config{Root: root}, nil, probeControl{})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if third.Path() != primary {
		t.Fatalf("new primary = %s, want %s", third.Path(), primary)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	check(map[string]bool{third.Path(): true})
}

func TestProbeMissingDirectoryAndBadArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(context.Background(), filepath.Join(t.TempDir(), "missing"), &out); err != nil || out.String() != "No running Gust instances found.\n" {
		t.Fatalf("empty probe: %q, %v", out.String(), err)
	}
	if code := Run(context.Background(), []string{"extra"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "Usage: gust ctl probe") {
		t.Fatalf("unexpected usage: code=%d stderr=%q", code, errOut.String())
	}
}
