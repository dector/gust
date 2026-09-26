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

	"github.com/dector/gust/internal/protocol"
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

func TestProbeMissingDirectoryAndBadArguments(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := run(context.Background(), filepath.Join(t.TempDir(), "missing"), &out); err != nil || out.String() != "No running Gust instances found.\n" {
		t.Fatalf("empty probe: %q, %v", out.String(), err)
	}
	if code := Run(context.Background(), []string{"extra"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "Usage: gust probe") {
		t.Fatalf("unexpected usage: code=%d stderr=%q", code, errOut.String())
	}
}
