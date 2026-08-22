package coordinator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
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
	readinessHealthTimeout   = config.HealthTimeout
	readinessHealthInterval  = config.HealthInterval
	readinessStabilityWindow = config.StabilityWindow
	fsDebounceDelay          = config.FSDebounce
)

// Coordinator owns the Gust runtime.
type Coordinator struct {
	cfg config.Config
	log *logger.Logger

	runner processRunner
	events chan any

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
	}
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
	return c.send(shutdownRequested{})
}

// Run starts the coordinator loop.
func (c *Coordinator) Run(ctx context.Context) error {
	state := stateStopped
	var proc childProcess
	var runID int
	var version int
	var pendingRerun bool
	var restartWorker bool
	var restartCancel context.CancelFunc
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
		if state == stateShuttingDown || restartWorker {
			pendingRerun = true
			return
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
		go c.restart(workerCtx, restartRunID, oldProc)
		_ = reason
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
			c.markShuttingDown()
			cancelFSDebounce()
			stopWatcher()
			if restartCancel != nil {
				restartCancel()
			}
			if readinessCancel != nil {
				readinessCancel()
			}
			if proc != nil {
				_, _ = proc.Stop(context.Background())
			}
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
						fsDebounceTimer = time.NewTimer(fsDebounceDelay)
					} else {
						if !fsDebounceTimer.Stop() {
							select {
							case <-fsDebounceTimer.C:
							default:
							}
						}
						fsDebounceTimer.Reset(fsDebounceDelay)
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
				if readinessCancel != nil {
					readinessCancel()
					readinessCancel = nil
				}
				if proc != nil && ev.event.PID == proc.PID() {
					proc = nil
				}
				browser.Ready = false
				browser.Error = processExitBrowserError(ev.event)
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
				if ev.err != nil {
					proc = nil
					state = stateStopped
					if c.log != nil {
						c.log.Printf("restart failed: %v", ev.err)
					}
				} else {
					proc = ev.proc
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
					state = stateRunning
				} else {
					browser.Ready = false
					browser.Error = ev.err.Error()
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
				c.markShuttingDown()
				cancelFSDebounce()
				stopWatcher()
				if restartCancel != nil {
					restartCancel()
				}
				if readinessCancel != nil {
					readinessCancel()
				}
				if proc != nil {
					_, _ = proc.Stop(context.Background())
				}
				return nil
			}
		}
	}
}

func (c *Coordinator) restart(ctx context.Context, runID int, oldProc childProcess) {
	if oldProc != nil {
		if _, err := oldProc.Stop(ctx); err != nil {
			c.send(restartCompleteEvent{runID: runID, err: err})
			return
		}
	}
	select {
	case <-ctx.Done():
		c.send(restartCompleteEvent{runID: runID, err: ctx.Err()})
		return
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
	c.send(restartCompleteEvent{runID: runID, proc: proc, err: err})
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
		err = waitForHealth(ctx, proc, c.cfg.AppPort, c.cfg.HealthPath)
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
	timer := time.NewTimer(readinessStabilityWindow)
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

func waitForHealth(ctx context.Context, proc childProcess, appPort int, healthPath string) error {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", appPort, healthPath)
	deadline := time.NewTimer(readinessHealthTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(readinessHealthInterval)
	defer ticker.Stop()
	client := &http.Client{Timeout: readinessHealthInterval}

	check := func() bool {
		resp, err := client.Get(url)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode >= 200 && resp.StatusCode <= 299
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

func (c *Coordinator) send(event any) bool {
	select {
	case c.events <- event:
		return true
	default:
		go func() { c.events <- event }()
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
