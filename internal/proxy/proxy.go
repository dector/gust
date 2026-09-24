package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/dector/gust/internal/comments"
	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/coordinator"
	"github.com/dector/gust/internal/logger"
)

// Server is Gust's development HTTP proxy.
type Server struct {
	server    *http.Server
	listener  net.Listener
	log       *logger.Logger
	hub       *BrowserHub
	appPort   int
	proxyPort int
	closeOnce sync.Once

	statusMu       sync.RWMutex
	statusProvider func(context.Context) (coordinator.Status, error)
	comments       *comments.Store
}

// BrowserHub tracks browser websocket clients and their latest status.
type BrowserHub struct {
	mu      sync.Mutex
	clients map[*browserClient]struct{}
	latest  browserMessage
	debug   bool
}

type browserClient struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

type browserMessage struct {
	Type    string `json:"type"`
	Version int    `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
	At      int64  `json:"at,omitempty"`
}

// browserInfo is the JSON payload served by /__gust/info.
type browserInfo struct {
	Status     string `json:"status"`
	PID        int    `json:"pid"`
	Version    int    `json:"version"`
	AutoReload string `json:"autoReload"`
	Trigger    string `json:"trigger"`
	ReadyMS    int64  `json:"readyMs"`
	Notice     string `json:"notice,omitempty"`
}

// BrowserReady records a ready browser state and broadcasts reload to clients.
func (h *BrowserHub) BrowserReady(version int) {
	if h == nil {
		return
	}
	at := time.Now().UnixMilli()
	h.broadcast(browserMessage{Type: "reload", Version: version, At: at}, browserMessage{Type: "ready", Version: version, At: at})
}

// ToggleDebug toggles browser debug outlines, broadcasts the new state, and returns it.
func (h *BrowserHub) ToggleDebug() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	h.debug = !h.debug
	enabled := h.debug
	clients := make([]*browserClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	data, _ := json.Marshal(browserMessage{Type: "debug", Enabled: &enabled})
	for _, c := range clients {
		if err := c.write(data); err != nil {
			h.remove(c)
			_ = c.conn.Close(websocket.StatusGoingAway, "write failed")
		}
	}
	return enabled
}

// BrowserError records and broadcasts a browser error banner message.
func (h *BrowserHub) BrowserError(message string) {
	if h == nil || message == "" {
		return
	}
	msg := browserMessage{Type: "error", Message: message}
	h.broadcast(msg, msg)
}

// BrowserNotice records and broadcasts a low-severity message shown in the
// floating panel only, without the error banner.
func (h *BrowserHub) BrowserNotice(message string) {
	if h == nil || message == "" {
		return
	}
	msg := browserMessage{Type: "notice", Message: message}
	h.broadcast(msg, msg)
}

func (h *BrowserHub) currentVersion() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.latest.Version
}

func (h *BrowserHub) broadcast(send browserMessage, latest browserMessage) {
	h.mu.Lock()
	h.latest = latest
	clients := make([]*browserClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()

	data, _ := json.Marshal(send)
	for _, c := range clients {
		if err := c.write(data); err != nil {
			h.remove(c)
			_ = c.conn.Close(websocket.StatusGoingAway, "write failed")
		}
	}
}

func (h *BrowserHub) add(conn *websocket.Conn) (*browserClient, browserMessage, bool) {
	client := &browserClient{conn: conn}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client] = struct{}{}
	return client, h.latest, h.debug
}

func (h *BrowserHub) remove(c *browserClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *BrowserHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	clients := make([]*browserClient, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.clients = map[*browserClient]struct{}{}
	h.mu.Unlock()
	for _, c := range clients {
		_ = c.conn.Close(websocket.StatusGoingAway, "proxy closed")
	}
}

func (c *browserClient) write(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// Start binds and starts the proxy when proxy mode is enabled.
func Start(ctx context.Context, cfg config.Config, log *logger.Logger) (*Server, error) {
	if !cfg.ProxyEnabled {
		return nil, nil
	}

	addr := fmt.Sprintf("127.0.0.1:%d", cfg.ProxyPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("start proxy on %s: %w", addr, err)
	}

	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", cfg.AppPort))
	if err != nil {
		_ = ln.Close()
		return nil, err
	}

	s := &Server{listener: ln, log: log, hub: &BrowserHub{clients: map[*browserClient]struct{}{}}, appPort: cfg.AppPort, proxyPort: cfg.ProxyPort}
	s.server = &http.Server{
		Addr:    addr,
		Handler: s.handler(target),
	}

	go func() {
		<-ctx.Done()
		_ = s.Close()
	}()
	go func() {
		if err := s.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if log != nil {
				log.Printf("proxy stopped: %v", err)
			}
		}
	}()

	return s, nil
}

// BrowserHub returns the browser websocket notification hub.
func (s *Server) BrowserHub() *BrowserHub {
	if s == nil {
		return nil
	}
	return s.hub
}

// SetStatusProvider configures the source for /__gust/status responses.
// SetCommentStore connects the shared in-memory comment store to browser endpoints.
func (s *Server) SetCommentStore(store *comments.Store) {
	if s != nil {
		s.comments = store
	}
}

func (s *Server) SetStatusProvider(provider func(context.Context) (coordinator.Status, error)) {
	if s == nil {
		return
	}
	s.statusMu.Lock()
	s.statusProvider = provider
	s.statusMu.Unlock()
}

// Close stops the proxy server.
func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		s.hub.close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err = s.server.Shutdown(ctx)
	})
	return err
}

func (s *Server) handler(target *url.URL) http.Handler {
	rp := httputil.NewSingleHostReverseProxy(target)
	director := rp.Director
	rp.Director = func(r *http.Request) {
		director(r)
		r.Header.Del("Accept-Encoding")
	}
	rp.Transport = retryTransport{
		base:     proxyTransport(),
		timeout:  config.ProxyRetryTimeout,
		interval: config.ProxyRetryInterval,
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		return injectHTMLResponse(resp, s.hub.currentVersion(), s.appPort, s.log)
	}
	rp.FlushInterval = -1
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if s.log != nil {
			s.log.Verbosef("proxy request failed: %v", err)
		}
		s.hub.BrowserError("proxy cannot reach app")
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__gust/ws" {
			s.serveWebSocket(w, r)
			return
		}
		if r.URL.Path == "/__gust/status" {
			s.serveStatus(w, r)
			return
		}
		if r.URL.Path == "/__gust/info" {
			s.serveInfo(w, r)
			return
		}
		if r.URL.Path == "/__gust/comments" {
			s.serveComments(w, r)
			return
		}
		if r.URL.Path == "/__gust/comments/submit" {
			s.serveCommentSubmit(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/__gust/") {
			http.NotFound(w, r)
			return
		}
		if err := prepareBodyForRetry(r); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		rp.ServeHTTP(&flushNoLengthWriter{ResponseWriter: w}, r)
	})
}

func (s *Server) serveStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.statusMu.RLock()
	provider := s.statusProvider
	s.statusMu.RUnlock()
	if provider == nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	status, err := provider(r.Context())
	if err != nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(status.Process)
}

func (s *Server) serveInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.statusMu.RLock()
	provider := s.statusProvider
	s.statusMu.RUnlock()
	if provider == nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	status, err := provider(r.Context())
	if err != nil {
		http.Error(w, "status unavailable", http.StatusServiceUnavailable)
		return
	}
	autoReload := "active"
	if status.AutoReloadPaused {
		autoReload = "paused"
	}
	info := browserInfo{
		Status:     string(status.State),
		PID:        status.PID,
		Version:    status.Version,
		AutoReload: autoReload,
		Trigger:    string(status.LastTrigger),
		ReadyMS:    status.LastReadyIn.Milliseconds(),
		Notice:     status.BrowserNotice,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

func (s *Server) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		if s.log != nil {
			s.log.Verbosef("browser websocket accept failed: %v", err)
		}
		return
	}
	client, latest, debug := s.hub.add(conn)
	defer func() {
		s.hub.remove(client)
		_ = conn.Close(websocket.StatusNormalClosure, "closed")
	}()
	if latest.Type != "" {
		data, _ := json.Marshal(latest)
		_ = client.write(data)
	}
	data, _ := json.Marshal(browserMessage{Type: "debug", Enabled: &debug})
	_ = client.write(data)
	for {
		_, _, err := conn.Read(r.Context())
		if err != nil {
			return
		}
	}
}

func proxyTransport() http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	clone := base.Clone()
	clone.DisableCompression = true
	return clone
}

func prepareBodyForRetry(r *http.Request) error {
	if r.Body == nil || r.Body == http.NoBody || r.GetBody != nil {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	r.ContentLength = int64(len(body))
	return nil
}

const flushNoLengthHeader = "X-Gust-Flush-No-Length"

func reloadScript(version, appPort int) string {
	return fmt.Sprintf(`<script id="__gust_reload">(function(){
let lastVersion = %d;
let retry = 250;
let socket;
function banner(){
  let el = document.getElementById("__gust_error");
  if (!el) {
    el = document.createElement("div");
    el.id = "__gust_error";
    el.style.cssText = "position:fixed;top:0;left:0;right:0;z-index:2147483646;background:#b00020;color:white;padding:8px 40px 8px 12px;font:14px sans-serif;text-align:left;white-space:pre-wrap";
    document.documentElement.appendChild(el);
  }
  return el;
}
function showError(message){ banner().textContent = message || "Gust error"; }
function hideError(){ const el = document.getElementById("__gust_error"); if (el) el.remove(); }
let connected = false;
let failing = false;
let gustIcon;
let gustWidget;
let gustPanel;
let pinned = false;
let hovering = false;
let reloadedAt = 0;
let gustInfo = null;
let gustProcess = null;
let infoTimer = null;
let commentUI = null;
let selecting = false;
let hoverPath = [];
let selectedIndex = 0;
let highlighted = null;
let editorOpen = false;
const gustAppPort = %d;
function ago(ms){
  if (!ms) return "unknown";
  const s = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (s < 60) return s + "s ago";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m ago";
  const h = Math.floor(m / 60);
  if (h < 24) return h + "h ago";
  return Math.floor(h / 24) + "d ago";
}
function applyIconState(){ if (!gustIcon) return; gustIcon.classList.toggle("__gust_icon_offline", !connected); gustIcon.classList.toggle("__gust_icon_failing", connected && failing); if (gustWidget) gustWidget.classList.toggle("__gust_wide", failing); }
function panelRow(label, value){ return '<div class="__gust_row"><span class="__gust_label">' + label + '</span><span>' + value + '</span></div>'; }
function logSection(name, text){ return '<details class="__gust_log" data-name="' + name + '"><summary>' + name + '</summary><pre><code>' + esc(text) + '</code></pre></details>'; }
function taskGroup(title, notice, exit){
  let html = '<div class="__gust_group"><div class="__gust_group_title">' + esc(title) + '</div>';
  html += '<div class="__gust_notice">' + esc(notice) + '</div>';
  const logs = exit && exit.logs;
  if (logs && logs.stdout) html += logSection("stdout", logs.stdout);
  if (logs && logs.stderr) html += logSection("stderr", logs.stderr);
  return html + '</div>';
}
function esc(s){ return String(s).replace(/[&<>"]/g, function(c){ return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c]; }); }
function statusColor(state){
  if (state === "running") return "#22c55e";
  if (state === "restarting") return "#f59e0b";
  if (state === "stopped") return "#ef4444";
  return "#6b7280";
}
function renderPanel(){
  if (!gustPanel) return;
  const savedCommentUI = commentUI && commentUI.parentNode === gustPanel ? commentUI : null;
  if (savedCommentUI) savedCommentUI.remove();
  const openLogs = {};
  gustPanel.querySelectorAll("details[data-name]").forEach(function(d){ openLogs[d.getAttribute("data-name")] = d.open; });
  let html = "";
  if (gustInfo) {
    const state = gustInfo.status || "unknown";
    html += panelRow("Status", '<span class="__gust_dot" style="background:' + statusColor(state) + '"></span>' + state);
    html += panelRow("Auto-reload", gustInfo.autoReload || "unknown");
  }
  html += panelRow("Reloaded", ago(reloadedAt));
  if (gustInfo) {
    html += panelRow("Trigger", gustInfo.trigger || "—");
    html += panelRow("Version", String(gustInfo.version || 0));
    html += panelRow("Ready In", gustInfo.readyMs ? gustInfo.readyMs + "ms" : "—");
  }
  html += panelRow("Proxying", "127.0.0.1:" + gustAppPort);
  if (gustInfo && gustInfo.notice) {
    html += taskGroup("Before task", gustInfo.notice, gustProcess && gustProcess.lastExit);
  }
  gustPanel.innerHTML = html;
  gustPanel.querySelectorAll("details[data-name]").forEach(function(d){ if (openLogs[d.getAttribute("data-name")]) d.open = true; });
  if (commentUI) gustPanel.appendChild(commentUI);
}
function refreshInfo(){
  fetch("/__gust/info", {cache: "no-store"}).then(function(r){ return r.ok ? r.json() : null; }).then(function(data){
    if (data) gustInfo = data;
    renderPanel();
  }).catch(function(){ renderPanel(); });
  fetch("/__gust/status", {cache: "no-store"}).then(function(r){ return r.ok ? r.json() : null; }).then(function(data){
    gustProcess = data;
    renderPanel();
  }).catch(function(){});
}
function startInfo(){
  if (infoTimer) return;
  refreshInfo();
  infoTimer = setInterval(refreshInfo, 1000);
}
function stopInfo(){
  if (!infoTimer) return;
  clearInterval(infoTimer);
  infoTimer = null;
}
function syncPanel(){
  if (!gustWidget) return;
  if (gustIcon) gustIcon.classList.toggle("__gust_pinned", pinned);
  if (pinned || hovering) {
    gustWidget.classList.add("__gust_open");
    startInfo();
    renderPanel();
    return;
  }
  gustWidget.classList.remove("__gust_open");
  stopInfo();
}
function isGustNode(el){ return !!(el && el.closest && el.closest("#__gust_widget,#__gust_error,[data-gust-overlay]")); }
function meaningfulPath(el){
  if (!el || el.nodeType !== 1 || isGustNode(el)) return {path:[], guessedIndex:0};
  let guess = el;
  const interactive = el.closest("button,a,input,textarea,select,[role=button],[role=link]");
  if (interactive && !isGustNode(interactive)) guess = interactive;
  else {
    let n = el;
    while (n.parentElement && n.parentElement !== document.body && n.parentElement !== document.documentElement) {
      const p = n.parentElement;
      if (p.children.length > 1 || (p.textContent || "").trim().length > 180) break;
      n = p;
    }
    guess = n;
  }
  const path = [];
  for (let n = el; n && n !== document.body && n !== document.documentElement && path.length < 12; n = n.parentElement) {
    if (isGustNode(n)) break;
    path.push(n);
  }
  return {path:path, guessedIndex:Math.max(0,path.indexOf(guess))};
}
function setHighlight(el){
  if (highlighted) highlighted.classList.remove("__gust_highlight");
  highlighted = el;
  if (highlighted) highlighted.classList.add("__gust_highlight");
}
function selectionLabel(el){
  let text = (el.innerText || el.getAttribute("aria-label") || el.getAttribute("alt") || "").trim().replace(/\s+/g," ");
  return el.tagName.toLowerCase() + (text ? " — " + text.slice(0,50) : "");
}
function updateCommentUI(){
  if (!commentUI) return;
  const status = commentUI.querySelector("[data-status]");
  const crumbs = commentUI.querySelector("[data-crumbs]");
  const controls = commentUI.querySelector("[data-controls]");
  status.textContent = selecting ? (hoverPath.length ? "Hover to preview, then choose the target." : "Hover over a page element.") : "";
  crumbs.replaceChildren();
  if (selecting && hoverPath.length) {
    hoverPath.slice().reverse().forEach(function(el, revIndex){
      const idx = hoverPath.length - 1 - revIndex;
      const b = document.createElement("button"); b.type="button"; b.textContent=selectionLabel(el);
      b.addEventListener("click", function(){ selectedIndex=idx; setHighlight(hoverPath[idx]); updateCommentUI(); });
      crumbs.appendChild(b);
    });
  }
  controls.hidden = !selecting;
  commentUI.querySelector("[data-editor]").hidden = !editorOpen;
}
function beginSelection(){ selecting=true; editorOpen=false; hoverPath=[]; setHighlight(null); pinned=true; savePinned(); syncPanel(); updateCommentUI(); }
function cancelSelection(){ selecting=false; editorOpen=false; hoverPath=[]; setHighlight(null); updateCommentUI(); }
function chooseSelection(){ if (!hoverPath.length) return; selectedIndex=Math.max(0,Math.min(selectedIndex,hoverPath.length-1)); selecting=false; editorOpen=true; updateCommentUI(); commentUI.querySelector("textarea").focus(); }
function cssEscape(v){ return window.CSS && CSS.escape ? CSS.escape(v) : String(v).replace(/[^a-zA-Z0-9_-]/g,"\\$&"); }
function locatorFor(el){
  const tag=el.tagName.toLowerCase();
  let selector="";
  if(el.id){ const candidate="#"+cssEscape(el.id); if(document.querySelectorAll(candidate).length===1) selector=candidate; }
  if(!selector){
    const attrs=["data-testid","name","aria-label"];
    for(const a of attrs){ const v=el.getAttribute(a); if(v){ const candidate=tag+"["+a+"=\""+v.replace(/\\/g,"\\\\").replace(/"/g,"\\\"")+"\"]"; try{if(document.querySelectorAll(candidate).length===1){selector=candidate;break;}}catch(_){} } }
  }
  if(!selector){ let n=el, bits=[]; while(n&&n.nodeType===1&&n!==document.body&&bits.length<5){let bit=n.tagName.toLowerCase();if(n.parentElement){const same=Array.from(n.parentElement.children).filter(x=>x.tagName===n.tagName);if(same.length>1)bit+=":nth-of-type("+(same.indexOf(n)+1)+")";}bits.unshift(bit);const s=bits.join(" > ");try{if(document.querySelectorAll(s).length===1){selector=s;break;}}catch(_){} n=n.parentElement;} }
  const text=(el.innerText||el.getAttribute("aria-label")||"").trim().replace(/\s+/g," ").slice(0,160);
  let count=0;try{count=selector?document.querySelectorAll(selector).length:0;}catch(_){}
  return JSON.stringify({selector:selector,tag:tag,text:text,confidence:selector&&count===1?"high":"low",matches:count});
}
function safeOuterHTML(el){
  if(el.matches("input[type=password],input[type=hidden]"))return "";
  const clone=el.cloneNode(true);
  if(clone.matches("textarea"))clone.textContent="";
  clone.querySelectorAll("script,style,input[type=password],input[type=hidden]").forEach(n=>n.remove());
  clone.querySelectorAll("textarea").forEach(n=>{n.textContent="";});
  [clone].concat(Array.from(clone.querySelectorAll("*"))).forEach(function(n){
    if(n.matches&&n.matches("input[type=password],input[type=hidden]"))return;
    Array.from(n.attributes||[]).forEach(function(a){if(/^on/i.test(a.name)||/^(value|srcdoc)$/i.test(a.name)||/token|secret|password|auth|session|csrf/i.test(a.name)||/^data:image/i.test(a.value))n.removeAttribute(a.name);});
  });
  return clone.outerHTML.slice(0,12000);
}
function createCommentUI(){
  if(commentUI)return;
  commentUI=document.createElement("div"); commentUI.id="__gust_comments";
  const add=document.createElement("button");add.type="button";add.textContent="Add comment";add.addEventListener("click",beginSelection);
  const status=document.createElement("div");status.dataset.status="";
  const crumbs=document.createElement("div");crumbs.dataset.crumbs="";
  const controls=document.createElement("div");controls.dataset.controls="";
  function action(label,fn){const b=document.createElement("button");b.type="button";b.textContent=label;b.addEventListener("click",fn);controls.appendChild(b);}
  action("Shrink",function(){if(selectedIndex>0)selectedIndex--;setHighlight(hoverPath[selectedIndex]);updateCommentUI();});
  action("Expand",function(){if(selectedIndex+1<hoverPath.length)selectedIndex++;setHighlight(hoverPath[selectedIndex]);updateCommentUI();});
  action("Choose this element",chooseSelection);action("Cancel",cancelSelection);
  const editor=document.createElement("div");editor.dataset.editor="";editor.hidden=true;
  const textarea=document.createElement("textarea");textarea.placeholder="Describe this element";textarea.maxLength=8192;
  const change=document.createElement("button");change.type="button";change.textContent="Change selection";change.addEventListener("click",function(){beginSelection();});
  const save=document.createElement("button");save.type="button";save.textContent="Save comment";
  const result=document.createElement("span");result.dataset.result="";
  save.addEventListener("click",function(){
    const el=hoverPath[selectedIndex]; const text=textarea.value.trim();
    if(!el||!text){result.textContent="Choose an element and enter a comment.";return;}
    save.disabled=true;result.textContent="Saving…";
    fetch("/__gust/comments",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({path:location.pathname, text:text, locator:locatorFor(el), html:safeOuterHTML(el)})})
      .then(function(r){if(!r.ok)throw new Error("Request failed ("+r.status+")");return r.json();})
      .then(function(){const pin=document.createElement("span");pin.className="__gust_pin";pin.textContent="●";pin.title="Created comment";el.appendChild(pin);textarea.value="";editorOpen=false;result.textContent="Comment created.";updateCommentUI();})
      .catch(function(e){result.textContent=e.message;}).finally(function(){save.disabled=false;});
  });
  editor.append(textarea,change,save,result);
  commentUI.append(add,status,crumbs,controls,editor);updateCommentUI();
}
function updateHoverPath(target){
  const result=meaningfulPath(target), path=result.path;
  if(!path.length)return false;
  const selected=hoverPath[selectedIndex];
  hoverPath=path;
  const preserved=selected?path.indexOf(selected):-1;
  selectedIndex=preserved>=0?preserved:result.guessedIndex;
  setHighlight(hoverPath[selectedIndex]);updateCommentUI();
  return true;
}
document.addEventListener("mousemove",function(e){
  if(!selecting||isGustNode(e.target))return;
  updateHoverPath(e.target);
},true);
document.addEventListener("click",function(e){
  if(!selecting||isGustNode(e.target))return;
  if(!updateHoverPath(e.target))return;
  e.preventDefault();e.stopPropagation();e.stopImmediatePropagation();
},true);
function loadPinned(){
  try { return localStorage.getItem("__gust_pinned") === "1"; } catch (_) { return false; }
}
function savePinned(){
  try { localStorage.setItem("__gust_pinned", pinned ? "1" : "0"); } catch (_) {}
}
function mountIcon(){
  if (document.getElementById("__gust_icon")) { gustIcon = document.getElementById("__gust_icon"); applyIconState(); return; }
  let style = document.getElementById("__gust_icon_style");
  if (!style) {
    style = document.createElement("style");
    style.id = "__gust_icon_style";
    style.textContent = "#__gust_widget{position:fixed;right:8px;top:8px;z-index:2147483647}#__gust_icon{width:24px;height:24px;color:#9ca3af;opacity:.45;transition:color .15s ease,opacity .15s ease;cursor:pointer}#__gust_icon:hover{color:#22c55e;opacity:1}#__gust_icon.__gust_pinned{color:#22c55e;opacity:1}#__gust_icon.__gust_icon_offline{color:#dc2626;opacity:1}#__gust_icon.__gust_icon_failing{color:#f59e0b;opacity:1}#__gust_panel{display:none;position:absolute;right:0;top:32px;background:#111827;color:#e5e7eb;font:14px/1.6 system-ui,sans-serif;padding:8px 10px;border-radius:6px;border:1px solid rgba(255,255,255,.12);box-shadow:0 6px 20px rgba(0,0,0,.45);white-space:nowrap}#__gust_widget.__gust_open #__gust_panel{display:block}#__gust_widget.__gust_wide #__gust_panel{width:32vw;min-width:280px;max-width:720px;white-space:normal}#__gust_panel .__gust_row{display:flex;justify-content:space-between;gap:16px}#__gust_panel .__gust_label{color:#9ca3af}#__gust_panel .__gust_group{margin-top:8px;padding:6px 8px;border:1px solid rgba(245,158,11,.4);border-radius:6px;background:rgba(245,158,11,.08);white-space:normal}#__gust_panel .__gust_group_title{color:#f59e0b;font-weight:600;margin-bottom:2px}#__gust_panel .__gust_notice{display:block;color:#f59e0b;margin-top:2px;white-space:normal;max-width:100%%}#__gust_panel .__gust_log{margin-top:4px;white-space:normal}#__gust_panel .__gust_log summary{cursor:pointer;color:#9ca3af}#__gust_panel .__gust_log pre{max-height:200px;max-width:100%%;overflow:auto;margin:4px 0 0;padding:6px 8px;background:#0b1220;border:1px solid rgba(255,255,255,.1);border-radius:4px;white-space:pre-wrap;word-break:break-word;font:12px/1.4 ui-monospace,SFMono-Regular,Menlo,monospace}#__gust_panel .__gust_log code{font:inherit;background:transparent;padding:0;border:0;color:inherit}#__gust_panel .__gust_dot{display:inline-block;width:8px;height:8px;border-radius:50%%;margin-right:6px;vertical-align:middle}#__gust_comments{margin-top:8px;border-top:1px solid #374151;padding-top:8px;white-space:normal}#__gust_comments button{font:inherit;cursor:pointer;margin:2px;padding:3px 7px}#__gust_comments textarea{box-sizing:border-box;width:100%%;min-height:70px;background:#0b1220;color:#e5e7eb;border:1px solid #4b5563;padding:6px}.__gust_highlight{outline:3px solid #f59e0b!important;outline-offset:2px!important}.__gust_pin{display:inline-block;margin-left:5px;border-radius:50%%;background:#f59e0b;color:#111827;font:bold 12px/20px sans-serif;text-align:center;width:20px;height:20px;vertical-align:middle}";
    document.head.appendChild(style);
  }
  const widget = document.createElement("div");
  widget.id = "__gust_widget";
  gustWidget = widget;
  gustIcon = document.createElement("div");
  gustIcon.id = "__gust_icon";
  gustIcon.title = "Gust";
  gustIcon.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15.914 4a1.5 1.5 0 00-2.474-1.561l-9 9A1.5 1.5 0 005.5 14h4.002a.5.5 0 01.471.666L8.086 20a1.5 1.5 0 002.475 1.56l9-9A1.5 1.5 0 0018.5 10h-3.997a.5.5 0 01-.472-.667z"/></svg>';
  gustPanel = document.createElement("div");
  gustPanel.id = "__gust_panel";
  createCommentUI();
  widget.appendChild(gustIcon);
  widget.appendChild(gustPanel);
  widget.addEventListener("mouseenter", function(){ hovering = true; syncPanel(); });
  widget.addEventListener("mouseleave", function(){ hovering = false; syncPanel(); });
  gustIcon.addEventListener("click", function(e){ e.stopPropagation(); pinned = !pinned; savePinned(); syncPanel(); });
  document.body.appendChild(widget);
  pinned = loadPinned();
  applyIconState();
  syncPanel();
}
if (document.body) mountIcon(); else document.addEventListener("DOMContentLoaded", mountIcon, {once:true});
function setDebug(enabled){
  const apply = function(){
    let style = document.getElementById("__gust_debug_style");
    if (!style) {
      style = document.createElement("style");
      style.id = "__gust_debug_style";
      style.textContent = ".gust-debug * { outline: 1px solid rgb(185 28 28 / 45%%); outline-offset: -1px; }.gust-debug *:nth-child(7n + 1) { outline-color: rgb(185 28 28 / 45%%); }.gust-debug *:nth-child(7n + 2) { outline-color: rgb(21 128 61 / 45%%); }.gust-debug *:nth-child(7n + 3) { outline-color: rgb(126 34 206 / 45%%); }.gust-debug *:nth-child(7n + 4) { outline-color: rgb(161 98 7 / 45%%); }.gust-debug *:nth-child(7n + 5) { outline-color: rgb(14 116 144 / 45%%); }.gust-debug *:nth-child(7n + 6) { outline-color: rgb(194 65 12 / 45%%); }.gust-debug *:nth-child(7n) { outline-color: rgb(29 78 216 / 45%%); }";
      document.head.appendChild(style);
    }
    document.body.classList.toggle("gust-debug", enabled);
  };
  if (document.body) apply(); else document.addEventListener("DOMContentLoaded", apply, {once:true});
}
function connect(){
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = proto + "//" + location.host + "/__gust/ws";
  socket = new WebSocket(url);
  socket.onopen = function(){ retry = 250; connected = true; applyIconState(); };
  socket.onmessage = function(event){
    let msg;
    try { msg = JSON.parse(event.data); } catch (_) { return; }
    if (msg.type === "debug") { setDebug(msg.enabled === true); return; }
    if (msg.type === "notice") { failing = true; applyIconState(); refreshInfo(); return; }
    if (msg.type === "error") { failing = true; applyIconState(); refreshInfo(); showError(msg.message); return; }
    if (msg.type === "ready") { failing = false; applyIconState(); hideError(); if (typeof msg.at === "number") reloadedAt = msg.at; if (typeof msg.version === "number" && msg.version > lastVersion) lastVersion = msg.version; return; }
    if (msg.type === "reload") {
      failing = false; applyIconState();
      hideError();
      if (typeof msg.at === "number") reloadedAt = msg.at;
      if (typeof msg.version === "number" && msg.version > lastVersion) {
        lastVersion = msg.version;
        location.reload();
      }
    }
  };
  socket.onclose = function(){ connected = false; applyIconState(); setTimeout(connect, retry); retry = Math.min(retry * 2, 5000); };
  socket.onerror = function(){ try { socket.close(); } catch (_) {} };
}
connect();
})();</script>`, version, appPort)
}

func injectHTMLResponse(resp *http.Response, version, appPort int, log *logger.Logger) error {
	if ok, reason := shouldInjectHTMLReason(resp); !ok {
		if log != nil {
			log.Verbosef("skipped HTML injection: %s", reason)
		}
		return nil
	}
	hadContentLength := resp.Header.Get("Content-Length") != ""

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if bytes.Contains(body, []byte("__gust_reload")) {
		if log != nil {
			log.Verbosef("skipped HTML injection: marker already present")
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if hadContentLength {
			resp.ContentLength = int64(len(body))
		} else {
			resp.ContentLength = -1
		}
		return nil
	}

	body = append(body, reloadScript(version, appPort)...)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if hadContentLength {
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	} else {
		resp.ContentLength = -1
		resp.Header.Set(flushNoLengthHeader, "1")
	}
	resp.Header.Del("ETag")
	resp.Header.Set("Cache-Control", "no-store")
	return nil
}

type flushNoLengthWriter struct {
	http.ResponseWriter
	flushed bool
}

func (w flushNoLengthWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *flushNoLengthWriter) WriteHeader(statusCode int) {
	if w.Header().Get(flushNoLengthHeader) == "1" {
		w.Header().Del(flushNoLengthHeader)
		w.ResponseWriter.WriteHeader(statusCode)
		w.flush()
		return
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *flushNoLengthWriter) Write(p []byte) (int, error) {
	if w.Header().Get(flushNoLengthHeader) == "1" {
		w.Header().Del(flushNoLengthHeader)
		w.flush()
	}
	return w.ResponseWriter.Write(p)
}

func (w *flushNoLengthWriter) flush() {
	if w.flushed {
		return
	}
	w.flushed = true
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func shouldInjectHTML(resp *http.Response) bool {
	ok, _ := shouldInjectHTMLReason(resp)
	return ok
}

func shouldInjectHTMLReason(resp *http.Response) (bool, string) {
	if resp == nil || resp.Request == nil {
		return false, "missing response request"
	}
	if resp.Request.Method == http.MethodHead {
		return false, "HEAD request"
	}
	if resp.Request.Header.Get("Range") != "" {
		return false, "range request"
	}
	if resp.StatusCode == http.StatusPartialContent {
		return false, "partial content response"
	}
	contentType := resp.Header.Get("Content-Type")
	lowerContentType := strings.ToLower(contentType)
	if strings.Contains(lowerContentType, "application/xhtml+xml") {
		return false, "xhtml content type"
	}
	if !strings.Contains(lowerContentType, "text/html") {
		return false, "non-html content type"
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Disposition")), "attachment") {
		return false, "attachment response"
	}
	encoding := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" {
		return false, "encoded response"
	}
	return true, ""
}

type retryTransport struct {
	base     http.RoundTripper
	timeout  time.Duration
	interval time.Duration
}

func (t retryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	deadline := time.Now().Add(t.timeout)
	var lastErr error
	for {
		attempt := r
		if r.GetBody != nil {
			body, err := r.GetBody()
			if err != nil {
				return nil, err
			}
			clone := r.Clone(r.Context())
			clone.Body = body
			attempt = clone
		}

		resp, err := base.RoundTrip(attempt)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if time.Now().Add(t.interval).After(deadline) {
			return nil, lastErr
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-time.After(t.interval):
		}
	}
}
