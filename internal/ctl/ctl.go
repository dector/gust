// Package ctl implements the `gust ctl` control client.
package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/dector/gust/internal/probe"
	"github.com/dector/gust/internal/protocol"
	"github.com/dector/gust/internal/socket"
)

const (
	dialTimeout = 5 * time.Second
	// waitReconnectWindow bounds how long a blocked --wait keeps retrying
	// after losing its connection. Time spent connected is not counted.
	waitReconnectBackoff = 250 * time.Millisecond
)

// waitReconnectWindow is a var so tests can shorten it.
var waitReconnectWindow = 30 * time.Second

var actions = map[string]protocol.Action{
	"status": protocol.ActionStatus,
	"pause":  protocol.ActionPause,
	"resume": protocol.ActionResume,
	"rerun":  protocol.ActionRerun,
	"logs":   protocol.ActionLogs,
}

// Run executes a `gust ctl` invocation and returns a process exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if code, handled := runComments(ctx, args, stdout, stderr); handled {
		return code
	}
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
	if verb == "probe" {
		if socketPath != "" {
			fmt.Fprintln(stderr, "gust ctl probe: -S is not supported; probe scans all local Gust sockets")
			return 2
		}
		return probe.Run(ctx, nil, stdout, stderr)
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
	resp, err := call(ctx, socketPath, protocol.Request{Action: action}, dialTimeout)
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

func call(ctx context.Context, path string, req protocol.Request, timeout time.Duration) (protocol.Response, error) {
	conn, err := dial(ctx, path)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("cannot reach gust at %s: %w", path, err)
	}
	defer conn.Close()
	return exchange(ctx, conn, req, timeout)
}

// callWait issues a blocking wait request and reconnects when Gust restarts.
// A dropped connection does not end the wait; it retries for up to
// waitReconnectWindow since the outage began. Time spent connected to Gust is
// not counted against that window, so waiting for a batch stays open-ended.
func callWait(ctx context.Context, path string, req protocol.Request) (protocol.Response, error) {
	var lastErr error
	var deadline time.Time
	for {
		if err := ctx.Err(); err != nil {
			return protocol.Response{}, err
		}
		conn, err := dial(ctx, path)
		if err == nil {
			// Reconnected: forget the previous outage and block on the batch.
			deadline = time.Time{}
			resp, exchangeErr := exchange(ctx, conn, req, 0)
			conn.Close()
			if exchangeErr == nil {
				return resp, nil
			}
			lastErr = exchangeErr
		} else {
			lastErr = err
		}
		if deadline.IsZero() {
			deadline = time.Now().Add(waitReconnectWindow)
		}
		if !time.Now().Before(deadline) {
			return protocol.Response{}, fmt.Errorf("cannot reach gust at %s: %w", path, lastErr)
		}
		select {
		case <-ctx.Done():
			return protocol.Response{}, ctx.Err()
		case <-time.After(waitReconnectBackoff):
		}
	}
}

func dial(ctx context.Context, path string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	var dialer net.Dialer
	return dialer.DialContext(dialCtx, "unix", path)
}

// exchange sends one request and reads its response. With timeout zero it has
// no deadline and unblocks as soon as ctx is done, closing the connection.
func exchange(ctx context.Context, conn net.Conn, req protocol.Request, timeout time.Duration) (protocol.Response, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if timeout == 0 {
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-done:
			}
		}()
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
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

var commentStateNames = []string{"created", "submitted", "seen", "review", "done"}

// parseCommentFilter turns "all" or a comma-separated state list into the
// concrete states sent to Gust, preserving order and dropping duplicates.
func parseCommentFilter(value string) ([]string, error) {
	var states []string
	added := map[string]bool{}
	add := func(state string) {
		if !added[state] {
			added[state] = true
			states = append(states, state)
		}
	}
	for _, part := range strings.Split(value, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		if token == "all" {
			for _, state := range commentStateNames {
				add(state)
			}
			continue
		}
		if !validCommentState(token) {
			return nil, fmt.Errorf("unknown state %q (valid: all, %s)", token, strings.Join(commentStateNames, ", "))
		}
		add(token)
	}
	if len(states) == 0 {
		return nil, fmt.Errorf("--filter needs at least one state")
	}
	return states, nil
}

