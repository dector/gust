package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/dector/gust/internal/comments"
	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
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

func TestProxyServesStatus(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()

	started := time.Now().Add(-time.Second).UTC()
	server.SetStatusProvider(func(context.Context) (coordinator.Status, error) {
		uptime := int64(1000)
		return coordinator.Status{Process: coordinator.ProcessStatus{
			Running: true, StartedAt: &started, UptimeMS: &uptime,
			LastExit: &coordinator.LastExit{Code: 1, At: started, PassedMS: 1000, Error: true,
				Logs: &coordinator.ProcessLogs{Stdout: "out", Stderr: "err"}},
		}}, nil
	})

	resp, err := http.Get(proxyURL + "/__gust/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var status coordinator.ProcessStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Running || status.UptimeMS == nil || status.LastExit == nil || status.LastExit.PassedMS != 1000 || status.LastExit.Logs.Stderr != "err" {
		t.Fatalf("status = %+v", status)
	}
}

func TestProxyServesInfo(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()

	server.SetStatusProvider(func(context.Context) (coordinator.Status, error) {
		return coordinator.Status{
			State:            coordinator.ExternalRunning,
			PID:              4242,
			Version:          7,
			AutoReloadPaused: true,
			LastTrigger:      coordinator.TriggerFS,
			LastReadyIn:      420 * time.Millisecond,
			BrowserNotice:    "before failed: templ generate (exit 1)",
		}, nil
	})

	resp, err := http.Get(proxyURL + "/__gust/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("response = status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var info browserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	want := browserInfo{Status: "running", PID: 4242, Version: 7, AutoReload: "paused", Trigger: "filesystem", ReadyMS: 420, Notice: "before failed: templ generate (exit 1)"}
	if info != want {
		t.Fatalf("info = %+v, want %+v", info, want)
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
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "debug" || msg.Enabled == nil || *msg.Enabled {
		t.Fatalf("connect debug message = %+v, want disabled", msg)
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

func TestProxyBrowserWebSocketTogglesDebug(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "debug" || msg.Enabled == nil || *msg.Enabled {
		t.Fatalf("connect message = %+v, want debug disabled", msg)
	}

	if enabled := closeProxy.BrowserHub().ToggleDebug(); !enabled {
		t.Fatal("ToggleDebug() = false, want true")
	}
	msg = readBrowserMessage(t, ctx, conn)
	if msg.Type != "debug" || msg.Enabled == nil || !*msg.Enabled {
		t.Fatalf("toggle message = %+v, want debug enabled", msg)
	}

	conn2, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "done")
	msg = readBrowserMessage(t, ctx, conn2)
	if msg.Type != "debug" || msg.Enabled == nil || !*msg.Enabled {
		t.Fatalf("new client message = %+v, want debug enabled", msg)
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

func TestProxyBrowserWebSocketSendsLatestNoticeOnConnect(t *testing.T) {
	proxyURL, closeProxy := startProxyForTest(t, freePort(t))
	defer closeProxy.Close()

	closeProxy.BrowserHub().BrowserNotice("before failed: templ generate (exit 1)")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(proxyURL, "http://", "ws://", 1)+"/__gust/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	msg := readBrowserMessage(t, ctx, conn)
	if msg.Type != "notice" || msg.Message != "before failed: templ generate (exit 1)" {
		t.Fatalf("connect message = %+v, want latest notice", msg)
	}
}

func TestProxyInjectedScriptSyntaxWhenNodeAvailable(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	for _, script := range []string{reloadScript(12, 8765), reloadScript(12, 8765, true, true)} {
		start := strings.Index(script, ">") + 1
		end := strings.LastIndex(script, "</script>")
		if start <= 0 || end < start {
			t.Fatal("could not extract injected JavaScript")
		}
		file := t.TempDir() + "/reload.js"
		if err := os.WriteFile(file, []byte(script[start:end]), 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(node, "--check", file).CombinedOutput(); err != nil {
			t.Fatalf("generated JavaScript syntax: %v\n%s", err, output)
		}
	}
}

func TestProxySelfDevScript(t *testing.T) {
	defaultScript := reloadScript(1, 8080, true)
	selfScript := reloadScript(1, 8080, true, true)
	if !strings.Contains(defaultScript, "const selfDev = false;") || !strings.Contains(selfScript, "const selfDev = true;") {
		t.Fatal("self-dev must be opt-in")
	}
	for _, expected := range []string{
		`function isSelfDevPanel(el)`,
		`isSelfDevPanel(e.target)&&!e.altKey`,
		`if(!selecting||blockedCommentTarget(e.target)||(isSelfDevPanel(e.target)&&!e.altKey))return;`,
		`if(selfDev&&reconnectAfterDisconnect){reconnectAfterDisconnect=false;location.reload();}`,
	} {
		if !strings.Contains(selfScript, expected) {
			t.Errorf("self-dev script missing %q", expected)
		}
	}
}

func TestProxyInjectedScriptContent(t *testing.T) {
	script := reloadScript(12, 8765)
	checks := []string{
		`<script id="__gust_reload">`,
		`let lastVersion = 12;`,
		`/__gust/ws`,
		`new WebSocket(url)`,
		`location.reload()`,
		`setTimeout(connect, retry)`,
		`__gust_error`,
		`position:fixed;top:0`,
		`z-index:2147483646`,
		`z-index:2147483647`,
		`__gust_icon`,
		`position:fixed;right:8px;top:8px`,
		`__gust_icon_online`,
		`__gust_icon_offline`,
		`__gust_icon_failing`,
		`let connectionAttempted = false;`,
		`gustIcon.classList.toggle("__gust_icon_offline", !connected && connectionAttempted)`,
		`connected = false; connectionAttempted = true; disconnected = true; applyIconState()`,
		`#__gust_icon{position:relative;width:28px;height:28px;color:#f9bb71;opacity:1}`,
		`viewBox="0 0 256 256"`,
		`#__gust_icon::after{content:"";position:absolute;right:-2px;bottom:-2px;width:8px;height:8px;border:2px solid #f9bb71;border-radius:50%;background:#24180f;`,
		`#__gust_icon.__gust_icon_online::after{background:#22c55e;border-color:#24180f}`,
		`#__gust_icon.__gust_icon_offline::after{background:#dc2626;border-color:#24180f}`,
		`#__gust_icon.__gust_icon_failing::after{background:#f59e0b;border-color:#24180f}`,
		`#__gust_panel{top:36px;color-scheme:dark;background:linear-gradient(135deg,#352416,#24180f);color:#fff7e9;`,
		`#__gust_comments textarea,[data-editor] textarea{background:#352416;color:#fff7e9;`,
		`#__gust_comment_editor{color-scheme:dark;background:#24180f;color:#fff7e9;`,
		`opacity:.45`,
		`#22c55e`,
		`#dc2626`,
		`#f59e0b`,
		`__gust_notice`,
		`__gust_group`,
		`function taskGroup(`,
		`__gust_wide`,
		`__gust_log`,
		`data-name=`,
		`function logSection(`,
		`<pre><code>`,
		`background:transparent`,
		`/__gust/status`,
		`gustProcess`,
		`msg.type === "notice"`,
		`gustInfo.notice`,
		`function esc(`,
		`cursor:pointer`,
		`__gust_pinned`,
		`localStorage.getItem("__gust_pinned")`,
		`localStorage.setItem("__gust_pinned"`,
		`const gustAppPort = 8765;`,
		`__gust_panel`,
		`/__gust/info`,
		`__gust_widget`,
		`#__gust_widget.__gust_open #__gust_panel`,
		`__gust_dot`,
		`font:14px/1.6`,
		`Proxying`,
		`Reloaded`,
		`Status`,
		`Version`,
		`Auto-reload`,
		`Trigger`,
		`Ready In`,
		`pinned`,
		`127.0.0.1:`,
		`__gust_debug_style`,
		`gust-debug`,
		`outline-offset: -1px`,
		`outline-offset:2px`,
		`add.setAttribute("aria-label","Add comment")`,
		`commentToolbar) gustPanel.prepend(commentToolbar)`,
		`#__gust_comment_toolbar{display:flex;`,
		`M2.992 16.342`,
		`textContent="Comment Mode"`,
		`toggle.setAttribute("aria-label",active?"Exit comment mode":"Add comment")`,
		`document.addEventListener("mouseout",function(e){if(selecting&&!e.relatedTarget)`,
		`selectedElement=hoverPath[selectedIndex]`,
		`const el=selectedElement; const text=textarea.value.trim();`,
		`function updateEditorPosition()`,
		`editorAnchor={x:e.clientX,y:e.clientY}`,
		`if(left+width>innerWidth-margin)left=editorAnchor.x-width-gap`,
		`if(top+height>innerHeight-margin)top=editorAnchor.y-height-gap`,
		`position:fixed;display:none;z-index:2147483647`,
		`font:13px/1.5 ui-monospace`,
		`max-height:calc(100vh - 24px)`,
		`if(blockedCommentTarget(e.target)||(isSelfDevPanel(e.target)&&!e.altKey)){hoverPath=[];setHighlight(null);updateCommentUI();return;}`,
		`window.addEventListener("scroll",function(){if(editorOpen)updateEditorPosition();},true)`,
		`function closeCommentMode()`,
		`function meaningfulPath(`,
		`for (let n = el; n && path.length < 12; n = n.parentElement)`,
		`if(el===document.body||el===document.documentElement)selector=tag`,
		`return {path:path, guessedIndex:Math.max(0,path.indexOf(guess))}`,
		`function updateHoverPath(target){`,
		`const commentIconSvg='<svg`,
		`add.innerHTML=commentIconSvg;`,
		`pin.innerHTML=commentIconSvg;`,
		`function elementSnippet(`,
		`function fullAncestry(`,
		`function renderPath(`,
		`function scheduleRenderPath(`,
		`target.matches("html,head,body")?"":elementSnippet(target)`,
		`ancestors.forEach(function(name){addPart(name,"__gust_path_ancestor");})`,
		`#__gust_selected_path .__gust_path_trail{display:flex;flex-wrap:wrap`,
		`max-width:calc(100vw - 24px)`,
		`crumbs.replaceChildren(trail)`,
		`text-shadow:0 1px 3px #000`,
		`crumbs.id="__gust_selected_path"`,
		`document.documentElement.appendChild(crumbs)`,
		`document.getElementById("__gust_selected_path")`,
		`#__gust_selected_path{position:fixed;bottom:16px`,
		`#__gust_selected_path[hidden]{display:none}`,
		`#__gust_selected_path .__gust_path_current{`,
		`document.documentElement.classList.toggle("__gust_selecting",selecting)`,
		`const commentCursor="url('data:image/svg+xml,"+encodeURIComponent(commentIconSvg.replace("currentColor","#f59e0b"))`,
		`html.__gust_selecting,html.__gust_selecting *{cursor:"+commentCursor+"!important}`,
		`html.__gust_selecting [data-gust-overlay]`,
		`function currentTargetEl()`,
		`function resumeSelection(){`,
		`refreshComments();resumeSelection();`,
		`const box=document.querySelector("[data-editor] textarea");if(box)box.focus();`,
		"renderCommentState();\n  updateCommentUI();",
		`document.addEventListener("click"`,
		`e.preventDefault();e.stopPropagation();e.stopImmediatePropagation();`,
		`chooseSelection(e);`,
		`selectedPoint=r.width>0&&r.height>0?`,
		`locator:locatorFor(el,selectedPoint)`,
		`const x=p&&Number.isFinite(p.x)`,
		`const y=p&&Number.isFinite(p.y)`,
		`new ResizeObserver(schedulePinReposition)`,
		`if(pinSizeObserver)pinSizeObserver.observe(el)`,
		`window.addEventListener("scroll",schedulePinReposition,true)`,
		`window.addEventListener("resize",schedulePinReposition)`,
		`requestAnimationFrame(function(){pinPositionFrame=0;repositionPins();})`,
		`function repositionPins(){`,
		`const el=c&&c.path===location.pathname&&matchingElement(c);`,
		`replace(/\s+/g," ")`,
		`function locatorFor(`,
		`function safeOuterHTML(`,
		`function beginSelection(`,

		`fetch("/__gust/comments"`,
		`method:"POST"`,
		`location.pathname`,
		`JSON.stringify({selector:selector,tag:tag,text:text,confidence:`,
		`__gust_pin`,
		`commentUI.append(autoLabel,status,listHeader,submitResult,pollError,list)`,
		`listHeader.append(listTitle,count,submit)`,
		`empty.textContent="No open comments yet."`,
		`__gust_comment_text{display:-webkit-box`,
		`__gust_comments_header{display:flex`,
		`auto.checked=true`,
		`e.key==="Enter"&&e.ctrlKey`,
		`sessionStorage.getItem("__gust_comment_mode")`,
		`sessionStorage.setItem("__gust_comment_mode", (selecting||editorOpen) ? "1" : "0")`,
		`selecting=loadCommentMode(); createCommentUI()`,
		`setHighlight(null);saveCommentMode();updateCommentUI();`,
		`/__gust/comments/submit?id=`,
		`active.slice().reverse().forEach(function(c)`,
		`summary.textContent=finished+" finished"`,
		`remove.setAttribute("aria-label","Remove draft")`,
		`method:"DELETE"`,
		`close.addEventListener("click",function(){textarea.value="";result.textContent="";resumeSelection();})`,
		`seen:"In progress"`,
		`if(c.state==="seen"){const spinner=document.createElement("span")`,
		`spinner.setAttribute("aria-hidden","true")`,
		`animation:__gust_comment_spin .9s linear infinite`,
		`prefers-reduced-motion:reduce`,
		`editor.append(close,title,textarea,save,hint,result)`,
		`function beginSelection(){if(selecting||editorOpen){closeCommentMode();return;}`,
		`replace(/\s+/g," ")`,
		`Comment sync failed: `,
		`open.title=onPage?`,
		`submit.textContent="Submit "+created`,
		`data-comment-id`,
		`pointer-events:auto`,
		`fetch("/__gust/comments/submit"`,
		`function refreshComments()`,
		`setInterval(refreshComments,3000)`,
		`l.confidence!=="high"||l.matches!==1`,
		`replace(/\s+/g," ");if(!actual.includes(l.text))return null;`,
		`c.state==="created"||c.state==="submitted"||c.state==="seen"`,
		`submitResult.dataset.submitResult`,
		`missing.textContent="Not found"`,
	}
	for _, check := range checks {
		if !strings.Contains(script, check) {
			t.Fatalf("script missing %q in %s", check, script)
		}
	}
	for _, removed := range []string{"Shrink", "Expand", "Choose this element", "Cancel", "Change selection", "Selected element"} {
		if strings.Contains(script, removed) {
			t.Errorf("script still contains removed comment-mode UI %q", removed)
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
	server, err := Start(ctx, config.Config{AppPort: appPort, ProxyPort: proxyPort, ProxyEnabled: true, CommentsEnabled: true}, nil)
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

func TestBrowserCommentsDisabledByDefaultWhenConfigured(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	server.SetCommentsEnabled(false)

	for _, path := range []string{"/__gust/comments", "/__gust/comments/submit"} {
		req, err := http.NewRequest(http.MethodGet, proxyURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status=%d, want 404", path, resp.StatusCode)
		}
	}
	injected := reloadScript(1, 8080, false)
	if !strings.Contains(injected, "if (false) { selecting=loadCommentMode(); createCommentUI(); startCommentRefresh(); }") {
		t.Fatal("disabled reload widget includes comment UI")
	}
}

func TestBrowserCommentsAPI(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)

	post := func(path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, proxyURL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", proxyURL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post("/__gust/comments", `{"path":"/one","text":"hello","html":"<div onclick=\"evil()\"><script>alert(1)</script><input value=\"secret\"><img src=\"data:image/png;base64,AAAA\"></div>","locator":"#target"}`)
	var created comments.Comment
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("create status=%d: %s", resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.State != comments.StateCreated || strings.Contains(created.HTML, "script") || strings.Contains(created.HTML, "onclick") || strings.Contains(created.HTML, "secret") || strings.Contains(created.HTML, "base64") {
		t.Fatalf("unsafe or unexpected created comment: %+v", created)
	}

	resp, err = http.Get(proxyURL + "/__gust/comments?path=%2Fone")
	if err != nil {
		t.Fatal(err)
	}
	var listed []comments.Comment
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list = %+v", listed)
	}

	resp = post("/__gust/comments/submit", "{}")
	var batch comments.Batch
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit status=%d: %s", resp.StatusCode, b)
	}
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(batch.Comments) != 1 || batch.Comments[0].State != comments.StateSubmitted {
		t.Fatalf("batch = %+v", batch)
	}
	claimed, err := store.NextBatch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Comments[0].State != comments.StateSeen {
		t.Fatalf("NextBatch state = %s", claimed.Comments[0].State)
	}
	current, err := store.Get(context.Background(), created.ID)
	if err != nil || current.State != comments.StateSeen {
		t.Fatalf("stored state=%s err=%v", current.State, err)
	}
}

func TestBrowserAutosubmitOnlyTargetsSavedComment(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	other, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "draft", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "auto", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, proxyURL+"/__gust/comments/submit?id="+current.ID, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", proxyURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var batch comments.Batch
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || len(batch.Comments) != 1 || batch.Comments[0].ID != current.ID {
		t.Fatalf("autosubmit: status %d, batch %+v", resp.StatusCode, batch)
	}
	draft, err := store.Get(context.Background(), other.ID)
	if err != nil || draft.State != comments.StateCreated {
		t.Fatalf("unrelated draft: %+v, %v", draft, err)
	}
}

func TestBrowserDeleteDraft(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	draft, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "draft", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := store.Create(context.Background(), comments.Input{Path: "/", Text: "sent", Locator: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitOne(context.Background(), submitted.ID); err != nil {
		t.Fatal(err)
	}
	remove := func(id, origin string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodDelete, proxyURL+"/__gust/comments/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := remove(draft.ID, "http://attacker.invalid"); got != 403 {
		t.Fatalf("cross-origin delete: %d", got)
	}
	if got := remove(submitted.ID, proxyURL); got != 409 {
		t.Fatalf("submitted delete: %d", got)
	}
	if got := remove(draft.ID, proxyURL); got != 204 {
		t.Fatalf("draft delete: %d", got)
	}
	if got := remove(draft.ID, proxyURL); got != 404 {
		t.Fatalf("repeated delete: %d", got)
	}
}

func TestBrowserCommentsAPIRejectsInvalidRequests(t *testing.T) {
	app := httptest.NewServer(http.NotFoundHandler())
	defer app.Close()
	proxyURL, server := startProxyForTest(t, appPort(t, app.URL))
	defer server.Close()
	store, err := comments.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server.SetCommentStore(store)
	cases := []struct {
		name, path, body string
		origin           string
		want             int
	}{
		{"invalid json", "/__gust/comments", `{`, "", 400},
		{"invalid path", "/__gust/comments", `{"path":"https://bad","text":"x","locator":"x"}`, "", 400},
		{"empty text", "/__gust/comments", `{"path":"/","text":" ","locator":"x"}`, "", 400},
		{"text limit", "/__gust/comments", `{"path":"/","text":"` + strings.Repeat("x", 8193) + `","locator":"x"}`, "", 400},
		{"html limit", "/__gust/comments", `{"path":"/","text":"x","html":"` + strings.Repeat("x", 32769) + `","locator":"x"}`, "", 400},
		{"locator limit", "/__gust/comments", `{"path":"/","text":"x","locator":"` + strings.Repeat("x", 8193) + `"}`, "", 400},
		{"unknown field", "/__gust/comments", `{"path":"/","text":"x","locator":"x","state":"done"}`, "", 400},
		{"origin mismatch", "/__gust/comments", `{"path":"/","text":"x","locator":"x"}`, "http://attacker.invalid", 403},
		{"no created", "/__gust/comments/submit", `{}`, "", 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, proxyURL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				b, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d body=%s", resp.StatusCode, b)
			}
			var payload map[string]map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["error"]["code"] == "" {
				t.Fatalf("missing error code: %+v", payload)
			}
		})
	}
	resp, err := postWithHeaders(proxyURL+"/__gust/comments", strings.Repeat(" ", maxCommentBody+1)+`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 413 {
		t.Fatalf("oversized body status=%d", resp.StatusCode)
	}

	resp, err = http.Get(proxyURL + "/__gust/comments/extra")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unknown reserved path status=%d", resp.StatusCode)
	}
}

func postWithHeaders(target, body string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return http.DefaultClient.Do(req)
}

func TestProxyDisabledHasNoServer(t *testing.T) {
	server, err := Start(context.Background(), config.Config{ProxyEnabled: false}, nil)
	if err != nil || server != nil {
		t.Fatalf("Start disabled = (%v, %v)", server, err)
	}
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
