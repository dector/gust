package coordinator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/logger"
	"github.com/dector/gust/internal/process"
	"github.com/dector/gust/internal/watcher"
)

// TriggerSource identifies why a restart was requested.
type TriggerSource string

const (
	TriggerInitial TriggerSource = "initial"
	TriggerManual  TriggerSource = "manual"
	TriggerAgent   TriggerSource = "agent"
	TriggerFS      TriggerSource = "filesystem"
)

type internalState string

const (
	stateStopped      internalState = "stopped"
	stateStarting     internalState = "starting"
	stateWaitingReady internalState = "waiting_ready"
	stateRunning      internalState = "running"
	stateStopping     internalState = "stopping"
	stateShuttingDown internalState = "shutting_down"
)

// ExternalState is the state exposed to status callers.
type ExternalState string

const (
	ExternalRunning      ExternalState = "running"
	ExternalRestarting   ExternalState = "restarting"
	ExternalStopped      ExternalState = "stopped"
	ExternalShuttingDown ExternalState = "shutting_down"
)

// Status is a snapshot of coordinator-owned runtime state.
type Status struct {
	State        ExternalState
	PID          int
	AppPort      int
	ProxyPort    int
	Version      int
	BrowserReady bool
	BrowserError string
}

// BrowserState is the latest app readiness state intended for browser clients.
type BrowserState struct {
	Ready   bool
	Version int
	Error   string
}

// BrowserNotifier receives browser websocket state updates.
type BrowserNotifier interface {
	BrowserReady(version int)
	BrowserError(message string)
}

type childProcess interface {
	PID() int
	Done() <-chan struct{}
	ExitEvent() (process.ExitEvent, bool)
	Stop(context.Context) (process.ExitEvent, error)
}

type processRunner interface {
	Start(process.Options) (childProcess, error)
}

type defaultRunner struct{}

func (defaultRunner) Start(opts process.Options) (childProcess, error) {
	return process.Start(opts)
}

type triggerEvent struct {
	source TriggerSource
	reason string
}

type processExitedEvent struct {
	runID int
	event process.ExitEvent
}

type restartCompleteEvent struct {
	runID int
	proc  childProcess
	err   error
}

type debouncedFSTriggerEvent struct {
	seq int
}

type readinessCompleteEvent struct {
	runID int
	ready bool
	err   error
}

type statusRequest struct {
	reply chan Status
}

type shutdownRequested struct{}

var (
	readinessHealthTimeoutNS   atomic.Int64
	readinessHealthIntervalNS  atomic.Int64
	readinessStabilityWindowNS atomic.Int64
	fsDebounceDelayNS          atomic.Int64
)

func init() {
	readinessHealthTimeoutNS.Store(int64(config.HealthTimeout))
	readinessHealthIntervalNS.Store(int64(config.HealthInterval))
	readinessStabilityWindowNS.Store(int64(config.StabilityWindow))
	fsDebounceDelayNS.Store(int64(config.FSDebounce))
}

func readinessHealthTimeout() time.Duration {
	return time.Duration(readinessHealthTimeoutNS.Load())
}

func readinessHealthInterval() time.Duration {
	return time.Duration(readinessHealthIntervalNS.Load())
}

func readinessStabilityWindow() time.Duration {
	return time.Duration(readinessStabilityWindowNS.Load())
}

func fsDebounceDelay() time.Duration {
	return time.Duration(fsDebounceDelayNS.Load())
}

// ShutdownHooks are cleanup steps for resources owned outside the coordinator.
type ShutdownHooks struct {
	StopSocketAccepts func() error
	CloseProxy        func() error
	RestoreTerminal   func() error
	RemoveSocket      func() error
}

// Coordinator owns the Gust runtime.
type Coordinator struct {
	cfg config.Config
	log *logger.Logger

	runner          processRunner
	browserNotifier BrowserNotifier
	shutdownHooks   ShutdownHooks
	events          chan any
	done            chan struct{}
	doneOnce        sync.Once

	mu           sync.Mutex
	shuttingDown bool
}

// New constructs a Coordinator.
func New(cfg config.Config, log *logger.Logger) *Coordinator {
	return newWithRunner(cfg, log, defaultRunner{})
}

func newWithRunner(cfg config.Config, log *logger.Logger, runner processRunner) *Coordinator {
	return &Coordinator{
		cfg:    cfg,
		log:    log,
		runner: runner,
		events: make(chan any, 32),
		done:   make(chan struct{}),
	}
}

// SetBrowserNotifier configures browser websocket state updates.
func (c *Coordinator) SetBrowserNotifier(notifier BrowserNotifier) {
	c.browserNotifier = notifier
}

