// Package ctl implements the `gust ctl` control client.
package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/dector/gust/internal/protocol"
	"github.com/dector/gust/internal/socket"
)

const dialTimeout = 5 * time.Second

var actions = map[string]protocol.Action{
	"status": protocol.ActionStatus,
	"pause":  protocol.ActionPause,
	"resume": protocol.ActionResume,
	"rerun":  protocol.ActionRerun,
	"logs":   protocol.ActionLogs,
}

// Run executes a `gust ctl` invocation and returns a process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	socketPath, verb, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "gust ctl: %v\n\n", err)
		printHelp(stderr)
		return 2
	}
	if verb == "" || verb == "help" {
		printHelp(stdout)
		return 0
	}
	action, ok := actions[verb]
	if !ok {
		fmt.Fprintf(stderr, "gust ctl: unknown command %q\n\n", verb)
		printHelp(stderr)
		return 2
	}
	if socketPath == "" {
		socketPath, err = socket.Path("")
		if err != nil {
			fmt.Fprintf(stderr, "gust ctl: %v\n", err)
			return 1
		}
	}
	resp, err := call(ctx, socketPath, action)
	if err != nil {
		fmt.Fprintf(stderr, "gust ctl: %v\n", err)
		return 1
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "gust ctl: %s\n", resp.Error)
		return 1
	}
	printResponse(stdout, verb, resp)
	return 0
}

// parseArgs extracts an optional socket path and the command verb. The -S flag
// may appear before or after the verb.
func parseArgs(args []string) (socketPath, verb string, err error) {
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-S" || arg == "--socket":
			if i+1 >= len(args) || args[i+1] == "" {
				return "", "", fmt.Errorf("%s requires a path", arg)
			}
			socketPath = args[i+1]
			i++
		case strings.HasPrefix(arg, "-S=") || strings.HasPrefix(arg, "--socket="):
			socketPath = arg[strings.Index(arg, "=")+1:]
			if socketPath == "" {
				return "", "", fmt.Errorf("%s requires a path", arg)
			}
		case arg == "-h" || arg == "--help":
			positional = append(positional, "help")
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) > 1 {
		return "", "", fmt.Errorf("unexpected argument: %s", positional[1])
	}
	if len(positional) == 1 {
		verb = positional[0]
	}
	return socketPath, verb, nil
}

func call(ctx context.Context, path string, action protocol.Action) (protocol.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("cannot reach gust at %s: %w", path, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(protocol.Request{Action: action}); err != nil {
		return protocol.Response{}, err
	}
	var resp protocol.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return protocol.Response{}, err
	}
	return resp, nil
}

func printResponse(w io.Writer, verb string, resp protocol.Response) {
	switch verb {
	case "status":
		fmt.Fprintf(w, "state: %s\n", resp.State)
		if resp.PID != 0 {
			fmt.Fprintf(w, "pid: %d\n", resp.PID)
		}
		fmt.Fprintf(w, "auto_reload: %s\n", resp.AutoReload)
		if resp.Version != 0 {
			fmt.Fprintf(w, "version: %d\n", resp.Version)
		}
		if resp.AppPort != 0 {
			fmt.Fprintf(w, "app_port: %d\n", resp.AppPort)
		}
		if resp.ProxyPort != 0 {
			fmt.Fprintf(w, "proxy_port: %d\n", resp.ProxyPort)
		}
		if resp.LastExit != nil {
			fmt.Fprintf(w, "last_exit: code=%d at=%s error=%t\n", resp.LastExit.Code, resp.LastExit.At, resp.LastExit.Error)
		}
	case "pause", "resume":
		fmt.Fprintf(w, "auto_reload: %s\n", resp.AutoReload)
	case "rerun":
		fmt.Fprintf(w, "status: %s\n", resp.Status)
	case "logs":
		fmt.Fprintf(w, "last_exit: code=%d at=%s\n", resp.Code, resp.At)
		if resp.Phase != "" {
			fmt.Fprintf(w, "phase: %s\n", resp.Phase)
		}
		if resp.Command != "" {
			fmt.Fprintf(w, "command: %s\n", resp.Command)
		}
		fmt.Fprintf(w, "--- stdout ---\n%s", resp.Stdout)
		if resp.Stdout != "" && !strings.HasSuffix(resp.Stdout, "\n") {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "--- stderr ---\n%s", resp.Stderr)
		if resp.Stderr != "" && !strings.HasSuffix(resp.Stderr, "\n") {
			fmt.Fprintln(w)
		}
	}
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `gust ctl - control a running Gust instance

Usage:
  gust ctl [-S <socket>] <command>

Commands:
  status    show instance state and last exit
  pause     pause filesystem auto-reload
  resume    resume filesystem auto-reload
  rerun     reload now (works while paused)
  logs      show output captured from the last failed exit
  help      show this help

Discovery:
  Without -S, the socket path is derived from the current directory.
  -S <path>  use an explicit socket path.

Exit codes:
  0  success
  1  could not reach or control the instance
  2  usage error
`)
}
