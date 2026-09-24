// Package exposure owns Gust's optional foreground Tailscale Serve session.
package exposure

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/dector/gust/internal/logger"
	"github.com/dector/serv/pkg/tailscale"
)

const (
	firstPort   = 49152
	portCount   = 65536 - firstPort
	maxAttempts = 64
)

type session interface {
	URL() <-chan string
	Wait() error
	Close() error
}

type starter func(context.Context, tailscale.Config) (session, error)

// Manager keeps the same session across app restarts and publishes the URL
// when Serve reports it.
type Manager struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	running    bool
	root       string
	appPort    int
	log        *logger.Logger
	start      starter
	startupURL chan string
}

// New creates an exposure manager. StartReady begins exposure asynchronously.
func New(root string, appPort int, log *logger.Logger) *Manager {
	return newManager(root, appPort, log, func(ctx context.Context, cfg tailscale.Config) (session, error) {
		return tailscale.Start(ctx, cfg)
	})
}

func newManager(root string, appPort int, log *logger.Logger, start starter) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{ctx: ctx, cancel: cancel, root: root, appPort: appPort, log: log, start: start}
}

// StartReady starts exposure asynchronously once. A running exposure is reused.
func (m *Manager) StartReady() <-chan string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running || m.ctx.Err() != nil {
		return m.startupURL
	}
	m.startupURL = make(chan string, 1)
	if m.ctx.Err() != nil {
		close(m.startupURL)
		return m.startupURL
	}
	m.running = true
	m.wg.Add(1)
	go m.run(m.startupURL)
	return m.startupURL
}

// Close cancels startup or stops and reaps the foreground Serve process.
func (m *Manager) Close() {
	m.mu.Lock()
	m.cancel()
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) run(startupURL chan string) {
	defer m.wg.Done()
	defer close(startupURL)
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	s, port, err := m.open()
	if err != nil {
		if m.ctx.Err() == nil {
			m.log.Warnf("Tailscale exposure failed: %v", err)
		}
		return
	}
	// If cancellation happened just as Start returned, still reap the process.
	if m.ctx.Err() != nil {
		_ = s.Close()
		return
	}
	m.log.Printf("Tailscale Serve starting on HTTPS port %d", port)
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = s.Wait(); close(done) }()
	// URL closes on process exit even if Serve never printed a URL.
	select {
	case url, ok := <-s.URL():
		if ok && m.ctx.Err() == nil {
			startupURL <- url
		}
	case <-m.ctx.Done():
	case <-done:
	}
	select {
	case <-m.ctx.Done():
		_ = s.Close()
	case <-done:
	}
	<-done
	if m.ctx.Err() == nil {
		if waitErr == nil {
			m.log.Warnf("Tailscale exposure stopped unexpectedly")
		} else {
			m.log.Warnf("Tailscale exposure stopped: %v", waitErr)
		}
	}
}

func (m *Manager) open() (session, int, error) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(m.appPort))
	for i := 0; i < maxAttempts; i++ {
		if err := m.ctx.Err(); err != nil {
			return nil, 0, err
		}
		port := candidate(m.root, i)
		cfg := tailscale.Config{LocalAddr: addr, HTTPSPort: port}
		s, err := m.start(m.ctx, cfg) // CheckAndStart checks existing Serve config before --yes.
		if err == nil {
			return s, port, nil
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("Tailscale HTTPS port %d is already configured", port)) {
			return nil, 0, err
		}
	}
	return nil, 0, errors.New("no free Tailscale HTTPS port among 64 deterministic candidates")
}

func candidate(root string, offset int) int {
	sum := sha256.Sum256([]byte(root))
	return firstPort + (int(binary.BigEndian.Uint32(sum[:4])%uint32(portCount))+offset)%portCount
}