// SetShutdownHooks configures cleanup steps owned by outer packages.
func (c *Coordinator) SetShutdownHooks(hooks ShutdownHooks) {
	c.shutdownHooks = hooks
}

// Trigger queues a restart request from a producer such as keyboard or socket.
func (c *Coordinator) Trigger(source TriggerSource, reason string) bool {
	if c.isShuttingDown() {
		return false
	}
	return c.send(triggerEvent{source: source, reason: reason})
}

// Status returns the current externally-visible coordinator state.
func (c *Coordinator) Status(ctx context.Context) (Status, error) {
	if c.isShuttingDown() {
		return Status{}, errors.New("coordinator stopped")
	}
	reply := make(chan Status, 1)
	if !c.send(statusRequest{reply: reply}) {
		return Status{}, errors.New("coordinator stopped")
	}
	select {
	case status := <-reply:
		return status, nil
	case <-ctx.Done():
		return Status{}, ctx.Err()
	}
}

// Shutdown requests graceful coordinator shutdown.
func (c *Coordinator) Shutdown() bool {
	c.markShuttingDown()
	return c.send(shutdownRequested{})
}

// Run starts the coordinator loop.
func (c *Coordinator) Run(ctx context.Context) error {
	defer c.doneOnce.Do(func() { close(c.done) })
	state := stateStopped
	var proc childProcess
	var runID int
	var version int
	var pendingRerun bool
	var restartWorker bool
	var restartCancel context.CancelFunc
	var restartDone chan restartCompleteEvent
	var readinessCancel context.CancelFunc
	var fsDebounceTimer *time.Timer
	var fsDebouncePending bool
	var fsDebounceSeq int
	browser := BrowserState{}

	var watchCancel context.CancelFunc
	var fsWatcher *watcher.Watcher
	if c.cfg.Root != "" {
		watchCtx, cancel := context.WithCancel(ctx)
		watchCancel = cancel
		w, err := watcher.Start(watchCtx, c.cfg.Root, c.cfg.Excludes, c.log)
		if err != nil {
			cancel()
			return err
		}
		fsWatcher = w
		go c.forwardWatcherEvents(w)
	}

	stopWatcher := func() {
		if watchCancel != nil {
			watchCancel()
		}
		if fsWatcher != nil {
			_ = fsWatcher.Close()
		}
	}

	cancelFSDebounce := func() {
		fsDebouncePending = false
		fsDebounceSeq++
		if fsDebounceTimer != nil {
			if !fsDebounceTimer.Stop() {
				select {
				case <-fsDebounceTimer.C:
				default:
				}
			}
		}
	}

	startRestart := func(reason string) {
		if state == stateShuttingDown {
			return
		}
		if restartWorker {
			pendingRerun = true
			return
		}
		if c.log != nil {
			c.log.Verbosef("restart requested: %s", reason)
		}
		runID++
		restartRunID := runID
		oldProc := proc
		if oldProc == nil {
			state = stateStarting
		} else {
			state = stateStopping
		}
		restartWorker = true
		workerCtx, cancel := context.WithCancel(ctx)
		restartCancel = cancel
		done := make(chan restartCompleteEvent, 1)
		restartDone = done
		go func() {
			ev := c.restart(workerCtx, restartRunID, oldProc)
			done <- ev
			c.send(ev)
		}()
	}

	requestRestart := func(reason string) {
		if state == stateShuttingDown {
			return
		}
		if restartWorker || state == stateStarting || state == stateStopping {
			pendingRerun = true
			return
		}
		if state == stateWaitingReady && readinessCancel != nil {
			readinessCancel()
			readinessCancel = nil
		}
		startRestart(reason)
	}

	startRestart("initial")

	for {
		var fsDebounceC <-chan time.Time
		if fsDebounceTimer != nil && fsDebouncePending {
			fsDebounceC = fsDebounceTimer.C
		}
		select {
		case <-ctx.Done():
			state = stateShuttingDown
			c.runShutdown(cancelFSDebounce, stopWatcher, restartCancel, restartDone, readinessCancel, proc)
			return nil
		case <-fsDebounceC:
			fsDebouncePending = false
			c.send(debouncedFSTriggerEvent{seq: fsDebounceSeq})
		case raw := <-c.events:
			switch ev := raw.(type) {
			case triggerEvent:
				if state == stateShuttingDown {
					continue
				}
				if ev.source == TriggerFS {
					fsDebouncePending = true
					fsDebounceSeq++
					if fsDebounceTimer == nil {
						fsDebounceTimer = time.NewTimer(fsDebounceDelay())
					} else {
						if !fsDebounceTimer.Stop() {
							select {
							case <-fsDebounceTimer.C:
							default:
							}
						}
						fsDebounceTimer.Reset(fsDebounceDelay())
					}
					continue
				}
				if ev.source == TriggerManual || ev.source == TriggerAgent {
					cancelFSDebounce()
				}
				requestRestart(ev.reason)
			case debouncedFSTriggerEvent:
				if ev.seq == fsDebounceSeq {
					requestRestart("filesystem change")
				}
			case processExitedEvent:
				if ev.runID != runID || state == stateShuttingDown {
					continue
				}
				c.logProcessExit(ev.event)
				if readinessCancel != nil {
					readinessCancel()
					readinessCancel = nil
				}
				if proc != nil && ev.event.PID == proc.PID() {
					proc = nil
				}
				browser.Ready = false
				browser.Error = processExitBrowserError(ev.event)
				c.notifyBrowserError(browser.Error)
				if !restartWorker {
					state = stateStopped
				}
			case restartCompleteEvent:
				if ev.runID != runID || state == stateShuttingDown {
					if ev.proc != nil {
						_, _ = ev.proc.Stop(context.Background())
					}
					continue
				}
				restartWorker = false
				restartCancel = nil
				restartDone = nil
				if ev.err != nil {
					proc = nil
					state = stateStopped
					browser.Ready = false
					browser.Error = ev.err.Error()
					c.notifyBrowserError(browser.Error)
					if c.log != nil {
						c.log.Printf("restart failed: %v", ev.err)
					}
				} else {
					proc = ev.proc
					if c.log != nil {
						c.log.Printf("started process pid=%d", proc.PID())
					}
					state = stateWaitingReady
					browser.Ready = false
					browser.Error = ""
					c.watchProcess(runID, proc)
					readyCtx, cancel := context.WithCancel(ctx)
					readinessCancel = cancel
					go c.checkReadiness(readyCtx, runID, proc)
				}
				if pendingRerun && state != stateShuttingDown && state != stateWaitingReady {
					pendingRerun = false
					startRestart("pending")
				}
			case readinessCompleteEvent:
				if ev.runID != runID || state == stateShuttingDown || state != stateWaitingReady {
					continue
				}
				readinessCancel = nil
				if ev.ready {
					version++
					browser.Ready = true
					browser.Version = version
					browser.Error = ""
					c.notifyBrowserReady(version)
					state = stateRunning
				} else {
					browser.Ready = false
					browser.Error = ev.err.Error()
					c.notifyBrowserError(browser.Error)
					if c.log != nil {
						c.log.Printf("readiness failed: %v", ev.err)
					}
					select {
					case <-proc.Done():
						state = stateStopped
					default:
						state = stateRunning
					}
				}
				if pendingRerun && state != stateShuttingDown {
					pendingRerun = false
					startRestart("pending")
				}
			case statusRequest:
				ev.reply <- makeStatus(state, proc, c.cfg, version, browser)
			case shutdownRequested:
				state = stateShuttingDown
				c.runShutdown(cancelFSDebounce, stopWatcher, restartCancel, restartDone, readinessCancel, proc)
				return nil
			}
		}
	}
}

