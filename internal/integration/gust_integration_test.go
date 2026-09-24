//go:build linux

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dector/gust/internal/socket"
)

var gustBin string
var repoRoot string

func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	repoRoot = filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	tmp, err := os.MkdirTemp("", "gust-integration-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)
	gustBin = filepath.Join(tmp, "gust")
	cmd := exec.Command("go", "build", "-o", gustBin, "./cmd/gust")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type gustProc struct {
	cmd *exec.Cmd
	out *lockedBuffer
}

func startGust(t *testing.T, root string, args ...string) *gustProc {
	t.Helper()
	cmd := exec.Command(gustBin, args...)
	cmd.Dir = root
	buf := &lockedBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start gust: %v", err)
	}
	gp := &gustProc{cmd: cmd, out: buf}
	t.Cleanup(func() { gp.stop(t) })
	return gp
}

func (g *gustProc) stop(t *testing.T) {
	t.Helper()
	if g.cmd.ProcessState != nil {
		return
	}
	_ = g.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- g.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = g.cmd.Process.Kill()
		<-done
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", msg)
}

func writeScript(t *testing.T, root, name, body string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return "./" + name
}

func readCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "run")
}

func socketRequest(t *testing.T, root string, req map[string]string) map[string]any {
	t.Helper()
	path, err := socket.Path(root)
	if err != nil {
		t.Fatal(err)
	}
	var conn net.Conn
	waitFor(t, 5*time.Second, func() bool {
		conn, err = net.Dial("unix", path)
		return err == nil
	}, "socket to accept connections")
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

func TestGenericRerunnerWithoutPort(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs.txt")
	script := writeScript(t, root, "run.sh", "echo run >> runs.txt\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
	gp := startGust(t, root, "-e", script)
	waitFor(t, 5*time.Second, func() bool { return readCount(runs) == 1 }, "initial generic run")
	var resp map[string]any
	waitFor(t, 5*time.Second, func() bool {
		resp = socketRequest(t, root, map[string]string{"action": "status"})
		return resp["state"] == "running"
	}, "running generic status")
	if resp["ok"] != true || int(resp["pid"].(float64)) == 0 {
		t.Fatalf("unexpected status: %#v; logs:\n%s", resp, gp.out.String())
	}
}

func TestAppPortWithoutProxy(t *testing.T) {
	root := t.TempDir()
	port := freePort(t)
	server := writeHTTPServer(t, root, 0, "ok")
	gp := startGust(t, root, "-e", "go run "+server, "-p", strconv.Itoa(port), "-h", "/health")
	waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/health", port), "ok", 10*time.Second)
	var resp map[string]any
	waitFor(t, 5*time.Second, func() bool {
		resp = socketRequest(t, root, map[string]string{"action": "status"})
		return resp["state"] == "running"
	}, "running app status")
	if resp["ok"] != true || int(resp["app_port"].(float64)) != port {
		t.Fatalf("unexpected status: %#v; logs:\n%s", resp, gp.out.String())
	}
}

func TestProxyModeInjectsAndForwards(t *testing.T) {
	root := t.TempDir()
	appPort := freePort(t)
	proxyPort := freePort(t)
	server := writeHTTPServer(t, root, 0, "<html>hello</html>")
	gp := startGust(t, root, "-e", "go run "+server, "-p", fmt.Sprintf("%d:%d", appPort, proxyPort), "-h", "/health")
	body := waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/", proxyPort), "__gust_reload", 10*time.Second)
	if !strings.Contains(body, "hello") {
		t.Fatalf("proxy body did not include app response: %q; logs:\n%s", body, gp.out.String())
	}
}

func TestFilesystemTriggeredRerun(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs.txt")
	script := writeScript(t, root, "run.sh", "echo run >> runs.txt\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
	gp := startGust(t, root, "-e", script)
	waitFor(t, 5*time.Second, func() bool { return readCount(runs) == 1 }, "initial run")
	if err := os.WriteFile(filepath.Join(root, "watched.txt"), []byte("change"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, func() bool { return readCount(runs) >= 2 }, "filesystem rerun")
	if strings.Contains(gp.out.String(), "restart failed") {
		t.Fatalf("unexpected restart failure:\n%s", gp.out.String())
	}
}

func TestSocketTriggeredRerun(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs.txt")
	script := writeScript(t, root, "run.sh", "echo run >> runs.txt\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
	gp := startGust(t, root, "-e", script)
	waitFor(t, 5*time.Second, func() bool { return readCount(runs) == 1 }, "initial run")
	resp := socketRequest(t, root, map[string]string{"action": "rerun"})
	if resp["ok"] != true || resp["status"] != "queued" {
		t.Fatalf("unexpected rerun response: %#v", resp)
	}
	waitFor(t, 8*time.Second, func() bool { return readCount(runs) >= 2 }, "socket rerun")
	if strings.Contains(gp.out.String(), "restart failed") {
		t.Fatalf("unexpected restart failure:\n%s", gp.out.String())
	}
}

func TestChildExitWaitsForNextTrigger(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs.txt")
	script := writeScript(t, root, "run.sh", "echo run >> runs.txt\n")
	startGust(t, root, "-e", script)
	waitFor(t, 5*time.Second, func() bool { return readCount(runs) == 1 }, "first self-exit")
	waitFor(t, 5*time.Second, func() bool {
		resp := socketRequest(t, root, map[string]string{"action": "status"})
		return resp["state"] == "stopped"
	}, "stopped status after child exit")
	resp := socketRequest(t, root, map[string]string{"action": "rerun"})
	if resp["ok"] != true {
		t.Fatalf("unexpected rerun response: %#v", resp)
	}
	waitFor(t, 5*time.Second, func() bool { return readCount(runs) == 2 }, "rerun after self-exit")
}

func TestHealthFailureErrors(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		root := t.TempDir()
		port := freePort(t)
		script := writeScript(t, root, "run.sh", "trap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
		gp := startGust(t, root, "-e", script, "-p", strconv.Itoa(port), "-h", "/health")
		waitFor(t, 13*time.Second, func() bool { return strings.Contains(gp.out.String(), "health check timed out") }, "health timeout log")
	})
	t.Run("app exits before health", func(t *testing.T) {
		root := t.TempDir()
		appPort := freePort(t)
		proxyPort := freePort(t)
		script := writeScript(t, root, "run.sh", "echo run\n")
		startGust(t, root, "-e", script, "-p", fmt.Sprintf("%d:%d", appPort, proxyPort), "-h", "/health")
		waitFor(t, 5*time.Second, func() bool {
			resp := socketRequest(t, root, map[string]string{"action": "status"})
			return resp["state"] == "stopped"
		}, "stopped status after app exits before health")
		msg := readBrowserMessage(t, fmt.Sprintf("ws://127.0.0.1:%d/__gust/ws", proxyPort))
		if msg["type"] != "error" {
			t.Fatalf("unexpected browser message: %#v", msg)
		}
	})
}

func TestProxyUnavailableToReadyRetries(t *testing.T) {
	root := t.TempDir()
	appPort := freePort(t)
	proxyPort := freePort(t)
	server := writeHTTPServer(t, root, 1200*time.Millisecond, "ready")
	gp := startGust(t, root, "-e", "go run "+server, "-p", fmt.Sprintf("%d:%d", appPort, proxyPort))
	waitFor(t, 5*time.Second, func() bool { return strings.Contains(gp.out.String(), "proxy: http://") }, "proxy startup log")
	body := waitHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/", proxyPort), "ready", 10*time.Second)
	if strings.Contains(body, "__gust_reload") {
		// Non-HTML body must not be injected.
		t.Fatalf("unexpected injection for plain response: %q", body)
	}
}

func TestCommentsOptIn(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
		code int
	}{
		{name: "disabled", want: "comments_disabled", code: 1},
		{name: "enabled", args: []string{"--optin", "comments"}, want: "[]", code: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			run := writeScript(t, root, "run.sh", "trap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
			args := append([]string{"-e", run}, tt.args...)
			gp := startGust(t, root, args...)
			waitFor(t, 5*time.Second, func() bool {
				return strings.Contains(gp.out.String(), "socket:")
			}, "gust socket startup")
			out, code := ctlCommand(t, root, "comments", "--pending")
			if code != tt.code || !strings.Contains(out, tt.want) {
				t.Fatalf("comments: code=%d out=%q; logs:\n%s", code, out, gp.out.String())
			}
		})
	}
}

func TestCtlPauseAndResume(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	runs := filepath.Join(stateDir, "runs.txt")
	watched := filepath.Join(root, "watched.txt")
	script := writeScript(t, root, "run.sh", "echo run >> "+runs+"\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
	gp := startGust(t, root, "-e", script)
	waitFor(t, 5*time.Second, func() bool { return readCount(runs) == 1 }, "initial run")

	out, code := ctlCommand(t, root, "pause")
	if code != 0 || !strings.Contains(out, "auto_reload: paused") {
		t.Fatalf("pause: code=%d out=%q; logs:\n%s", code, out, gp.out.String())
	}

	if err := os.WriteFile(watched, []byte("change"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if got := readCount(runs); got != 1 {
		t.Fatalf("runs while paused = %d, want 1", got)
	}

	out, code = ctlCommand(t, root, "status")
	if code != 0 || !strings.Contains(out, "auto_reload: paused") {
		t.Fatalf("status: code=%d out=%q", code, out)
	}

	out, code = ctlCommand(t, root, "rerun")
	if code != 0 || !strings.Contains(out, "status: queued") {
		t.Fatalf("rerun: code=%d out=%q", code, out)
	}
	waitFor(t, 8*time.Second, func() bool { return readCount(runs) == 2 }, "manual rerun while paused")

	out, code = ctlCommand(t, root, "resume")
	if code != 0 || !strings.Contains(out, "auto_reload: active") {
		t.Fatalf("resume: code=%d out=%q", code, out)
	}

	if err := os.WriteFile(watched, []byte("change2"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, func() bool { return readCount(runs) >= 3 }, "filesystem rerun after resume")
}

func TestBeforeFailureKeepsRunningApp(t *testing.T) {
	root := t.TempDir()
	runs := filepath.Join(root, "runs.txt")
	beforeRuns := filepath.Join(root, "before.txt")
	before := writeScript(t, root, "before.sh", "echo before-run >> before.txt\nif [ -f fail ]; then exit 1; fi\n")
	run := writeScript(t, root, "run.sh", "echo run >> runs.txt\ntrap 'exit 0' TERM\nwhile :; do sleep 1; done\n")
	gp := startGust(t, root, "-e", run, "--e.before", before)
	waitFor(t, 5*time.Second, func() bool { return readCount(runs) == 1 }, "initial run")
	waitFor(t, 5*time.Second, func() bool { return readCount(beforeRuns) == 1 }, "initial before run")
	waitFor(t, 5*time.Second, func() bool {
		return socketRequest(t, root, map[string]string{"action": "status"})["state"] == "running"
	}, "running after initial start")

	statusBefore := socketRequest(t, root, map[string]string{"action": "status"})
	pidBefore := int(statusBefore["pid"].(float64))

	// Trigger a rerun whose before step fails.
	if err := os.WriteFile(filepath.Join(root, "fail"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, func() bool { return readCount(beforeRuns) >= 2 }, "before rerun")
	time.Sleep(700 * time.Millisecond)

	if got := readCount(runs); got != 1 {
		t.Fatalf("app restarted despite before failure: runs=%d; logs:\n%s", got, gp.out.String())
	}
	status := socketRequest(t, root, map[string]string{"action": "status"})
	if status["state"] != "running" || int(status["pid"].(float64)) != pidBefore {
		t.Fatalf("status = %#v, want running pid %d; logs:\n%s", status, pidBefore, gp.out.String())
	}
	logsResp := socketRequest(t, root, map[string]string{"action": "logs"})
	if logsResp["phase"] != "before" || logsResp["command"] != before {
		t.Fatalf("logs = %#v, want phase before command %s; logs:\n%s", logsResp, before, gp.out.String())
	}
}

func ctlCommand(t *testing.T, root string, args ...string) (string, int) {
	t.Helper()
	path, err := socket.Path(root)
	if err != nil {
		t.Fatal(err)
	}
	full := append([]string{"ctl", "-S", path}, args...)
	cmd := exec.Command(gustBin, full...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("run gust ctl %v: %v; output:\n%s", args, err, out)
	return "", -1
}

func readBrowserMessage(t *testing.T, url string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func waitHTTP(t *testing.T, url, want string, timeout time.Duration) string {
	t.Helper()
	client := &http.Client{Timeout: timeout + time.Second}
	var last string
	waitFor(t, timeout, func() bool {
		resp, err := client.Get(url)
		if err != nil {
			last = err.Error()
			return false
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		last = string(data)
		return resp.StatusCode == http.StatusOK && strings.Contains(last, want)
	}, "HTTP "+url+" containing "+want+"; last="+last)
	return last
}

func writeHTTPServer(t *testing.T, root string, delay time.Duration, body string) string {
	t.Helper()
	src := fmt.Sprintf(`package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	port := os.Getenv("GUST_APP_PORT")
	if port == "" {
		panic("missing GUST_APP_PORT")
	}
	delay, _ := time.ParseDuration(%q)
	time.Sleep(delay)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if %q != "" && %q[0] == '<' {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		} else {
			w.Header().Set("Content-Type", "text/plain")
		}
		fmt.Fprint(w, %q)
	})
	if err := http.ListenAndServe("127.0.0.1:"+port, nil); err != nil {
		panic(err)
	}
}
`, delay.String(), body, body, body)
	name := "server.go"
	if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

var _ = context.Background
