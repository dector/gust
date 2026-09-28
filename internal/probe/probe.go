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

// Instance describes a responsive Gust control socket.
type Instance struct {
	SocketPath   string
	Root         string
	State        string
	AppPort      int
	ProxyPort    int
	TailscaleURL string
}

// Scan discovers responsive Gust instances for the current user.
// Unreachable sockets and unsuccessful status responses are ignored.
func Scan(ctx context.Context) ([]Instance, error) {
	return scan(ctx, socket.Dir())
}

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
	instances, err := scan(ctx, dir)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		fmt.Fprintln(out, "No running Gust instances found.")
		return nil
	}
	for i, instance := range instances {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s (%s)\n", instance.Root, instance.State)
		if (i > 0 && instances[i-1].Root == instance.Root) || (i+1 < len(instances) && instances[i+1].Root == instance.Root) {
			fmt.Fprintf(out, "  socket: %s\n", instance.SocketPath)
		}
		port := instance.ProxyPort
		if port == 0 {
			port = instance.AppPort
		}
		if port != 0 {
			fmt.Fprintf(out, "  local: http://127.0.0.1:%d/\n", port)
		} else {
			fmt.Fprintln(out, "  local: unknown (no -p)")
		}
		if instance.TailscaleURL != "" {
			fmt.Fprintf(out, "  tailscale: %s\n", instance.TailscaleURL)
		}
	}
	return nil
}

func scan(ctx context.Context, dir string) ([]Instance, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSocket != 0 && filepath.Ext(entry.Name()) == ".sock" {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}

	// Bound both the wait for unresponsive sockets and the number of open connections.
	sem := make(chan struct{}, 8)
	results := make(chan Instance, len(paths))
	var wg sync.WaitGroup
	for _, path := range paths {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
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
			results <- Instance{SocketPath: path, Root: resp.Root, State: resp.State, AppPort: resp.AppPort, ProxyPort: resp.ProxyPort, TailscaleURL: resp.TailscaleURL}
		}()
	}
	wg.Wait()
	close(results)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var instances []Instance
	for instance := range results {
		instances = append(instances, instance)
	}
	sort.Slice(instances, func(i, j int) bool {
		if instances[i].Root == instances[j].Root {
			return instances[i].SocketPath < instances[j].SocketPath
		}
		return instances[i].Root < instances[j].Root
	})
	return instances, nil
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
