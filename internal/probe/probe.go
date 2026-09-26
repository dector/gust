// Package probe discovers live Gust instances through their control sockets.
package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/dector/gust/internal/protocol"
	"github.com/dector/gust/internal/socket"
)

const probeTimeout = 500 * time.Millisecond

// Run scans the per-user socket directory and prints responsive instances.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "Usage: gust ctl probe")
		return 2
	}
	if err := run(ctx, socket.Dir(), stdout); err != nil {
		fmt.Fprintf(stderr, "gust ctl probe: %v\n", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, dir string, out io.Writer) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "No running Gust instances found.")
		return nil
	}
	if err != nil {
		return err
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSocket != 0 && filepath.Ext(entry.Name()) == ".sock" {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}

	// Bound both the wait for unresponsive sockets and the number of open connections.
	sem := make(chan struct{}, 8)
	results := make(chan protocol.Response, len(paths))
	var wg sync.WaitGroup
	for _, path := range paths {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			resp, err := status(ctx, path)
			if err != nil || !resp.OK {
				return
			}
			if resp.Root == "" { // Older Gust instances do not report their root.
				resp.Root = path
			}
			results <- resp
		}()
	}
	wg.Wait()
	close(results)
	if err := ctx.Err(); err != nil {
		return err
	}

	var instances []protocol.Response
	for resp := range results {
		instances = append(instances, resp)
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Root < instances[j].Root })
	if len(instances) == 0 {
		fmt.Fprintln(out, "No running Gust instances found.")
		return nil
	}
	for i, resp := range instances {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s (%s)\n", resp.Root, resp.State)
		port := resp.ProxyPort
		if port == 0 {
			port = resp.AppPort
		}
		if port != 0 {
			fmt.Fprintf(out, "  local: http://127.0.0.1:%d/\n", port)
		} else {
			fmt.Fprintln(out, "  local: unknown (no -p)")
		}
		if resp.TailscaleURL != "" {
			fmt.Fprintf(out, "  tailscale: %s\n", resp.TailscaleURL)
		}
	}
	return nil
}

func status(ctx context.Context, path string) (protocol.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return protocol.Response{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(protocol.Request{Action: protocol.ActionStatus}); err != nil {
		return protocol.Response{}, err
	}
	var resp protocol.Response
	err = json.NewDecoder(conn).Decode(&resp)
	return resp, err
}
