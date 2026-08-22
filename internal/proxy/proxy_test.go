package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dector/gust/internal/config"
)

func TestProxyForwardsRequestsAndBodies(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/echo" || r.URL.RawQuery != "x=1" {
			t.Fatalf("unexpected app URL: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-App", "ok")
		fmt.Fprintf(w, "%s:%s", r.Method, body)
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Post(proxyURL+"/echo?x=1", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("X-App") != "ok" || string(body) != "POST:hello" {
		t.Fatalf("unexpected response header=%q body=%q", resp.Header.Get("X-App"), body)
	}
}

func TestProxyStreamsNonHTMLResponses(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0, 1, 2, 3})
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/data")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got, want := string(body), string([]byte{0, 1, 2, 3}); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestProxyReservesGustPaths(t *testing.T) {
	appHit := false
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/__gust/anything")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
	if appHit {
		t.Fatal("reserved Gust path was forwarded to app")
	}
}

func TestProxyRetriesUntilAppAvailable(t *testing.T) {
	appPort := freePort(t)
	proxyPort := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := Start(ctx, config.Config{AppPort: appPort, ProxyPort: proxyPort, ProxyEnabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	go func() {
		time.Sleep(250 * time.Millisecond)
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", appPort))
		if err != nil {
			return
		}
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("ready"))
		}))
	}()

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", proxyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ready" {
		t.Fatalf("body = %q, want ready", body)
	}
}

func TestProxyBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	server, err := Start(context.Background(), config.Config{AppPort: freePort(t), ProxyPort: port, ProxyEnabled: true}, nil)
	if err == nil {
		server.Close()
		t.Fatal("expected bind failure")
	}
}

