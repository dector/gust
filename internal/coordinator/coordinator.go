package coordinator

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/logger"
	"github.com/dector/gust/internal/process"
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
	State     ExternalState
	PID       int
	AppPort   int
	ProxyPort int
	Version   int
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

type statusRequest struct {
	reply chan Status
}

type shutdownRequested struct{}

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

	startRestart("initial")

	for {
		select {
		case <-ctx.Done():
			state = stateShuttingDown
			c.markShuttingDown()
			if restartCancel != nil {
				restartCancel()
			}
			if proc != nil {
				_, _ = proc.Stop(context.Background())
			}
			return nil
		case raw := <-c.events:
			switch ev := raw.(type) {
			case triggerEvent:
				if state == stateShuttingDown {
					continue
				}
				if restartWorker || state == stateStarting || state == stateStopping || state == stateWaitingReady {
					pendingRerun = true
					continue
				}
				startRestart(ev.reason)
			case processExitedEvent:
				if ev.runID != runID || state == stateShuttingDown {
					continue
				}
				if proc != nil && ev.event.PID == proc.PID() {
					proc = nil
				}
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
					state = stateRunning
					c.watchProcess(runID, proc)
				}
				if pendingRerun && state != stateShuttingDown {
					pendingRerun = false
					startRestart("pending")
				}
			case statusRequest:
				ev.reply <- makeStatus(state, proc, c.cfg, version)
			case shutdownRequested:
				state = stateShuttingDown
				c.markShuttingDown()
				if restartCancel != nil {
					restartCancel()
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

func makeStatus(state internalState, proc childProcess, cfg config.Config, version int) Status {
	pid := 0
	if proc != nil {
		pid = proc.PID()
	}
	return Status{
		State:     externalState(state),
		PID:       pid,
		AppPort:   cfg.AppPort,
		ProxyPort: cfg.ProxyPort,
		Version:   version,
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