func (c *Coordinator) runShutdown(cancelFSDebounce, stopWatcher func(), restartCancel context.CancelFunc, restartDone chan restartCompleteEvent, readinessCancel context.CancelFunc, proc childProcess) {
	c.markShuttingDown()
	if c.shutdownHooks.StopSocketAccepts != nil {
		_ = c.shutdownHooks.StopSocketAccepts()
	}
	stopWatcher()
	cancelFSDebounce()
	if restartCancel != nil {
		restartCancel()
	}
	if readinessCancel != nil {
		readinessCancel()
	}
	if proc != nil {
		_, _ = proc.Stop(context.Background())
	}
	if restartDone != nil {
		ev := <-restartDone
		if ev.proc != nil && (proc == nil || ev.proc.PID() != proc.PID()) {
			_, _ = ev.proc.Stop(context.Background())
		}
	}
	if c.shutdownHooks.CloseProxy != nil {
		_ = c.shutdownHooks.CloseProxy()
	}
	if c.shutdownHooks.RestoreTerminal != nil {
		_ = c.shutdownHooks.RestoreTerminal()
	}
	if c.shutdownHooks.RemoveSocket != nil {
		_ = c.shutdownHooks.RemoveSocket()
	}
}

func (c *Coordinator) restart(ctx context.Context, runID int, oldProc childProcess) restartCompleteEvent {
	if oldProc != nil {
		if _, err := oldProc.Stop(ctx); err != nil {
			return restartCompleteEvent{runID: runID, err: err}
		}
	}
	select {
	case <-ctx.Done():
		return restartCompleteEvent{runID: runID, err: ctx.Err()}
	default:
	}
	proc, err := c.runner.Start(process.Options{
		Command:      c.cfg.Exec,
		Root:         c.cfg.Root,
		AppPort:      c.cfg.AppPort,
		HasAppPort:   c.cfg.HasAppPort,
		ProxyPort:    c.cfg.ProxyPort,
		ProxyEnabled: c.cfg.ProxyEnabled,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
	})
	return restartCompleteEvent{runID: runID, proc: proc, err: err}
}

