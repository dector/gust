package exposure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dector/gust/internal/logger"
	"github.com/dector/serv/pkg/tailscale"
)

type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *safeBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

type fakeSession struct {
	url  chan string
	done chan struct{}
	once sync.Once
}

func newFakeSession() *fakeSession {
	return &fakeSession{url: make(chan string, 1), done: make(chan struct{})}
}
func (s *fakeSession) URL() <-chan string { return s.url }
func (s *fakeSession) Wait() error        { <-s.done; return nil }
func (s *fakeSession) Close() error {
	s.once.Do(func() { close(s.done); close(s.url) })
	return nil
}

func await(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for exposure")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCandidateIsStableAndHigh(t *testing.T) {
	p := candidate("/work/app", 8080, 0)
	if p < firstPort || p >= 65536 || p != candidate("/work/app", 8080, 0) {
		t.Fatalf("unstable or invalid candidate: %d", p)
	}
	if p == candidate("/work/app", 8080, 1) {
		t.Fatal("collision probe reused candidate")
	}
}

func TestManagerReuseCollisionAndShutdown(t *testing.T) {
	var mu sync.Mutex
	var ports []int
	s := newFakeSession()
	var output safeBuffer
	m := newManager("/work/app", 8080, logger.New(&output, false), func(_ context.Context, cfg tailscale.Config) (session, error) {
		mu.Lock()
		ports = append(ports, cfg.HTTPSPort)
		mu.Unlock()
		if cfg.HTTPSPort == candidate("/work/app", 8080, 0) {
			return nil, fmt.Errorf("Tailscale HTTPS port %d is already configured", cfg.HTTPSPort)
		}
		if cfg.LocalAddr != "127.0.0.1:8080" || cfg.Action != tailscale.Default || cfg.AllowReplaceExisting {
			t.Errorf("unsafe config: %+v", cfg)
		}
		s.url <- "https://example.ts.net:50000"
		return s, nil
	})
	urlReady := m.StartReady()
	if got := <-urlReady; got != "https://example.ts.net:50000" {
		t.Fatalf("exposure URL = %q", got)
	}
	m.StartReady() // next app readiness must reuse the same foreground session
	mu.Lock()
	if len(ports) != 2 || ports[0] != candidate("/work/app", 8080, 0) || ports[1] != candidate("/work/app", 8080, 1) {
		t.Errorf("candidate ports: %v", ports)
	}
	mu.Unlock()
	m.Close()
	select {
	case <-s.done:
	default:
		t.Fatal("session not closed")
	}
	m.StartReady() // cannot reopen after shutdown
}

func TestManagerReportsFailureAndRetriesAfterReadiness(t *testing.T) {
	var output safeBuffer
	var mu sync.Mutex
	calls := 0
	s := newFakeSession()
	m := newManager("/work", 3000, logger.New(&output, false), func(_ context.Context, _ tailscale.Config) (session, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return nil, errors.New("tailscale missing")
		}
		s.url <- "https://example.ts.net"
		return s, nil
	})
	if got := <-m.StartReady(); got != "" {
		t.Fatalf("URL after failed start = %q, want empty", got)
	}
	await(t, func() bool { return strings.Contains(output.String(), "tailscale missing") })
	if got := <-m.StartReady(); got != "https://example.ts.net" {
		t.Fatalf("retry URL = %q", got)
	}
	m.Close()
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("starts = %d, want 2", calls)
	}
}

func TestManagerCloseDuringStartup(t *testing.T) {
	entered := make(chan struct{})
	m := newManager("/work", 3000, nil, func(ctx context.Context, _ tailscale.Config) (session, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	m.StartReady()
	<-entered
	m.Close()
}

func TestManagerNoticesUnexpectedExit(t *testing.T) {
	var output safeBuffer
	s := newFakeSession()
	m := newManager("/work", 3000, logger.New(&output, false), func(context.Context, tailscale.Config) (session, error) {
		s.url <- "https://example.ts.net"
		return s, nil
	})
	if got := <-m.StartReady(); got != "https://example.ts.net" {
		t.Fatalf("exposure URL = %q", got)
	}
	_ = s.Close()
	await(t, func() bool { return strings.Contains(output.String(), "stopped unexpectedly") })
	m.Close()
}