func validCommentState(state string) bool {
	for _, name := range commentStateNames {
		if name == state {
			return true
		}
	}
	return false
}

func runComments(ctx context.Context, args []string, stdout, stderr io.Writer) (int, bool) {
	var positional []string
	socketPath := ""
	human := false
	filterValue := ""
	since := int64(0)
	sinceSet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--human":
			human = true
		case a == "--filter":
			if i+1 >= len(args) || args[i+1] == "" {
				fmt.Fprintln(stderr, "gust ctl: --filter requires a value")
				return 2, true
			}
			filterValue = args[i+1]
			i++
		case strings.HasPrefix(a, "--filter="):
			filterValue = strings.TrimPrefix(a, "--filter=")
			if filterValue == "" {
				fmt.Fprintln(stderr, "gust ctl: --filter requires a value")
				return 2, true
			}
		case a == "--since":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "gust ctl: --since requires a value")
				return 2, true
			}
			v, err := strconv.ParseInt(args[i+1], 10, 64)
			if err != nil || v < 0 {
				fmt.Fprintln(stderr, "gust ctl: --since requires a non-negative integer")
				return 2, true
			}
			since = v
			sinceSet = true
			i++
		case strings.HasPrefix(a, "--since="):
			v, err := strconv.ParseInt(strings.TrimPrefix(a, "--since="), 10, 64)
			if err != nil || v < 0 {
				fmt.Fprintln(stderr, "gust ctl: --since requires a non-negative integer")
				return 2, true
			}
			since = v
			sinceSet = true
		case a == "-S" || a == "--socket":
			if i+1 >= len(args) || args[i+1] == "" {
				fmt.Fprintf(stderr, "gust ctl: %s requires a path\n", a)
				return 2, true
			}
			socketPath = args[i+1]
			i++
		case strings.HasPrefix(a, "-S=") || strings.HasPrefix(a, "--socket="):
			socketPath = a[strings.Index(a, "=")+1:]
			if socketPath == "" {
				fmt.Fprintf(stderr, "gust ctl: %s requires a path\n", a)
				return 2, true
			}
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 || positional[0] != "comments" {
		return 0, false
	}
	usage := func() {
		fmt.Fprintln(stderr, "Usage: gust ctl [-S <socket>] comments [--filter <states>|--pending|--wait [--one]|watch [--since <n>]|seen <id>|reply <id> <text> [--human]|review <id> <text>|done <id>]")
	}
	var req protocol.Request
	if len(positional) == 1 {
		req.Action = protocol.ActionCommentsList
	} else if len(positional) == 2 && positional[1] == "--pending" {
		req.Action = protocol.ActionCommentsPending
	} else if len(positional) == 2 && positional[1] == "--wait" {
		req.Action = protocol.ActionCommentsWait
	} else if len(positional) == 2 && positional[1] == "watch" {
		req.Action = protocol.ActionCommentsWatch
		req.Since = since
	} else if len(positional) == 3 && ((positional[1] == "--wait" && positional[2] == "--one") || (positional[1] == "--one" && positional[2] == "--wait")) {
		req.Action = protocol.ActionCommentsWait
		req.One = true
	} else if len(positional) == 3 && positional[1] == "seen" && positional[2] != "" {
		req.Action, req.ID = protocol.ActionCommentsSeen, positional[2]
	} else if len(positional) == 3 && positional[1] == "done" && positional[2] != "" {
		req.Action, req.ID = protocol.ActionCommentsDone, positional[2]
	} else if len(positional) == 4 && positional[1] == "reply" && positional[2] != "" && strings.TrimSpace(positional[3]) != "" {
		req.Action, req.ID, req.Text = protocol.ActionCommentsReply, positional[2], positional[3]
	} else if len(positional) == 4 && positional[1] == "review" && positional[2] != "" && strings.TrimSpace(positional[3]) != "" {
		req.Action, req.ID, req.Text = protocol.ActionCommentsReview, positional[2], positional[3]
	} else {
		fmt.Fprintln(stderr, "gust ctl: invalid comments command")
		usage()
		return 2, true
	}
	if human && req.Action != protocol.ActionCommentsReply {
		fmt.Fprintln(stderr, "gust ctl: --human is only valid with comments reply")
		usage()
		return 2, true
	}
	if filterValue != "" && req.Action != protocol.ActionCommentsList {
		fmt.Fprintln(stderr, "gust ctl: --filter is only valid with comments")
		usage()
		return 2, true
	}
	if sinceSet && req.Action != protocol.ActionCommentsWatch {
		fmt.Fprintln(stderr, "gust ctl: --since is only valid with comments watch")
		usage()
		return 2, true
	}
	if filterValue != "" {
		states, err := parseCommentFilter(filterValue)
		if err != nil {
			fmt.Fprintf(stderr, "gust ctl: %v\n", err)
			usage()
			return 2, true
		}
		req.Filter = states
	}
	req.Human = human
	if socketPath == "" {
		var err error
		socketPath, err = socket.Path("")
		if err != nil {
			fmt.Fprintf(stderr, "gust ctl: %v\n", err)
			return 1, true
		}
	}
	var resp protocol.Response
	var err error
	if req.Action == protocol.ActionCommentsWait || req.Action == protocol.ActionCommentsWatch {
		resp, err = callWait(ctx, socketPath, req)
	} else {
		resp, err = call(ctx, socketPath, req, dialTimeout)
	}
	if err != nil {
		fmt.Fprintf(stderr, "gust ctl: %v\n", err)
		return 1, true
	}
	if !resp.OK {
		fmt.Fprintf(stderr, "gust ctl: %s\n", resp.Error)
		return 1, true
	}
	var value any
	switch req.Action {
	case protocol.ActionCommentsWait:
		value = resp.Batch
	case protocol.ActionCommentsPending, protocol.ActionCommentsList:
		value = resp.Comments
	case protocol.ActionCommentsWatch:
		value = map[string]any{"cursor": resp.Cursor, "comments": resp.Comments}
	default:
		value = resp.Comment
	}
	enc := json.NewEncoder(stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		fmt.Fprintf(stderr, "gust ctl: %v\n", err)
		return 1, true
	}
	return 0, true
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `gust ctl - control a running Gust instance

Usage:
  gust ctl [-S <socket>] <command>

Commands:
  status    show instance state and last exit
  probe     list running instances and their URLs (no -S)
  pause     pause filesystem auto-reload
  resume    resume filesystem auto-reload
  rerun     reload now (works while paused)
  logs      show output captured from the last failed exit
  comments  wait for, recover, reply to, or review submitted comments
  help      show this help

Comments:
  comments                    list all unfinished comments as JSON
  comments --filter <states>  list only these states: all or a comma-separated
                              list of created, submitted, seen, review, done
  comments --pending          list seen unfinished comments as JSON
  comments --wait             wait for oldest batch; marks comments seen
  comments --wait --one       claim one comment from oldest submitted batch
  comments watch [--since <n>]
                              block until a thread changes, then print the
                              full snapshot and cursor as JSON (no claim)
  comments seen <id>          mark a submitted thread seen (claim it)
  comments reply <id> <text>  post an agent reply on a seen thread
  comments reply <id> <text> --human
                              post a human reply; a human reply to a review
                              thread reopens it as submitted for the agent
  comments review <id> <text> post an agent reply and mark the thread review
  comments done <id>          resolve a submitted, seen, or review thread

Discovery:
  Without -S, the socket path is derived from the current directory.
  -S <path>  use an explicit socket path.

Exit codes:
  0  success
  1  could not reach or control the instance
  2  usage error
`)
}
