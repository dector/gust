package probe_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/dector/gust/pkg/probe"
)

func TestScan(t *testing.T) {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("gust-%d", os.Getuid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fmt.Sprintf("probe-test-%d.sock", os.Getpid()))
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				var request struct {
					Action string `json:"action"`
				}
				if json.NewDecoder(conn).Decode(&request) == nil && request.Action == "status" {
					_ = json.NewEncoder(conn).Encode(map[string]any{
						"ok": true, "root": "/probe-test", "state": "running",
						"app_port": 3000, "proxy_port": 4000, "tailscale_url": "https://test.ts.net/",
					})
				}
			}()
		}
	}()

	instances, err := probe.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, instance := range instances {
		if instance.SocketPath != path {
			continue
		}
		if instance.Root != "/probe-test" || instance.State != "running" || instance.AppPort != 3000 || instance.ProxyPort != 4000 || instance.TailscaleURL != "https://test.ts.net/" {
			t.Fatalf("unexpected instance: %+v", instance)
		}
		return
	}
	t.Fatalf("socket %s not discovered: %+v", path, instances)
}

func TestScanCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probe.Scan(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan(cancelled): %v", err)
	}
}
