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
	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/logger"
)

// Server is Gust's development HTTP proxy.
type Server struct {
	server    *http.Server
	listener  net.Listener
	log       *logger.Logger
	hub       *BrowserHub
	closeOnce sync.Once
}

// BrowserHub tracks browser websocket clients and their latest status.
type BrowserHub struct {
	mu      sync.Mutex
	clients map[*browserClient]struct{}
	latest  browserMessage
}

type browserClient struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

type browserMessage struct {
	Type    string `json:"type"`
	Version int    `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

// BrowserReady records a ready browser state and broadcasts reload to clients.
func (h *BrowserHub) BrowserReady(version int) {
	if h == nil {
		return
	}
	h.broadcast(browserMessage{Type: "reload", Version: version}, browserMessage{Type: "ready", Version: version})
}

// BrowserError records and broadcasts a browser error banner message.
func (h *BrowserHub) BrowserError(message string) {
	if h == nil || message == "" {
		return
	}
	msg := browserMessage{Type: "error", Message: message}
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

func (h *BrowserHub) add(conn *websocket.Conn) (*browserClient, browserMessage) {
	client := &browserClient{conn: conn}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client] = struct{}{}
	return client, h.latest
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

	s := &Server{listener: ln, log: log, hub: &BrowserHub{clients: map[*browserClient]struct{}{}}}
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
		return injectHTMLResponse(resp, s.hub.currentVersion())
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

func (s *Server) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		if s.log != nil {
			s.log.Verbosef("browser websocket accept failed: %v", err)
		}
		return
	}
	client, latest := s.hub.add(conn)
	defer func() {
		s.hub.remove(client)
		_ = conn.Close(websocket.StatusNormalClosure, "closed")
	}()
	if latest.Type != "" {
		data, _ := json.Marshal(latest)
		_ = client.write(data)
	}
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

func reloadScript(version int) string {
	return fmt.Sprintf(`<script id="__gust_reload">(function(){
let lastVersion = %d;
let retry = 250;
let socket;
function banner(){
  let el = document.getElementById("__gust_error");
  if (!el) {
    el = document.createElement("div");
    el.id = "__gust_error";
    el.style.cssText = "position:fixed;top:0;left:0;right:0;z-index:2147483647;background:#b00020;color:white;padding:8px 12px;font:14px sans-serif;text-align:left;white-space:pre-wrap";
    document.documentElement.appendChild(el);
  }
  return el;
}
function showError(message){ banner().textContent = message || "Gust error"; }
function hideError(){ const el = document.getElementById("__gust_error"); if (el) el.remove(); }
function connect(){
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const url = proto + "//" + location.host + "/__gust/ws";
  socket = new WebSocket(url);
  socket.onopen = function(){ retry = 250; };
  socket.onmessage = function(event){
    let msg;
    try { msg = JSON.parse(event.data); } catch (_) { return; }
    if (msg.type === "error") { showError(msg.message); return; }
    if (msg.type === "ready") { hideError(); if (typeof msg.version === "number" && msg.version > lastVersion) lastVersion = msg.version; return; }
    if (msg.type === "reload") {
      hideError();
      if (typeof msg.version === "number" && msg.version > lastVersion) {
        lastVersion = msg.version;
        location.reload();
      }
    }
  };
  socket.onclose = function(){ setTimeout(connect, retry); retry = Math.min(retry * 2, 5000); };
  socket.onerror = function(){ try { socket.close(); } catch (_) {} };
}
connect();
})();</script>`, version)
}

func injectHTMLResponse(resp *http.Response, version int) error {
	if !shouldInjectHTML(resp) {
		return nil
	}
	hadContentLength := resp.Header.Get("Content-Length") != ""

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	if bytes.Contains(body, []byte("__gust_reload")) {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if hadContentLength {
			resp.ContentLength = int64(len(body))
		} else {
			resp.ContentLength = -1
		}
		return nil
	}

	body = append(body, reloadScript(version)...)
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
	if resp == nil || resp.Request == nil {
		return false
	}
	if resp.Request.Method == http.MethodHead || resp.Request.Header.Get("Range") != "" {
		return false
	}
	if resp.StatusCode == http.StatusPartialContent {
		return false
	}
	contentType := resp.Header.Get("Content-Type")
	lowerContentType := strings.ToLower(contentType)
	if !strings.Contains(lowerContentType, "text/html") || strings.Contains(lowerContentType, "application/xhtml+xml") {
		return false
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Disposition")), "attachment") {
		return false
	}
	encoding := strings.TrimSpace(strings.ToLower(resp.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" {
		return false
	}
	return true
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