func TestProxyInjectsHTMLAndUpdatesHeaders(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "" {
			t.Fatalf("Accept-Encoding forwarded as %q", got)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", "12")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		_, _ = w.Write([]byte("<h1>hi</h1>!"))
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	req, err := http.NewRequest(http.MethodGet, proxyURL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `<script id="__gust_reload">`) {
		t.Fatalf("missing injected script in %q", body)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got, want := resp.Header.Get("Content-Length"), fmt.Sprint(len(body)); got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}
	if got := resp.Header.Get("ETag"); got != "" {
		t.Fatalf("ETag = %q, want removed", got)
	}
	if got := resp.Header.Get("Last-Modified"); got != "Wed, 21 Oct 2015 07:28:00 GMT" {
		t.Fatalf("Last-Modified = %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestProxyInjectsHTMLErrorPagesAndLeavesAbsentLengthAbsent(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusInternalServerError)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("oops"))
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "__gust_reload") {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want absent", got)
	}
}

func TestProxySkipsHTMLInjectionRules(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		reqHeader  map[string]string
		status     int
		respHeader map[string]string
		body       string
	}{
		{name: "xhtml", respHeader: map[string]string{"Content-Type": "application/xhtml+xml"}},
		{name: "head", method: http.MethodHead, respHeader: map[string]string{"Content-Type": "text/html"}},
		{name: "range request", reqHeader: map[string]string{"Range": "bytes=0-4"}, respHeader: map[string]string{"Content-Type": "text/html"}},
		{name: "partial response", status: http.StatusPartialContent, respHeader: map[string]string{"Content-Type": "text/html"}},
		{name: "attachment", respHeader: map[string]string{"Content-Type": "text/html", "Content-Disposition": "attachment; filename=x.html"}},
		{name: "encoded", respHeader: map[string]string{"Content-Type": "text/html", "Content-Encoding": "gzip"}},
		{name: "already injected", respHeader: map[string]string{"Content-Type": "text/html"}, body: `<script id="__gust_reload"></script>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tt.respHeader {
					w.Header().Set(k, v)
				}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				body := tt.body
				if body == "" {
					body = "<html></html>"
				}
				_, _ = w.Write([]byte(body))
			}))
			defer app.Close()

			proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
			defer closeProxy.Close()

			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req, err := http.NewRequest(method, proxyURL+"/", nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tt.reqHeader {
				req.Header.Set(k, v)
			}
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if strings.Count(string(body), "__gust_reload") > strings.Count(tt.body, "__gust_reload") {
				t.Fatalf("unexpected injection body=%q", body)
			}
			if got := resp.Header.Get("Cache-Control"); got == "no-store" && tt.name != "already injected" {
				t.Fatalf("Cache-Control set on skipped response")
			}
		})
	}
}

func TestProxyInjectsIdentityEncodedHTML(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Encoding", "identity")
		_, _ = w.Write([]byte("ok"))
	}))
	defer app.Close()

	proxyURL, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()

	resp, err := http.Get(proxyURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "__gust_reload") {
		t.Fatalf("body = %q, want injection", body)
	}
}

func TestProxyBrowserWebSocketSendsLatestAndReloads(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	closeProxy.BrowserHub().BrowserReady(3)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "ready" || msg.Version != 3 {
		t.Fatalf("connect message = %+v, want ready v3", msg)
	}

	closeProxy.BrowserHub().BrowserReady(4)
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "reload" || msg.Version != 4 {
		t.Fatalf("broadcast message = %+v, want reload v4", msg)
	}

	closeProxy.BrowserHub().BrowserError("health check timed out")
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "error" || msg.Message != "health check timed out" {
		t.Fatalf("error message = %+v", msg)
	}
}

func TestProxyBrowserWebSocketSendsLatestErrorOnConnect(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	closeProxy.BrowserHub().BrowserError("process exited")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "error" || msg.Message != "process exited" {
		t.Fatalf("connect message = %+v, want latest error", msg)
	}
}

func TestProxyInjectedScriptContent(t *testing.T) {
	script := reloadScript(12)
	checks := []string{
		`<script id="__gust_reload">`,
		`let lastVersion = 12;`,
		`/__gust/ws`,
		`new WebSocket(url)`,
		`location.reload()`,
		`setTimeout(connect, retry)`,
		`__gust_error`,
		`position:fixed;top:0`,
	}
	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Fatalf("script missing %q in %s", check, script)
		}
	}
}

func TestProxyForwardsWebSocketUpgrades(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Fatalf("missing websocket upgrade header")
		}
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer cannot hijack")
		}
		conn, bufrw, err := h.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = bufrw.Flush()
	}))
	defer app.Close()

	_, closeProxy := startProxyForTest(t, appPort(t, app.URL))
	defer closeProxy.Close()
	proxyAddr := strings.TrimPrefix(strings.TrimSuffix(closeProxy.URL, "/"), "http://")

	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: example\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")
	buf := make([]byte, 128)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "101 Switching Protocols") {
		t.Fatalf("upgrade response = %q", buf[:n])
	}
}

func readBrowserMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) browserMessage {
	t.Helper()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var msg browserMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

type proxyCloser struct {
	URL    string
	cancel context.CancelFunc
	*Server
}

func startProxyForTest(t *testing.T, appPort int) (string, *proxyCloser) {
	t.Helper()
	proxyPort := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	server, err := Start(ctx, config.Config{AppPort: appPort, ProxyPort: proxyPort, ProxyEnabled: true}, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	closer := &proxyCloser{URL: fmt.Sprintf("http://127.0.0.1:%d", proxyPort), cancel: cancel, Server: server}
	return closer.URL, closer
}

func (c *proxyCloser) Close() {
	c.cancel()
	_ = c.Server.Close()
}

func appPort(t *testing.T, rawURL string) int {
	t.Helper()
	u := strings.TrimPrefix(rawURL, "http://127.0.0.1:")
	var port int
	if _, err := fmt.Sscanf(u, "%d", &port); err != nil {
		t.Fatalf("parse port from %q: %v", rawURL, err)
	}
	return port
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