func (c *Coordinator) watchProcess(runID int, proc childProcess) {
	if proc == nil {
		return
	}
	go func() {
		<-proc.Done()
		event, _ := proc.ExitEvent()
		c.send(processExitedEvent{runID: runID, event: event})
	}()
}

func (c *Coordinator) forwardWatcherEvents(w *watcher.Watcher) {
	for event := range w.Events() {
		c.Trigger(TriggerFS, event.Path)
	}
}

func (c *Coordinator) checkReadiness(ctx context.Context, runID int, proc childProcess) {
	var err error
	if c.cfg.HealthPath != "" {
		err = c.waitForHealth(ctx, proc, c.cfg.AppPort, c.cfg.HealthPath)
	} else {
		err = waitForStability(ctx, proc)
	}
	if err != nil {
		c.send(readinessCompleteEvent{runID: runID, err: err})
		return
	}
	c.send(readinessCompleteEvent{runID: runID, ready: true})
}

func waitForStability(ctx context.Context, proc childProcess) error {
	timer := time.NewTimer(readinessStabilityWindow())
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-proc.Done():
		return errors.New("process exited before readiness")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Coordinator) waitForHealth(ctx context.Context, proc childProcess, appPort int, healthPath string) error {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", appPort, healthPath)
	healthInterval := readinessHealthInterval()
	deadline := time.NewTimer(readinessHealthTimeout())
	defer deadline.Stop()
	ticker := time.NewTicker(healthInterval)
	defer ticker.Stop()
	client := &http.Client{Timeout: healthInterval}

	check := func() bool {
		resp, err := client.Get(url)
		if err != nil {
			if c.log != nil {
				c.log.Verbosef("health retry: GET %s failed: %v", url, err)
			}
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			if c.log != nil {
				c.log.Verbosef("health retry: GET %s returned %d", url, resp.StatusCode)
			}
			return false
		}
		return true
	}
	for {
		if check() {
			return nil
		}
		select {
		case <-proc.Done():
			return errors.New("process exited before health check succeeded")
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("health check timed out")
		case <-ticker.C:
		}
	}
}

func (c *Coordinator) logProcessExit(event process.ExitEvent) {
	if c.log == nil {
		return
	}
	if event.Err != nil {
		c.log.Printf("process exited pid=%d: %v", event.PID, event.Err)
		return
	}
	c.log.Printf("process exited pid=%d code=%d", event.PID, event.Code)
}

func processExitBrowserError(event process.ExitEvent) string {
	if event.Err != nil {
		return fmt.Sprintf("process exited: %v", event.Err)
	}
	if event.Code != 0 {
		return fmt.Sprintf("process exited with code %d", event.Code)
	}
	return "process exited"
}

func makeStatus(state internalState, proc childProcess, cfg config.Config, version int, browser BrowserState) Status {
	pid := 0
	if proc != nil {
		pid = proc.PID()
	}
	return Status{
		State:        externalState(state),
		PID:          pid,
		AppPort:      cfg.AppPort,
		ProxyPort:    cfg.ProxyPort,
		Version:      version,
		BrowserReady: browser.Ready,
		BrowserError: browser.Error,
	}
}

func externalState(state internalState) ExternalState {
	switch state {
	case stateRunning:
		return ExternalRunning
	case stateShuttingDown:
		return ExternalShuttingDown
	case stateStopped:
		return ExternalStopped
	default:
		return ExternalRestarting
	}
}

func (c *Coordinator) notifyBrowserReady(version int) {
	if c.browserNotifier != nil {
		c.browserNotifier.BrowserReady(version)
	}
}

func (c *Coordinator) notifyBrowserError(message string) {
	if c.browserNotifier != nil {
		c.browserNotifier.BrowserError(message)
	}
}

func (c *Coordinator) send(event any) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.events <- event:
		return true
	case <-c.done:
		return false
	default:
		go func() {
			select {
			case c.events <- event:
			case <-c.done:
			}
		}()
		return true
	}
}

func (c *Coordinator) isShuttingDown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shuttingDown
}

func (c *Coordinator) markShuttingDown() {
	c.mu.Lock()
	c.shuttingDown = true
	c.mu.Unlock()
}
