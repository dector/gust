package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
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

	statusMu        sync.RWMutex
	statusProvider  func(context.Context) (coordinator.Status, error)
	comments        *comments.Store
	commentsEnabled bool
	selfDev         bool
	bootID          string
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
	BootID  string `json:"bootId,omitempty"`
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

	s := &Server{listener: ln, log: log, hub: &BrowserHub{clients: map[*browserClient]struct{}{}}, appPort: cfg.AppPort, proxyPort: cfg.ProxyPort, commentsEnabled: cfg.CommentsEnabled, selfDev: cfg.SelfDev, bootID: rand.Text()}
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

// SetCommentsEnabled controls the browser comments UI and API.
func (s *Server) SetCommentsEnabled(enabled bool) {
	if s != nil {
		s.commentsEnabled = enabled
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
		return injectHTMLResponse(resp, s.hub.currentVersion(), s.appPort, s.log, s.commentsEnabled, s.selfDev)
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
			if !s.commentsEnabled {
				http.NotFound(w, r)
				return
			}
			s.serveComments(w, r)
			return
		}
		commentID := strings.TrimPrefix(r.URL.Path, "/__gust/comments/")
		if strings.HasPrefix(r.URL.Path, "/__gust/comments/") && len(commentID) == 32 && strings.Trim(commentID, "0123456789abcdef") == "" {
			if !s.commentsEnabled {
				http.NotFound(w, r)
				return
			}
			s.serveCommentDelete(w, r)
			return
		}
		if r.URL.Path == "/__gust/comments/submit" {
			if !s.commentsEnabled {
				http.NotFound(w, r)
				return
			}
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
	boot, _ := json.Marshal(browserMessage{Type: "boot", BootID: s.bootID})
	_ = client.write(boot)
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

func reloadScript(version, appPort int, options ...bool) string {
	commentsOn := true
	if len(options) > 0 {
		commentsOn = options[0]
	}
	selfDev := len(options) > 1 && options[1]
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
let connectionAttempted = false;
let bootID = "";
let restartPending = false;
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
let commentToolbar = null;
let windButton = null;
let windEnabled = false;
let windTimer = null;
let windFrame = null;
let windOverlay = null;
const windMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
let selecting = false;
let hoverPath = [];
let selectedIndex = 0;
let highlighted = null;
let selectedElement = null;
let selectedPoint = null;
let editorAnchor = null;
let editorOpen = false;
let commentState = [];
let commentRefreshTimer = null;
let submittingComments = false;
let pinOverlay = null;
let pinSizeObserver = typeof ResizeObserver !== "undefined" ? new ResizeObserver(schedulePinReposition) : null;
let pathFrame = 0;
const commentIconSvg='<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M2.992 16.342a2 2 0 0 1 .094 1.167l-1.065 3.29a1 1 0 0 0 1.236 1.168l3.413-.998a2 2 0 0 1 1.099.092 10 10 0 1 0-4.777-4.719"/><path d="M8 12h.01"/><path d="M12 12h.01"/><path d="M16 12h.01"/></svg>';
// The cursor hotspot is the bubble's lower-left tail, where the click lands.
const commentCursor="url('data:image/svg+xml,"+encodeURIComponent(commentIconSvg.replace("currentColor","#f59e0b"))+"') 2 21, pointer";
const gustAppPort = %d;
const selfDev = %t;
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
function applyIconState(){ if (!gustIcon) return; gustIcon.classList.toggle("__gust_icon_online", connected && !failing); gustIcon.classList.toggle("__gust_icon_offline", !connected && connectionAttempted); gustIcon.classList.toggle("__gust_icon_failing", connected && failing); if (gustWidget) gustWidget.classList.toggle("__gust_wide", failing); }
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
  if (commentToolbar) gustPanel.prepend(commentToolbar);
  if (commentUI) gustPanel.appendChild(commentUI);
  renderCommentState();
  updateCommentUI();
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
function isPrivatePanelNode(el){ return !!(el && el.closest && el.closest("details.__gust_log,#__gust_comments [data-comments]")); }
function isSelfDevPanel(el){ return !!(selfDev && el && el.closest && el.closest("#__gust_panel") && !isPrivatePanelNode(el)); }
function blockedCommentTarget(el){ return isGustNode(el) && !isSelfDevPanel(el); }
function meaningfulPath(el){
  if (!el || el.nodeType !== 1 || blockedCommentTarget(el)) return {path:[], guessedIndex:0};
  let guess = el;
  const interactive = el.closest("button,a,input,textarea,select,[role=button],[role=link]");
  if (interactive && !blockedCommentTarget(interactive)) guess = interactive;
  else {
    let n = el;
    while (n.parentElement && n.parentElement !== document.body && n.parentElement !== document.documentElement) {
      const p = n.parentElement;
      if (isSelfDevPanel(el) && p.id === "__gust_widget") break;
      if (p.children.length > 1 || (p.textContent || "").trim().length > 180) break;
      n = p;
    }
    guess = n;
  }
  const path = [];
  for (let n = el; n && path.length < 12; n = n.parentElement) {
    if (blockedCommentTarget(n)) break;
    path.push(n);
    if (isSelfDevPanel(el) && n.id === "__gust_panel") break;
  }
  return {path:path, guessedIndex:Math.max(0,path.indexOf(guess))};
}
function setHighlight(el){
  if (highlighted) highlighted.classList.remove("__gust_highlight");
  highlighted = el;
  if (highlighted) highlighted.classList.add("__gust_highlight");
}
function elementSnippet(el){
  if(!el||el.nodeType!==1)return "";
  let text="";
  if(el.matches&&el.matches("input,textarea,select")){
    if(el.type!=="password")text=el.value||"";
    if(!text)text=el.getAttribute("aria-label")||el.getAttribute("placeholder")||el.getAttribute("name")||"";
  }else{
    text=el.getAttribute("aria-label")||el.getAttribute("alt")||el.innerText||el.textContent||"";
  }
  const normalized=String(text).replace(/\s+/g," ").trim();
  return normalized.length>120?normalized.slice(0,119)+"…":normalized;
}
function fullAncestry(el){
  const chain=[];
  for(let n=el;n&&n.nodeType===1;n=n.parentElement){chain.unshift(n);if(n===document.documentElement)break;}
  return chain;
}
function renderPath(){
  pathFrame=0;if(!commentUI)return;
  const crumbs=document.getElementById("__gust_selected_path");if(!crumbs)return;
  const target=currentTargetEl();
  if(!target||!target.isConnected){crumbs.hidden=true;crumbs.replaceChildren();return;}
  const chain=fullAncestry(target);if(!chain.length)return;
  const tag=target.tagName.toLowerCase();
  const label=target.matches("html,head,body")?"":elementSnippet(target);
  const ancestors=chain.slice(0,-1).map(function(el){return el.tagName.toLowerCase();});
  const trail=document.createElement("div");trail.className="__gust_path_trail";
  const addPart=function(name,className){
    if(trail.childNodes.length){const separator=document.createElement("span");separator.className="__gust_path_separator";separator.setAttribute("aria-hidden","true");separator.textContent="›";trail.appendChild(separator);}
    const part=document.createElement("span");part.className=className;part.textContent=name;trail.appendChild(part);
  };
  ancestors.forEach(function(name){addPart(name,"__gust_path_ancestor");});
  addPart(tag,"__gust_path_current");
  crumbs.replaceChildren(trail);
  if(label){const description=document.createElement("span");description.className="__gust_path_description";description.textContent=label;crumbs.appendChild(description);}
  crumbs.title=ancestors.concat([tag]).join(" > ")+(label?' — "'+label+'"':"");
  crumbs.hidden=false;
}
function scheduleRenderPath(){if(pathFrame)return;pathFrame=requestAnimationFrame(renderPath);}
function parseLocator(c){ try { return JSON.parse(c.locator); } catch (_) { return null; } }
function matchingElement(c){
  const l=parseLocator(c); if(!l||!l.selector||l.confidence!=="high"||l.matches!==1)return null;
  let matches; try{matches=document.querySelectorAll(l.selector);}catch(_){return null;}
  if(matches.length!==1)return null;
  const el=matches[0]; if(blockedCommentTarget(el)||el.tagName.toLowerCase()!==l.tag)return null;
  if(l.text){const actual=(el.innerText||el.getAttribute("aria-label")||"").trim().replace(/\s+/g," ");if(!actual.includes(l.text))return null;}
  return el;
}
function ensurePinOverlay(){
  if(pinOverlay&&pinOverlay.isConnected)return pinOverlay;
  pinOverlay=document.createElement("div");pinOverlay.dataset.gustOverlay="";pinOverlay.id="__gust_pin_overlay";
  pinOverlay.style.cssText="position:fixed;inset:0;pointer-events:none;z-index:2147483645;";document.documentElement.appendChild(pinOverlay);return pinOverlay;
}
function pinPosition(c,el){
  const r=el.getBoundingClientRect(),p=parseLocator(c)?.point;
  if(!r.width||!r.height)return null;
  // Older comments without a point keep their original upper-right placement.
  const x=p&&Number.isFinite(p.x)&&p.x>=0&&p.x<=1?r.left+r.width*p.x:r.right;
  const y=p&&Number.isFinite(p.y)&&p.y>=0&&p.y<=1?r.top+r.height*p.y:r.top;
  if(x<0||x>innerWidth||y<0||y>innerHeight)return null;
  return {left:Math.max(0,Math.min(innerWidth-20,x-10)),top:Math.max(0,Math.min(innerHeight-20,y-10))};
}
function repositionPins(){
  if(!pinOverlay||!pinOverlay.isConnected)return;
  Array.from(pinOverlay.children).forEach(function(pin){
    const c=commentState.find(function(x){return x.id===pin.dataset.commentId;});
    const el=c&&c.path===location.pathname&&matchingElement(c);
    const pos=el&&pinPosition(c,el);
    pin.style.display=pos?"":"none";
    if(pos){pin.style.left=pos.left+"px";pin.style.top=pos.top+"px";}
  });
}
let pinPositionFrame=0;
function schedulePinReposition(){
  if(pinPositionFrame)return;
  pinPositionFrame=requestAnimationFrame(function(){pinPositionFrame=0;repositionPins();});
}
window.addEventListener("scroll",schedulePinReposition,true);
window.addEventListener("resize",schedulePinReposition);
function focusComment(id){
  const row=commentUI&&Array.from(commentUI.querySelectorAll("[data-comment-id]")).find(function(r){return r.dataset.commentId===id;});
  if(row){row.scrollIntoView({block:"nearest"});row.focus();}
  const c=commentState.find(function(x){return x.id===id;});const el=c&&matchingElement(c);
  if(el){el.scrollIntoView({block:"center",inline:"nearest",behavior:"smooth"});setHighlight(el);setTimeout(function(){if(highlighted===el)setHighlight(null);},1600);}
}
function renderCommentState(){
  if(!commentUI)return;
  const list=commentUI.querySelector("[data-comments]"); if(!list)return;list.replaceChildren();
  const active=commentState.filter(c=>c.state!=="done"&&c.state!=="abandoned");
  const created=active.filter(c=>c.state==="created").length;
  const submit=commentUI.querySelector("[data-submit]");submit.hidden=!created;submit.disabled=submittingComments;
  submit.textContent="Submit "+created;
  commentUI.querySelector("[data-comment-count]").textContent=active.length?String(active.length):"";
  const finished=commentState.length-active.length;
  active.slice().reverse().forEach(function(c){
    const row=document.createElement("div");row.className="__gust_comment_item";row.dataset.commentId=c.id;row.tabIndex=-1;
    const open=document.createElement("button");open.type="button";open.className="__gust_comment_row __gust_comment_row_"+c.state;
    const body=document.createElement("span");body.className="__gust_comment_text";body.textContent=(c.text||"").replace(/\s+/g," ").trim()||"(empty comment)";open.appendChild(body);
    const stateLabel={created:"Draft",submitted:"Submitted",seen:"In progress"}[c.state]||c.state||"Unknown";
    const onPage=c.path===location.pathname;
    const locationFound=onPage&&!!matchingElement(c);
    const meta=document.createElement("span");meta.className="__gust_comment_meta";
    const badge=document.createElement("span");badge.className="__gust_comment_badge __gust_comment_badge_"+c.state;badge.textContent=stateLabel;
    if(c.state==="seen"){const spinner=document.createElement("span");spinner.className="__gust_comment_spinner";spinner.setAttribute("aria-hidden","true");meta.appendChild(spinner);}
    meta.appendChild(badge);
    const path=document.createElement("span");path.className="__gust_comment_path";path.textContent=c.path||"/";meta.appendChild(path);
    if(onPage&&!locationFound){const missing=document.createElement("span");missing.className="__gust_comment_missing";missing.textContent="Not found";meta.appendChild(missing);}
    open.title=onPage?(locationFound?"Show on page":"Location not found on this page"):"Comment on another page";
    open.appendChild(meta);
    open.addEventListener("click",function(){focusComment(c.id);});row.appendChild(open);
    if(c.state==="created"){
      const remove=document.createElement("button");remove.type="button";remove.className="__gust_remove_draft";remove.textContent="×";remove.title="Remove draft";remove.setAttribute("aria-label","Remove draft");
      remove.addEventListener("click",function(){
        remove.disabled=true;
        fetch("/__gust/comments/"+encodeURIComponent(c.id),{method:"DELETE"})
          .then(function(r){if(!r.ok)return r.json().catch(function(){return {};}).then(function(data){throw new Error(data.error&&data.error.message||("Request failed ("+r.status+")"));});})
          .then(function(){refreshComments();})
          .catch(function(e){commentUI.querySelector("[data-poll-error]").textContent="Could not remove draft: "+e.message;remove.disabled=false;});
      });row.appendChild(remove);
    }
    list.appendChild(row);
  });
  if(!active.length){const empty=document.createElement("div");empty.className="__gust_comments_empty";empty.textContent="No open comments yet.";list.appendChild(empty);}
  if(finished){const summary=document.createElement("div");summary.className="__gust_done_summary";summary.textContent=finished+" finished";list.appendChild(summary);}
  const overlay=ensurePinOverlay();overlay.replaceChildren();if(pinSizeObserver)pinSizeObserver.disconnect();
  commentState.filter(c=>c.path===location.pathname&&(c.state==="created"||c.state==="submitted"||c.state==="seen")).forEach(function(c){
    const el=matchingElement(c);if(!el)return;const pos=pinPosition(c,el);if(pinSizeObserver)pinSizeObserver.observe(el);
    const pin=document.createElement("button");pin.type="button";pin.dataset.commentId=c.id;pin.className="__gust_pin __gust_pin_"+c.state;pin.innerHTML=commentIconSvg;pin.title=c.state+" comment — click to inspect";pin.setAttribute("aria-label",c.state+" comment");
    pin.style.cssText="position:fixed;left:"+(pos?pos.left:0)+"px;top:"+(pos?pos.top:0)+"px;pointer-events:auto;";pin.style.display=pos?"":"none";
    pin.addEventListener("click",function(e){e.preventDefault();e.stopPropagation();pinned=true;savePinned();syncPanel();focusComment(c.id);});overlay.appendChild(pin);
  });
}
function refreshComments(){
  fetch("/__gust/comments",{cache:"no-store"}).then(function(r){if(!r.ok)throw new Error("Could not refresh comments ("+r.status+")");return r.json();}).then(function(data){if(!Array.isArray(data))throw new Error("Invalid comments response");commentState=data;commentUI.querySelector("[data-poll-error]").textContent="";renderCommentState();}).catch(function(e){if(commentUI){commentUI.querySelector("[data-poll-error]").textContent="Comment sync failed: "+e.message;}});
}
function startCommentRefresh(){if(!commentUI||commentRefreshTimer)return;refreshComments();commentRefreshTimer=setInterval(refreshComments,3000);}
function updateEditorPosition(){
  if(!commentUI||!editorOpen||!editorAnchor)return;
  const editor=document.querySelector("[data-editor]");
  const width=editor.offsetWidth||Math.min(320,innerWidth-24),height=editor.offsetHeight||160;
  const margin=12, gap=12;
  let left=editorAnchor.x+gap;
  if(left+width>innerWidth-margin)left=editorAnchor.x-width-gap;
  let top=editorAnchor.y+gap;
  if(top+height>innerHeight-margin)top=editorAnchor.y-height-gap;
  editor.style.left=Math.max(margin,Math.min(innerWidth-width-margin,left))+"px";
  editor.style.top=Math.max(margin,Math.min(innerHeight-height-margin,top))+"px";
}
function currentTargetEl(){return editorOpen?selectedElement:(selecting&&hoverPath.length?hoverPath[selectedIndex]:null);}
function updateCommentUI(){
  if(!commentUI)return;
  document.documentElement.classList.toggle("__gust_selecting",selecting);
  const toggle=commentToolbar.querySelector("button:not(#__gust_wind_button)"),active=selecting||editorOpen;toggle.setAttribute("aria-pressed",String(active));toggle.setAttribute("aria-label",active?"Exit comment mode":"Add comment");toggle.title=active?(selfDev?"Exit comment mode (Ctrl+click to select Gust panel elements)":"Exit comment mode"):(selfDev?"Add comment (Ctrl+click to select Gust panel elements)":"Add comment");const title=commentToolbar.querySelector("[data-mode-title]");if(title)title.hidden=!active;const auto=commentUI.querySelector("[data-autosubmit-label]");if(auto)auto.hidden=!active;
  const editor=commentUI.querySelector("[data-editor]")||document.querySelector("[data-editor]");if(editor){editor.hidden=!editorOpen;editor.style.display=editorOpen?"block":"none";if(editorOpen)updateEditorPosition();}
  scheduleRenderPath();
}
function closeCommentMode(){selecting=false;editorOpen=false;selectedElement=null;selectedPoint=null;editorAnchor=null;hoverPath=[];setHighlight(null);saveCommentMode();updateCommentUI();}
function beginSelection(){if(selecting||editorOpen){closeCommentMode();return;}selecting=true;editorOpen=false;selectedElement=null;selectedPoint=null;editorAnchor=null;hoverPath=[];setHighlight(null);saveCommentMode();pinned=true;savePinned();syncPanel();updateCommentUI();}
function resumeSelection(){selecting=true;editorOpen=false;selectedElement=null;selectedPoint=null;editorAnchor=null;hoverPath=[];setHighlight(null);updateCommentUI();}
function chooseSelection(e){if(!hoverPath.length)return;selectedIndex=Math.max(0,Math.min(selectedIndex,hoverPath.length-1));selectedElement=hoverPath[selectedIndex];
  const r=selectedElement.getBoundingClientRect();
  selectedPoint=r.width>0&&r.height>0?{x:Math.max(0,Math.min(1,(e.clientX-r.left)/r.width)),y:Math.max(0,Math.min(1,(e.clientY-r.top)/r.height))}:null;
  editorAnchor={x:e.clientX,y:e.clientY};
  selecting=false;editorOpen=true;hoverPath=[];setHighlight(null);updateCommentUI();const box=document.querySelector("[data-editor] textarea");if(box)box.focus();}
function cssEscape(v){ return window.CSS && CSS.escape ? CSS.escape(v) : String(v).replace(/[^a-zA-Z0-9_-]/g,"\\$&"); }
function locatorFor(el,point){
  const tag=el.tagName.toLowerCase();
  let selector="";
  if(el===document.body||el===document.documentElement)selector=tag;
  if(!selector&&el.id){ const candidate="#"+cssEscape(el.id); if(document.querySelectorAll(candidate).length===1) selector=candidate; }
  if(!selector){
    const attrs=["data-testid","name","aria-label"];
    for(const a of attrs){ const v=el.getAttribute(a); if(v){ const candidate=tag+"["+a+"=\""+v.replace(/\\/g,"\\\\").replace(/"/g,"\\\"")+"\"]"; try{if(document.querySelectorAll(candidate).length===1){selector=candidate;break;}}catch(_){} } }
  }
  if(!selector){ let n=el, bits=[]; while(n&&n.nodeType===1&&n!==document.body&&bits.length<5){let bit=n.tagName.toLowerCase();if(n.parentElement){const same=Array.from(n.parentElement.children).filter(x=>x.tagName===n.tagName);if(same.length>1)bit+=":nth-of-type("+(same.indexOf(n)+1)+")";}bits.unshift(bit);const s=bits.join(" > ");try{if(document.querySelectorAll(s).length===1){selector=s;break;}}catch(_){} n=n.parentElement;} }
  const hasPrivateChildren=isSelfDevPanel(el)&&!!el.querySelector("details.__gust_log,#__gust_comments [data-comments]");
  const text=(el===document.body||el===document.documentElement||isPrivatePanelNode(el)||hasPrivateChildren)?"":(el.innerText||el.getAttribute("aria-label")||"").trim().replace(/\s+/g," ").slice(0,160);
  let count=0;try{count=selector?document.querySelectorAll(selector).length:0;}catch(_){}
  return JSON.stringify({selector:selector,tag:tag,text:text,confidence:selector&&count===1?"high":"low",matches:count,point:point});
}
function safeOuterHTML(el){
  if(isPrivatePanelNode(el)||el.matches("input[type=password],input[type=hidden]"))return "";
  const clone=el.cloneNode(true);
  if(clone.matches("textarea"))clone.textContent="";
  clone.querySelectorAll("script,style,input[type=password],input[type=hidden],#__gust_widget,#__gust_error,[data-gust-overlay],details.__gust_log,#__gust_comments [data-comments]").forEach(n=>n.remove());
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
  const add=document.createElement("button");add.type="button";add.title="Add comment";add.setAttribute("aria-label","Add comment");add.setAttribute("aria-pressed","false");
  add.innerHTML=commentIconSvg;
  add.addEventListener("click",beginSelection);commentToolbar.insertBefore(add,windButton);
  const modeTitle=document.createElement("span");modeTitle.dataset.modeTitle="";modeTitle.textContent="Comment Mode";modeTitle.hidden=true;commentToolbar.insertBefore(modeTitle,windButton);
  const autoLabel=document.createElement("label");autoLabel.dataset.autosubmitLabel="";autoLabel.className="__gust_autosubmit";autoLabel.hidden=true;
  const auto=document.createElement("input");auto.type="checkbox";auto.checked=true;auto.dataset.autosubmit="";autoLabel.append(auto,document.createTextNode(" Autosubmit"));
  const status=document.createElement("div");status.dataset.status="";
  const crumbs=document.createElement("div");crumbs.id="__gust_selected_path";crumbs.dataset.crumbs="";crumbs.dataset.gustOverlay="";crumbs.hidden=true;
  document.documentElement.appendChild(crumbs);

  const listHeader=document.createElement("div");listHeader.className="__gust_comments_header";
  const listTitle=document.createElement("span");listTitle.textContent="Comments";
  const count=document.createElement("span");count.dataset.commentCount="";count.className="__gust_comments_count";
  const submit=document.createElement("button");submit.type="button";submit.textContent="Submit";submit.dataset.submit="";submit.hidden=true;
  listHeader.append(listTitle,count,submit);
  const submitResult=document.createElement("span");submitResult.dataset.submitResult="";
  submit.addEventListener("click",function(){if(submittingComments)return;submittingComments=true;submit.disabled=true;submitResult.textContent="Submitting…";fetch("/__gust/comments/submit",{method:"POST",headers:{"Content-Type":"application/json"},body:"{}"}).then(function(r){return r.json().catch(function(){return {};}).then(function(data){if(!r.ok)throw new Error(data.error&&data.error.message||("Request failed ("+r.status+")"));return data;});}).then(function(){submitResult.textContent="Comments submitted.";refreshComments();}).catch(function(e){submitResult.textContent=e.message;}).finally(function(){submittingComments=false;renderCommentState();});});
  const pollError=document.createElement("div");pollError.dataset.pollError="";
  const list=document.createElement("div");list.dataset.comments="";
  const editor=document.createElement("div");editor.id="__gust_comment_editor";editor.dataset.editor="";editor.dataset.gustOverlay="";editor.hidden=true;
  const textarea=document.createElement("textarea");textarea.placeholder="Describe this element";textarea.maxLength=8192;
  const close=document.createElement("button");close.type="button";close.textContent="×";close.setAttribute("aria-label","Close comment editor");close.addEventListener("click",function(){textarea.value="";result.textContent="";resumeSelection();});
  const save=document.createElement("button");save.type="button";save.textContent="Save comment";
  const result=document.createElement("span");result.dataset.result="";
  textarea.addEventListener("keydown",function(e){if(e.key==="Enter"&&e.ctrlKey&&!e.isComposing){e.preventDefault();save.click();}});
  save.addEventListener("click",function(){
    if(save.disabled)return;
    const el=selectedElement; const text=textarea.value.trim();
    if(!el||!text){result.textContent="Choose an element and enter a comment.";return;}
    save.disabled=true;result.textContent="Saving…";
    fetch("/__gust/comments",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({path:location.pathname, text:text, locator:locatorFor(el,selectedPoint), html:safeOuterHTML(el)})})
      .then(function(r){return r.json().catch(function(){return {};}).then(function(data){if(!r.ok)throw new Error(data.error&&data.error.message||("Request failed ("+r.status+")"));return data;});})
      .then(function(comment){textarea.value="";result.textContent="Draft saved.";refreshComments();resumeSelection();
        if(!auto.checked)return;
        submitResult.textContent="Submitting…";
        return fetch("/__gust/comments/submit?id="+encodeURIComponent(comment.id),{method:"POST",headers:{"Content-Type":"application/json"},body:"{}"})
          .then(function(r){return r.json().catch(function(){return {};}).then(function(data){if(!r.ok)throw new Error(data.error&&data.error.message||("Request failed ("+r.status+")"));return data;});})
          .then(function(){submitResult.textContent="Comment submitted.";refreshComments();})
          .catch(function(e){submitResult.textContent="Autosubmit failed: "+e.message+" — use Submit to retry.";refreshComments();});
      })
      .catch(function(e){result.textContent=e.message;}).finally(function(){save.disabled=false;});
  });
  const title=document.createElement("strong");title.textContent="Add a comment";
  const hint=document.createElement("small");hint.textContent="Ctrl+Enter to save";
  editor.append(close,title,textarea,save,hint,result);
  const editorStyle=document.createElement("style");editorStyle.textContent=
    '#__gust_comment_editor{position:fixed;display:none;z-index:2147483647;width:min(320px,calc(100vw - 24px));max-height:calc(100vh - 24px);overflow:auto;box-sizing:border-box;padding:16px;background:#1c1c1c;color:#fafafa;border:1px solid #555;border-radius:12px;box-shadow:0 16px 48px rgba(0,0,0,.55);font:13px/1.5 system-ui,sans-serif}'+
    '#__gust_comment_editor strong{display:block;margin:0 28px 12px 0;font-size:14px;font-weight:600}'+
    '#__gust_comment_editor textarea{display:block;box-sizing:border-box;width:100%%;min-height:100px;resize:vertical;margin:0 0 12px;padding:10px 12px;background:#262626;color:#fafafa;border:1px solid #555;border-radius:8px;outline:none;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace}'+
    '#__gust_comment_editor textarea:focus{border-color:#f59e0b;box-shadow:0 0 0 2px #f59e0b33}'+
    '#__gust_comment_editor button{cursor:pointer;font:inherit}'+
    '#__gust_comment_editor button[aria-label="Close comment editor"]{position:absolute;right:12px;top:10px;padding:2px 8px;border:0;border-radius:6px;background:transparent;color:#aaa;font-size:20px}'+
    '#__gust_comment_editor button[aria-label="Close comment editor"]:hover{color:white;background:#333}'+
    '#__gust_comment_editor button:not([aria-label]){padding:7px 12px;border:1px solid #d97706;border-radius:7px;background:#a7550b;color:white;font-weight:600}'+
    '#__gust_comment_editor button:not([aria-label]):hover{background:#b9630e}'+
    '#__gust_comment_editor button:focus-visible{outline:2px solid #f59e0b;outline-offset:2px}'+
    '#__gust_comment_editor small{margin-left:10px;color:#aaa;font:11px ui-monospace,SFMono-Regular,Menlo,monospace}'+
    '#__gust_comment_editor [data-result]{display:block;margin-top:6px;color:#fbbf24;font-size:12px}';
  // Keep the editor readable over any host page, using the same warm palette as the panel.
  editorStyle.textContent +=
    '#__gust_comment_editor{color-scheme:dark;background:#24180f;color:#fff7e9;border-color:#f9bb7155}'+
    '#__gust_comment_editor textarea{background:#352416;color:#fff7e9;border-color:#f9bb7155}'+
    '#__gust_comment_editor textarea:focus{border-color:#f9bb71;box-shadow:0 0 0 2px #f9bb7133}'+
    '#__gust_comment_editor button[aria-label="Close comment editor"]{color:#e0cfba}'+
    '#__gust_comment_editor button[aria-label="Close comment editor"]:hover{color:#fff7e9;background:#49301c}'+
    '#__gust_comment_editor button:not([aria-label]){border-color:#f9bb71;background:#704423;color:#fff7e9}'+
    '#__gust_comment_editor button:not([aria-label]):hover{background:#90552a}'+
    '#__gust_comment_editor button:focus-visible{outline-color:#f9bb71}'+
    '#__gust_comment_editor small{color:#e0cfba}'+
    '#__gust_comment_editor [data-result]{color:#ffca81}';
  document.documentElement.append(editorStyle,editor);
  commentUI.append(autoLabel,status,listHeader,submitResult,pollError,list);updateCommentUI();
}
function updateHoverPath(target){
  const result=meaningfulPath(target),path=result.path;
  if(!path.length){hoverPath=[];setHighlight(null);updateCommentUI();return false;}
  hoverPath=path;selectedIndex=result.guessedIndex;setHighlight(hoverPath[selectedIndex]);updateCommentUI();return true;
}
document.addEventListener("mousemove",function(e){
  if(!selecting)return;
  if(blockedCommentTarget(e.target)||(isSelfDevPanel(e.target)&&!e.ctrlKey)){hoverPath=[];setHighlight(null);updateCommentUI();return;}
  updateHoverPath(e.target);
},true);
document.addEventListener("mouseout",function(e){if(selecting&&!e.relatedTarget){hoverPath=[];setHighlight(null);updateCommentUI();}},true);
window.addEventListener("scroll",function(){if(editorOpen)updateEditorPosition();},true);
window.addEventListener("resize",function(){if(editorOpen)updateEditorPosition();scheduleRenderPath();});
document.addEventListener("keydown",function(e){if(e.key==="Escape"){if(editorOpen){const box=document.querySelector("[data-editor] textarea");if(box)box.value="";resumeSelection();}else if(selecting)closeCommentMode();}},true);
document.addEventListener("click",function(e){
  if(!selecting||blockedCommentTarget(e.target)||(isSelfDevPanel(e.target)&&!e.ctrlKey))return;
  if(!updateHoverPath(e.target))return;
  e.preventDefault();e.stopPropagation();e.stopImmediatePropagation();
  chooseSelection(e);
},true);
function loadCommentMode(){
  try { return sessionStorage.getItem("__gust_comment_mode") === "1"; } catch (_) { return false; }
}
function saveCommentMode(){
  try { sessionStorage.setItem("__gust_comment_mode", (selecting||editorOpen) ? "1" : "0"); } catch (_) {}
}
function loadWind(){
  try { return localStorage.getItem("__gust_wind") === "1"; } catch (_) { return false; }
}
function clearWind(){
  clearTimeout(windTimer);windTimer=null;
  if(windFrame!==null){cancelAnimationFrame(windFrame);windFrame=null;}
  if(windOverlay){windOverlay.remove();windOverlay=null;}
}
function windTraceCount(){
  const roll=Math.random();
  // Usually one trace; four or five are occasional accents.
  return roll<.70?1:roll<.85?2:roll<.94?3:roll<.98?4:5;
}
function scheduleWind(immediate){
  if(!windEnabled||windMotion.matches||document.hidden)return;
  // Start on activation, then leave a three-to-seven-second gap after each gust.
  windTimer=setTimeout(function(){
    windTimer=null;
    if(!windEnabled||windMotion.matches||document.hidden)return;
    windOverlay=document.createElement("div");windOverlay.id="__gust_wind";
    windOverlay.setAttribute("aria-hidden","true");windOverlay.dataset.gustOverlay="";
    windOverlay.innerHTML='<svg viewBox="0 0 1200 800" preserveAspectRatio="none" aria-hidden="true"></svg>';
    document.documentElement.appendChild(windOverlay);
    const svg=windOverlay.querySelector("svg"), traces=[];
    for(let n=0;n<windTraceCount();n++){
      const y=130+Math.random()*540, rise=80+Math.random()*100, dip=50+Math.random()*110;
      const path=document.createElementNS("http://www.w3.org/2000/svg","path");
      path.setAttribute("d","M-100 "+y+" C180 "+(y-rise)+" 340 "+(y+dip)+" 620 "+(y-50)+" S960 "+(y-rise)+" 1300 "+(y-20));
      svg.appendChild(path);
      const length=path.getTotalLength(), delay=Math.random()*1500;
      path.style.strokeDasharray="180 "+(length+180);
      path.style.strokeDashoffset="180";
      path.style.setProperty("--gust-wind-end",-(length+180));
      path.style.animationDelay=delay+"ms";
      const trace={path:path,length:length,delay:delay,sparks:[]};
      for(let i=0;i<2+Math.floor(Math.random()*2);i++){
        const spark=document.createElementNS("http://www.w3.org/2000/svg","circle");
        spark.setAttribute("r",2+Math.random()*2);spark.setAttribute("class","__gust_wind_follow");
        svg.appendChild(spark);
        trace.sparks.push({offset:8+Math.random()*140,jitter:(Math.random()-.5)*24,el:spark});
      }
      traces.push(trace);
    }
    // Nearby sparks track their own trace's dash; separate wanderers live 3–10 seconds.
    const started=performance.now(), period=9000;
    function moveFollowers(now){
      let running=false;
      traces.forEach(function(tr){
        const elapsed=(now-started-tr.delay)/period;
        if(elapsed<0||elapsed>1)return;
        running=running||elapsed<1;
        const at=(tr.length+360)*elapsed;
        tr.sparks.forEach(function(f){
          const s=at-f.offset;
          if(s<0||s>tr.length){f.el.style.opacity="0";return;}
          const p=tr.path.getPointAtLength(s),next=tr.path.getPointAtLength(Math.min(s+2,tr.length));
          const angle=Math.atan2(next.y-p.y,next.x-p.x);
          f.el.setAttribute("cx",p.x-Math.sin(angle)*f.jitter);
          f.el.setAttribute("cy",p.y+Math.cos(angle)*f.jitter);
          f.el.style.opacity="1";
        });
      });
      windFrame=running?requestAnimationFrame(moveFollowers):null;
    }
    windFrame=requestAnimationFrame(moveFollowers);
    for(let i=0;i<3+Math.floor(Math.random()*4);i++){
      const spark=document.createElement("i");spark.className="__gust_wind_wander";
      spark.style.top=(10+Math.random()*80)+"%%";
      spark.style.left=(5+Math.random()*85)+"%%";
      spark.style.animationDuration=(3+Math.random()*7)+"s";
      windOverlay.appendChild(spark);
    }
    // A trace needs up to 10.5s to clear the viewport; wait for wanderers too.
    windTimer=setTimeout(function(){clearWind();scheduleWind();},11000);
  },immediate?0:3000+Math.random()*4000);
}
function setWind(enabled){
  windEnabled=enabled;
  if(windButton)windButton.setAttribute("aria-pressed",String(enabled));
  try { localStorage.setItem("__gust_wind",enabled?"1":"0"); } catch (_) {}
  clearWind();scheduleWind(enabled);
}
windMotion.addEventListener("change",function(){clearWind();scheduleWind();});
document.addEventListener("visibilitychange",function(){clearWind();scheduleWind();});
function createToolbar(){
  commentToolbar=document.createElement("div");commentToolbar.id="__gust_comment_toolbar";
  windButton=document.createElement("button");windButton.type="button";
  windButton.title="Wind effect";windButton.setAttribute("aria-label","Wind effect");
  windButton.setAttribute("aria-pressed","false");windButton.id="__gust_wind_button";
  windButton.innerHTML='<svg viewBox="0 0 256 256" fill="none" aria-hidden="true"><path d="M128 192c3 9 14 16 24 16a24 24 0 0 0 0-48H40M96 64c3-9 14-16 24-16a24 24 0 0 1 0 48H24M184 96c3-9 14-16 24-16a24 24 0 0 1 0 48H32" stroke="currentColor" stroke-width="16" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  windButton.addEventListener("click",function(){setWind(!windEnabled);});
  commentToolbar.appendChild(windButton);
  windEnabled=loadWind();windButton.setAttribute("aria-pressed",String(windEnabled));scheduleWind(true);
}
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
    style.textContent = "#__gust_widget{position:fixed;right:8px;top:8px;z-index:2147483647}#__gust_icon{width:24px;height:24px;color:#a3a3a3;opacity:.45;transition:color .15s ease,opacity .15s ease;cursor:pointer}#__gust_icon:hover{color:#22c55e;opacity:1}#__gust_icon.__gust_pinned{color:#22c55e;opacity:1}#__gust_icon.__gust_icon_offline{color:#dc2626;opacity:1}#__gust_icon.__gust_icon_failing{color:#f59e0b;opacity:1}#__gust_panel{display:none;position:absolute;right:0;top:32px;background:#171717;color:#fafafa;font:14px/1.6 system-ui,sans-serif;padding:8px 10px;border-radius:6px;border:1px solid rgba(255,255,255,.12);box-shadow:0 6px 20px rgba(0,0,0,.45);white-space:normal;width:min(420px,calc(100vw - 32px));max-height:calc(100vh - 52px);overflow:auto;overflow-wrap:anywhere}#__gust_widget.__gust_open #__gust_panel{display:block}#__gust_widget.__gust_wide #__gust_panel{width:min(520px,calc(100vw - 32px));white-space:normal}#__gust_panel .__gust_row{display:flex;justify-content:space-between;gap:16px}#__gust_panel .__gust_label{color:#a3a3a3}#__gust_panel .__gust_group{margin-top:8px;padding:6px 8px;border:1px solid rgba(245,158,11,.4);border-radius:6px;background:rgba(245,158,11,.08);white-space:normal}#__gust_panel .__gust_group_title{color:#f59e0b;font-weight:600;margin-bottom:2px}#__gust_panel .__gust_notice{display:block;color:#f59e0b;margin-top:2px;white-space:normal;max-width:100%%}#__gust_panel .__gust_log{margin-top:4px;white-space:normal}#__gust_panel .__gust_log summary{cursor:pointer;color:#a3a3a3}#__gust_panel .__gust_log pre{max-height:200px;max-width:100%%;overflow:auto;margin:4px 0 0;padding:6px 8px;background:#262626;border:1px solid rgba(255,255,255,.1);border-radius:4px;white-space:pre-wrap;word-break:break-word;font:12px/1.4 ui-monospace,SFMono-Regular,Menlo,monospace}#__gust_panel .__gust_log code{font:inherit;background:transparent;padding:0;border:0;color:inherit}#__gust_panel .__gust_dot{display:inline-block;width:8px;height:8px;border-radius:50%%;margin-right:6px;vertical-align:middle}#__gust_comment_toolbar{display:flex;align-items:center;gap:4px;border-bottom:1px solid #404040;padding-bottom:6px;margin-bottom:6px}#__gust_comment_toolbar button{display:grid;place-items:center;width:34px;height:34px;padding:4px;border:1px solid transparent;border-radius:4px;background:transparent;color:#e5e5e5;cursor:pointer}#__gust_comment_toolbar button:hover,#__gust_comment_toolbar button[aria-pressed=true]{background:#404040;color:#fafafa}#__gust_comment_toolbar button:focus-visible{outline:2px solid #f59e0b;outline-offset:2px}#__gust_comment_toolbar svg{display:block}#__gust_comment_toolbar [data-mode-title]{font-weight:600;color:#fafafa;padding-left:8px;border-left:1px solid #525252}#__gust_comments{margin-top:8px;border-top:1px solid #404040;padding-top:8px;white-space:normal}#__gust_comments button{font:inherit;cursor:pointer;margin:2px;padding:3px 7px}#__gust_comments textarea,[data-editor] textarea{box-sizing:border-box;width:100%%;min-height:70px;background:#262626;color:#fafafa;border:1px solid #525252;padding:6px}#__gust_comments .__gust_comment_row{display:block;box-sizing:border-box;width:100%%;text-align:left;padding:5px 6px;color:#e5e5e5;font-size:12px;white-space:normal;overflow-wrap:anywhere;background:#262626;border:1px solid #404040;border-radius:4px}#__gust_comments .__gust_comment_meta{display:block;color:#a3a3a3;font-size:11px}#__gust_comments .__gust_comment_item{display:flex;align-items:stretch;gap:4px;margin:4px 0}#__gust_comments .__gust_comment_row{flex:1;min-width:0;margin:0}#__gust_comments .__gust_remove_draft{align-self:start;flex:none;color:#fca5a5;background:#262626;border:1px solid #525252;border-radius:4px;line-height:1;padding:4px 7px}#__gust_comments .__gust_remove_draft:hover{background:#7f1d1d}#__gust_comments .__gust_comment_row_created{border-left:3px solid #f59e0b}#__gust_comments .__gust_comment_row_submitted{border-left:3px solid #60a5fa}#__gust_comments .__gust_comment_row_seen{border-left:3px solid #a78bfa}#__gust_comments .__gust_comment_badge{display:inline-block;font-size:11px;font-weight:700}#__gust_comments .__gust_comment_badge_created{color:#fbbf24}#__gust_comments .__gust_comment_badge_submitted{color:#93c5fd}#__gust_comments .__gust_comment_badge_seen{color:#c4b5fd}#__gust_comments .__gust_comment_spinner{display:inline-block;width:9px;height:9px;margin-right:5px;border:2px solid #525252;border-top-color:#c4b5fd;border-radius:50%%;vertical-align:-2px;animation:__gust_comment_spin .9s linear infinite}@keyframes __gust_comment_spin{to{transform:rotate(360deg)}}@media (prefers-reduced-motion:reduce){#__gust_comments .__gust_comment_spinner{animation:none;border-color:#c4b5fd}}#__gust_comments .__gust_done_summary{text-align:center;color:#a3a3a3;font-size:12px;padding:8px}#__gust_comments .__gust_autosubmit{display:flex;align-items:center;gap:4px;cursor:pointer;font-size:12px;color:#d4d4d4}#__gust_comments .__gust_autosubmit[hidden]{display:none}#__gust_comments [data-poll-error]{color:#fca5a5;font-size:12px}.__gust_pin{border:0;background:transparent;padding:0;cursor:pointer;width:20px;height:20px;line-height:0;filter:drop-shadow(0 1px 2px rgba(0,0,0,.55))}.__gust_pin svg{display:block;width:20px;height:20px}.__gust_pin_created{color:#f59e0b}.__gust_pin_submitted{color:#60a5fa}.__gust_pin_seen{color:#a78bfa}.__gust_highlight{outline:3px solid #f59e0b!important;outline-offset:2px!important}html.__gust_selecting,html.__gust_selecting *{cursor:"+commentCursor+"!important}html.__gust_selecting #__gust_widget,html.__gust_selecting #__gust_widget *,html.__gust_selecting [data-gust-overlay],html.__gust_selecting [data-gust-overlay] *{cursor:auto!important}html.__gust_selecting #__gust_widget button,html.__gust_selecting [data-gust-overlay] button{cursor:pointer!important}html.__gust_selecting #__gust_widget textarea,html.__gust_selecting [data-gust-overlay] textarea{cursor:text!important}";
    style.textContent += `+"`"+`
#__gust_comments{margin-top:12px;padding-top:12px;border-color:#353535}
#__gust_comments .__gust_comments_header{display:flex;align-items:center;gap:8px;margin-bottom:10px;color:#f5f5f5;font-size:12px;font-weight:650;letter-spacing:.01em}
#__gust_comments .__gust_comments_count{display:inline-grid;place-items:center;min-width:17px;height:17px;padding:0 4px;box-sizing:border-box;border-radius:9px;background:#343434;color:#bcbcbc;font-size:10px;font-weight:600}
#__gust_comments .__gust_comments_count:empty{display:none}
#__gust_comments [data-submit]{margin:0 0 0 auto;padding:4px 9px;border:1px solid #875519;border-radius:6px;background:#4a321c;color:#fcd49a;font-size:11px;font-weight:600}
#__gust_comments [data-submit]:hover{background:#65401d;border-color:#b77923}
#__gust_comments [data-submit]:disabled{opacity:.5;cursor:wait}
#__gust_comments [data-submit][hidden]{display:none}
#__gust_comments [data-submit-result]:not(:empty),#__gust_comments [data-poll-error]:not(:empty){display:block;margin:0 0 8px;font-size:11px}
#__gust_comments [data-comments]{display:grid;gap:6px}
#__gust_comments .__gust_comment_item{position:relative;display:flex;align-items:stretch;gap:0;margin:0;border:1px solid #383838;border-radius:8px;background:#222;overflow:hidden;transition:background .15s,border-color .15s}
#__gust_comments .__gust_comment_item:hover{background:#292929;border-color:#555}
#__gust_comments .__gust_comment_item:focus-within{border-color:#b77923}
#__gust_comments .__gust_comment_row{display:block;flex:1;min-width:0;margin:0;padding:10px 12px;text-align:left;border:0;border-radius:0;background:transparent;color:#ededed;font-size:12px;line-height:1.45}
#__gust_comments .__gust_comment_row:hover{background:transparent}
#__gust_comments .__gust_comment_row:focus-visible,#__gust_comments .__gust_remove_draft:focus-visible{outline:2px solid #f59e0b;outline-offset:-2px}
#__gust_comments .__gust_comment_row_created,#__gust_comments .__gust_comment_row_submitted,#__gust_comments .__gust_comment_row_seen{border-left:2px solid transparent}
#__gust_comments .__gust_comment_row_created{border-left-color:#c88a38}
#__gust_comments .__gust_comment_row_submitted{border-left-color:#6698c2}
#__gust_comments .__gust_comment_row_seen{border-left-color:#a38ac8}
#__gust_comments .__gust_comment_text{display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:3;overflow:hidden;overflow-wrap:anywhere;white-space:normal}
#__gust_comments .__gust_comment_meta{display:flex;align-items:center;gap:6px;min-width:0;margin-top:6px;color:#888;font-size:10px;line-height:1.3}
#__gust_comments .__gust_comment_badge{flex:none;font-size:10px;font-weight:600}
#__gust_comments .__gust_comment_path{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;direction:rtl;text-align:left}
#__gust_comments .__gust_comment_missing{flex:none;color:#d2a174}
#__gust_comments .__gust_comment_spinner{flex:none;width:7px;height:7px;margin:0;border-width:1.5px;vertical-align:middle}
#__gust_comments .__gust_remove_draft{align-self:center;margin:0 8px 0 0;padding:2px 5px;border:0;border-radius:5px;background:transparent;color:#888;font-size:18px;line-height:1}
#__gust_comments .__gust_remove_draft:hover{background:#502c2c;color:#ffb4a9}
#__gust_comments .__gust_comments_empty{padding:14px 4px;color:#929292;font-size:12px;text-align:center}
#__gust_comments .__gust_done_summary{padding:5px;color:#777;font-size:11px;text-align:center}
#__gust_selected_path{position:fixed;bottom:16px;left:50%%;transform:translateX(-50%%);z-index:2147483646;box-sizing:border-box;width:max-content;max-width:calc(100vw - 24px);pointer-events:none;color:#fff;text-align:center;text-shadow:0 1px 3px #000,0 0 9px #000;font:12px/1.4 system-ui,sans-serif}
#__gust_selected_path[hidden]{display:none}
#__gust_selected_path .__gust_path_trail{display:flex;flex-wrap:wrap;align-items:center;justify-content:center;gap:5px;font:11px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
#__gust_selected_path .__gust_path_ancestor,#__gust_selected_path .__gust_path_separator{color:#dfdfdf}
#__gust_selected_path .__gust_path_separator{color:#bababa}
#__gust_selected_path .__gust_path_current{color:#ffdcac;font-weight:700}
#__gust_selected_path .__gust_path_description{display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:2;overflow:hidden;margin-top:3px;color:#eee;font-size:11px;overflow-wrap:anywhere}
/* Warm demo palette; keep status and comment-state colors distinct. */
#__gust_icon{position:relative;width:28px;height:28px;color:#f9bb71;opacity:1}
#__gust_icon svg{display:block;width:28px;height:28px}
#__gust_icon:hover,#__gust_icon.__gust_pinned,#__gust_icon.__gust_icon_offline,#__gust_icon.__gust_icon_failing{color:#f9bb71;opacity:1}
#__gust_icon::after{content:"";position:absolute;right:-2px;bottom:-2px;width:8px;height:8px;border:2px solid #f9bb71;border-radius:50%%;background:#24180f;box-shadow:0 0 0 1px #24180f;pointer-events:none}
#__gust_icon.__gust_icon_online::after{background:#22c55e;border-color:#24180f}
#__gust_icon.__gust_icon_offline::after{background:#dc2626;border-color:#24180f}
#__gust_icon.__gust_icon_failing::after{background:#f59e0b;border-color:#24180f}
#__gust_panel{top:36px;color-scheme:dark;background:#ffffff0d;color:#fff7e9;border:1px solid #ffffff24;border-radius:1.2rem;backdrop-filter:blur(12px);padding:12px 14px;box-shadow:0 16px 48px #0009,inset 0 1px #ffffff18}
#__gust_panel .__gust_label,#__gust_panel .__gust_log summary{color:#e0cfba}
#__gust_panel .__gust_group{border-color:#f9bb7155;background:#49301cbb}
#__gust_panel .__gust_group_title,#__gust_panel .__gust_notice{color:#ffca81}
#__gust_panel .__gust_log pre{background:#24180f;border-color:#f9bb7133}
#__gust_comment_toolbar{border-color:#f9bb7133}
#__gust_wind_button{margin-left:auto}
#__gust_wind_button svg{width:20px;height:20px}
#__gust_wind{position:fixed;inset:0;overflow:hidden;pointer-events:none!important;z-index:2147483645;opacity:.65;animation:__gust_wind_fade 11s ease-in-out both}
#__gust_wind *{pointer-events:none!important}
#__gust_wind svg{position:absolute;width:100%%;height:100%%;opacity:.65}
#__gust_wind path{fill:none;stroke:#f9bb71;stroke-width:2;stroke-linecap:round;animation:__gust_wind_stream 9s linear both}
#__gust_wind circle{fill:#fbd18e;filter:drop-shadow(0 0 6px #ed9a50);opacity:0}
#__gust_wind i{position:absolute;width:5px;height:5px;border-radius:50%%;background:#fbd18e;box-shadow:0 0 20px #ed9a50;animation-name:__gust_wind_wander;animation-timing-function:linear;animation-fill-mode:both}
@keyframes __gust_wind_fade{0%%{opacity:0}6%%,88%%{opacity:.65}100%%{opacity:0}}
@keyframes __gust_wind_stream{to{stroke-dashoffset:var(--gust-wind-end)}}
@keyframes __gust_wind_wander{0%%{transform:translate(-12vw,4vh) scale(.6);opacity:0}20%%,75%%{opacity:.9}100%%{transform:translate(40vw,-6vh) scale(1.1);opacity:0}}
#__gust_comment_toolbar button svg{width:20px;height:20px}
#__gust_comment_toolbar button{color:#e0cfba;border-radius:10px}
#__gust_comment_toolbar button:hover,#__gust_comment_toolbar button[aria-pressed=true]{background:#49301c;color:#fff7e9}
#__gust_comment_toolbar button:focus-visible{outline-color:#f9bb71}
#__gust_comment_toolbar [data-mode-title]{color:#fff7e9;border-color:#f9bb7155}
#__gust_comments{border-color:#f9bb7133}
#__gust_comments textarea,[data-editor] textarea{background:#352416;color:#fff7e9;border-color:#f9bb7155}
#__gust_comments .__gust_autosubmit{color:#e0cfba}
#__gust_comments .__gust_comments_header{color:#fff7e9}
#__gust_comments .__gust_comments_count{background:#49301c;color:#e0cfba}
#__gust_comments [data-submit]{border-color:#f9bb7180;background:#49301c;color:#ffca81}
#__gust_comments [data-submit]:hover{background:#704423;border-color:#f9bb71}
#__gust_comments .__gust_comment_item{background:#352416;border-color:#f9bb7133}
#__gust_comments .__gust_comment_item:hover{background:#49301c;border-color:#f9bb7180}
#__gust_comments .__gust_comment_item:focus-within{border-color:#f9bb71}
#__gust_comments .__gust_comment_row{color:#fff7e9}
#__gust_comments .__gust_comment_row:focus-visible,#__gust_comments .__gust_remove_draft:focus-visible{outline-color:#f9bb71}
#__gust_comments .__gust_comment_meta,#__gust_comments .__gust_remove_draft,#__gust_comments .__gust_comments_empty,#__gust_comments .__gust_done_summary{color:#e0cfba}
`+"`"+`;
    document.head.appendChild(style);
  }
  const widget = document.createElement("div");
  widget.id = "__gust_widget";
  gustWidget = widget;
  gustIcon = document.createElement("div");
  gustIcon.id = "__gust_icon";
  gustIcon.title = "Gust";
  gustIcon.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 256 256" fill="none" aria-hidden="true"><path d="M128,192c3.39,9.15,13.67,16,24,16a24,24,0,0,0,0-48H40" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/><path d="M96,64c3.39-9.15,13.67-16,24-16a24,24,0,0,1,0,48H24" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/><path d="M184,96c3.39-9.15,13.67-16,24-16a24,24,0,0,1,0,48H32" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="16"/></svg>';
  gustPanel = document.createElement("div");
  gustPanel.id = "__gust_panel";
  createToolbar();
  if (%t) { selecting=loadCommentMode(); createCommentUI(); startCommentRefresh(); }
  widget.appendChild(gustIcon);
  widget.appendChild(gustPanel);
  if (!commentUI) gustPanel.prepend(commentToolbar);
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
    if (msg.type === "boot") { if(bootID&&bootID!==msg.bootId)restartPending=true; bootID=msg.bootId; return; }
    if (msg.type === "debug") { setDebug(msg.enabled === true); return; }
    if (msg.type === "notice") { failing = true; applyIconState(); refreshInfo(); return; }
    if (msg.type === "error") { failing = true; applyIconState(); refreshInfo(); showError(msg.message); return; }
    if (msg.type === "ready") { failing = false; applyIconState(); hideError(); if (typeof msg.at === "number") reloadedAt = msg.at; if (typeof msg.version === "number" && msg.version > lastVersion) lastVersion = msg.version; if(selfDev&&restartPending){restartPending=false;location.reload();} return; }
    if (msg.type === "reload") {
      if(selfDev&&restartPending){restartPending=false;location.reload();return;}
      failing = false; applyIconState();
      hideError();
      if (typeof msg.at === "number") reloadedAt = msg.at;
      if (typeof msg.version === "number" && msg.version > lastVersion) {
        lastVersion = msg.version;
        location.reload();
      }
    }
  };
  socket.onclose = function(){ connected = false; connectionAttempted = true; applyIconState(); setTimeout(connect, retry); retry = Math.min(retry * 2, 5000); };
  socket.onerror = function(){ try { socket.close(); } catch (_) {} };
}
connect();
})();</script>`, version, appPort, selfDev, commentsOn)
}

func injectHTMLResponse(resp *http.Response, version, appPort int, log *logger.Logger, commentsEnabled ...bool) error {
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

	body = append(body, reloadScript(version, appPort, commentsEnabled...)...)
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
