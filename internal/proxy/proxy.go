package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/logger"
)

// Server is Gust's development HTTP proxy.
type Server struct {
	server   *http.Server
	listener net.Listener
	log      *logger.Logger
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

	s := &Server{listener: ln, log: log}
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

// Close stops the proxy server.
func (s *Server) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
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
	rp.ModifyResponse = injectHTMLResponse
	rp.FlushInterval = -1
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if s.log != nil {
			s.log.Verbosef("proxy request failed: %v", err)
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

const (
	reloadScript        = `<script id="__gust_reload">window.__gust_reload=window.__gust_reload||true;</script>`
	flushNoLengthHeader = "X-Gust-Flush-No-Length"
)

func injectHTMLResponse(resp *http.Response) error {
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

	body = append(body, reloadScript...)
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
