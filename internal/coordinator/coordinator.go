package coordinator

import (
	"context"
	"errors"
	"fmt"
	"io"
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

	// AutoReloadPaused reports whether filesystem auto-reload is paused.
	AutoReloadPaused bool

	// Process is the status exposed by the proxy's /__gust/status endpoint.
	Process ProcessStatus
}

// ProcessStatus is a snapshot of the proxied application process.
type ProcessStatus struct {
	Running   bool       `json:"running"`
	StartedAt *time.Time `json:"startedAt"`
	UptimeMS  *int64     `json:"uptimeMs"`
	LastExit  *LastExit  `json:"lastExit"`
}

// LastExit describes the most recent application process exit.
type LastExit struct {
	Code     int          `json:"code"`
	At       time.Time    `json:"at"`
	PassedMS int64        `json:"passedMs"`
	Error    bool         `json:"error"`
	Phase    string       `json:"phase,omitempty"`
	Command  string       `json:"command,omitempty"`
	Logs     *ProcessLogs `json:"logs"`
}

// ProcessLogs contains output produced by a failed application process.
type ProcessLogs struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// Logs is captured output from the last failed application exit or task.
type Logs struct {
	Code    int
	At      time.Time
	Phase   string
	Command string
	Stdout  string
	Stderr  string
}

// ErrNoLogs is returned when no failure output has been captured.
var ErrNoLogs = errors.New("no failure logs")

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
	runID  int
	proc   childProcess
	output *processOutput
	err    error
}

type debouncedFSTriggerEvent struct {
	seq int
}

type autoReloadToggleEvent struct{}

type infoToggleEvent struct{}

type readinessCompleteEvent struct {
	runID int
	ready bool
	err   error
}

type statusRequest struct {
	reply chan Status
}

type setAutoReloadRequest struct {
	paused bool
	reply  chan bool
}

type logsResult struct {
	logs    Logs
	present bool
}

type logsRequest struct {
	reply chan logsResult
}

type processOutput struct {
	stdout *limitedBuffer
	stderr *limitedBuffer
}

// taskFailure describes a failed before/after command.
type taskFailure struct {
	phase   string
	command string
	code    int
	err     error
	output  *processOutput
}

// beforeCompleteEvent reports the outcome of the before task list.
type beforeCompleteEvent struct {
	reason   string
	failure  *taskFailure
	canceled bool
}

// afterCompleteEvent reports the outcome of the after task list.
type afterCompleteEvent struct {
	runID    int
	failure  *taskFailure
	canceled bool
}

// commandRunner runs a one-shot task command to completion.
type commandRunner interface {
	Run(context.Context, process.Options) (process.ExitEvent, error)
}

type defaultCommandRunner struct{}

func (defaultCommandRunner) Run(ctx context.Context, opts process.Options) (process.ExitEvent, error) {
	return process.Run(ctx, opts)
}

const maxCapturedOutput = 64 * 1024

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
	commands        commandRunner
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
		cfg:      cfg,
		log:      log,
		runner:   runner,
		commands: defaultCommandRunner{},
		events:   make(chan any, 32),
		done:     make(chan struct{}),
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

// ToggleAutoReload pauses or resumes restarts caused by filesystem watcher events.
func (c *Coordinator) ToggleAutoReload() bool {
	if c.isShuttingDown() {
		return false
	}
	return c.send(autoReloadToggleEvent{})
}

// ToggleInfo enables or disables informational logs about filesystem reloads.
func (c *Coordinator) ToggleInfo() bool {
	if c.isShuttingDown() {
		return false
	}
	return c.send(infoToggleEvent{})
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

// SetAutoReload pauses or resumes filesystem-triggered restarts. It returns the
// resulting paused state once the change has been applied.
func (c *Coordinator) SetAutoReload(ctx context.Context, paused bool) (bool, error) {
	if c.isShuttingDown() {
		return false, errors.New("coordinator stopped")
	}
	reply := make(chan bool, 1)
	if !c.send(setAutoReloadRequest{paused: paused, reply: reply}) {
		return false, errors.New("coordinator stopped")
	}
	select {
	case state := <-reply:
		return state, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Logs returns captured output from the last failed application exit.
func (c *Coordinator) Logs(ctx context.Context) (Logs, error) {
	if c.isShuttingDown() {
		return Logs{}, errors.New("coordinator stopped")
	}
	reply := make(chan logsResult, 1)
	if !c.send(logsRequest{reply: reply}) {
		return Logs{}, errors.New("coordinator stopped")
	}
	select {
	case result := <-reply:
		if !result.present {
			return Logs{}, ErrNoLogs
		}
		return result.logs, nil
	case <-ctx.Done():
		return Logs{}, ctx.Err()
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
	var procStartedAt time.Time
	var procOutput *processOutput
	var lastExit *LastExit
	var runID int
	var version int
	var pendingRerun bool
	var restartWorker bool
	var restartCancel context.CancelFunc
	var restartDone chan restartCompleteEvent
	var readinessCancel context.CancelFunc
	var beforeWorker bool
	var beforeCancel context.CancelFunc
	var beforeDone chan struct{}
	var afterWorker bool
	var afterCancel context.CancelFunc
	var afterDone chan struct{}
	var fsSuppressRun bool
	var pendingTaskError string
	var fsDebounceTimer *time.Timer
	var fsDebouncePending bool
	var fsDebounceSeq int
	var fsTriggerReason string
	var autoReloadPaused bool
	var autoReloadMissedFS bool
	infoEnabled := c.cfg.Info
	browser := BrowserState{}

	var watchCancel context.CancelFunc
	var fsWatcher *watcher.Watcher
	if c.cfg.Root != "" {
		watchCtx, cancel := context.WithCancel(ctx)
		watchCancel = cancel
		w, err := watcher.Start(watchCtx, c.cfg.Root, c.cfg.Excludes, c.cfg.ExcludeGlobs, c.log)
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

	cancelAfter := func() {
		afterWorker = false
		if afterCancel != nil {
			afterCancel()
			afterCancel = nil
		}
	}

	startAfterTasks := func(taskRunID int) {
		if len(c.cfg.After) == 0 {
			return
		}
		afterWorker = true
		workerCtx, cancel := context.WithCancel(ctx)
		afterCancel = cancel
		done := make(chan struct{})
		afterDone = done
		go func() {
			defer close(done)
			failure, canceled := c.runCommands(workerCtx, "after", c.cfg.After)
			c.send(afterCompleteEvent{runID: taskRunID, failure: failure, canceled: canceled})
		}()
	}

	startRestart := func(reason string) {
		if state == stateShuttingDown {
			return
		}
		if restartWorker {
			pendingRerun = true
			return
		}
		cancelAfter()
		pendingTaskError = ""
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

	beginRerun := func(reason string) {
		if state == stateShuttingDown {
			return
		}
		if len(c.cfg.Before) > 0 {
			fsSuppressRun = true
			beforeWorker = true
			state = stateStopping
			workerCtx, cancel := context.WithCancel(ctx)
			beforeCancel = cancel
			done := make(chan struct{})
			beforeDone = done
			go func() {
				defer close(done)
				failure, canceled := c.runCommands(workerCtx, "before", c.cfg.Before)
				c.send(beforeCompleteEvent{reason: reason, failure: failure, canceled: canceled})
			}()
			return
		}
		startRestart(reason)
	}

	requestRestart := func(reason string) {
		if state == stateShuttingDown {
			return
		}
		if restartWorker || beforeWorker || state == stateStarting || state == stateStopping {
			pendingRerun = true
			return
		}
		if state == stateWaitingReady && readinessCancel != nil {
			readinessCancel()
			readinessCancel = nil
		}
		beginRerun(reason)
	}

	applyAutoReload := func(paused bool) {
		if paused == autoReloadPaused {
			return
		}
		autoReloadPaused = paused
		if autoReloadPaused {
			if fsDebouncePending {
				autoReloadMissedFS = true
			}
			cancelFSDebounce()
			if c.log != nil {
				c.log.Printf("\x1b[3mAuto-reload paused\x1b[23m")
			}
			return
		}
		if c.log != nil {
			c.log.Printf("\x1b[3mAuto-reload resumed\x1b[23m")
		}
		if autoReloadMissedFS {
			autoReloadMissedFS = false
			if infoEnabled && c.log != nil {
				c.log.Printf("reload triggered by: %s", fsTriggerReason)
			}
			requestRestart("filesystem change")
		}
	}

	beginRerun("initial")

	for {
		var fsDebounceC <-chan time.Time
		if fsDebounceTimer != nil && fsDebouncePending {
			fsDebounceC = fsDebounceTimer.C
		}
		select {
		case <-ctx.Done():
			state = stateShuttingDown
			c.runShutdown(cancelFSDebounce, stopWatcher, restartCancel, restartDone, readinessCancel, proc, beforeCancel, afterCancel, beforeDone, afterDone)
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
					if fsSuppressRun || beforeWorker || afterWorker {
						continue
					}
					fsTriggerReason = ev.reason
					if autoReloadPaused {
						autoReloadMissedFS = true
						continue
					}
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
					autoReloadMissedFS = false
				}
				requestRestart(ev.reason)
			case debouncedFSTriggerEvent:
				if fsSuppressRun || beforeWorker || afterWorker {
					continue
				}
				if !autoReloadPaused && ev.seq == fsDebounceSeq {
					if infoEnabled && c.log != nil {
						c.log.Printf("reload triggered by: %s", fsTriggerReason)
					}
					requestRestart("filesystem change")
				}
			case beforeCompleteEvent:
				beforeWorker = false
				beforeCancel = nil
				if state == stateShuttingDown || ev.canceled {
					continue
				}
				if ev.failure != nil {
					fsSuppressRun = false
					pendingRerun = false
					lastExit = makeTaskLastExit(ev.failure)
					browser.Ready = false
					browser.Error = taskBrowserError(ev.failure)
					c.notifyBrowserError(browser.Error)
					if c.log != nil {
						c.log.Printf("%s task failed: %s (exit %d)", ev.failure.phase, ev.failure.command, ev.failure.code)
					}
					if proc == nil {
						state = stateStopped
					} else {
						state = stateRunning
					}
					continue
				}
				startRestart(ev.reason)
			case afterCompleteEvent:
				afterWorker = false
				afterCancel = nil
				if ev.runID != runID || state == stateShuttingDown || ev.canceled {
					continue
				}
				if ev.failure != nil {
					lastExit = makeTaskLastExit(ev.failure)
					pendingTaskError = taskBrowserError(ev.failure)
					browser.Ready = false
					browser.Error = pendingTaskError
					c.notifyBrowserError(pendingTaskError)
					if c.log != nil {
						c.log.Printf("%s task failed: %s (exit %d)", ev.failure.phase, ev.failure.command, ev.failure.code)
					}
				}
			case autoReloadToggleEvent:
				applyAutoReload(!autoReloadPaused)
			case infoToggleEvent:
				infoEnabled = !infoEnabled
				if c.log != nil {
					if infoEnabled {
						c.log.Printf("info logs enabled")
					} else {
						c.log.Printf("info logs disabled")
					}
				}
			case processExitedEvent:
				if ev.runID != runID || state == stateShuttingDown {
					continue
				}
				c.logProcessExit(ev.event)
				lastExit = makeLastExit(ev.event, procOutput)
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
					fsSuppressRun = false
					browser.Ready = false
					browser.Error = ev.err.Error()
					c.notifyBrowserError(browser.Error)
					if c.log != nil {
						c.log.Printf("restart failed: %v", ev.err)
					}
				} else {
					proc = ev.proc
					procStartedAt = time.Now()
					procOutput = ev.output
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
					if c.cfg.HealthPath == "" {
						startAfterTasks(runID)
					}
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
				fsSuppressRun = false
				if ev.ready {
					version++
					browser.Ready = true
					browser.Version = version
					browser.Error = ""
					c.notifyBrowserReady(version)
					if pendingTaskError != "" {
						c.notifyBrowserError(pendingTaskError)
					}
					state = stateRunning
					if c.cfg.HealthPath != "" {
						startAfterTasks(ev.runID)
					}
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
			case setAutoReloadRequest:
				applyAutoReload(ev.paused)
				ev.reply <- autoReloadPaused
			case logsRequest:
				if lastExit != nil && lastExit.Logs != nil {
					ev.reply <- logsResult{present: true, logs: Logs{
						Code:    lastExit.Code,
						At:      lastExit.At,
						Phase:   lastExit.Phase,
						Command: lastExit.Command,
						Stdout:  lastExit.Logs.Stdout,
						Stderr:  lastExit.Logs.Stderr,
					}}
				} else {
					ev.reply <- logsResult{}
				}
			case statusRequest:
				ev.reply <- makeStatus(state, proc, procStartedAt, lastExit, c.cfg, version, browser, autoReloadPaused)
			case shutdownRequested:
				state = stateShuttingDown
				c.runShutdown(cancelFSDebounce, stopWatcher, restartCancel, restartDone, readinessCancel, proc, beforeCancel, afterCancel, beforeDone, afterDone)
				return nil
			}
		}
	}
}

func (c *Coordinator) runShutdown(cancelFSDebounce, stopWatcher func(), restartCancel context.CancelFunc, restartDone chan restartCompleteEvent, readinessCancel context.CancelFunc, proc childProcess, beforeCancel, afterCancel context.CancelFunc, beforeDone, afterDone chan struct{}) {
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
	if beforeCancel != nil {
		beforeCancel()
	}
	if afterCancel != nil {
		afterCancel()
	}
	if beforeDone != nil {
		<-beforeDone
	}
	if afterDone != nil {
		<-afterDone
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
	output := &processOutput{
		stdout: &limitedBuffer{limit: maxCapturedOutput},
		stderr: &limitedBuffer{limit: maxCapturedOutput},
	}
	proc, err := c.runner.Start(process.Options{
		Command:    c.cfg.Exec,
		Root:       c.cfg.Root,
		AppPort:    c.cfg.AppPort,
		HasAppPort: c.cfg.HasAppPort,
		ProxyPort:  c.cfg.ProxyPort,
		Stdout:     io.MultiWriter(os.Stdout, output.stdout),
		Stderr:     io.MultiWriter(os.Stderr, output.stderr),
	})
	return restartCompleteEvent{runID: runID, proc: proc, output: output, err: err}
}

// runCommands runs a task list sequentially, streaming output. It returns the
// first failure, or canceled=true when the context was canceled.
func (c *Coordinator) runCommands(ctx context.Context, phase string, commands []string) (*taskFailure, bool) {
	for _, command := range commands {
		if c.log != nil {
			c.log.Verbosef("%s: %s", phase, command)
		}
		output := &processOutput{
			stdout: &limitedBuffer{limit: maxCapturedOutput},
			stderr: &limitedBuffer{limit: maxCapturedOutput},
		}
		event, runErr := c.commands.Run(ctx, process.Options{
			Command:    command,
			Root:       c.cfg.Root,
			AppPort:    c.cfg.AppPort,
			HasAppPort: c.cfg.HasAppPort,
			ProxyPort:  c.cfg.ProxyPort,
			Stdout:     io.MultiWriter(os.Stdout, output.stdout),
			Stderr:     io.MultiWriter(os.Stderr, output.stderr),
		})
		if runErr != nil && ctx.Err() != nil {
			return nil, true
		}
		if runErr != nil {
			return &taskFailure{phase: phase, command: command, code: -1, err: runErr, output: output}, false
		}
		if event.Code != 0 {
			return &taskFailure{phase: phase, command: command, code: event.Code, err: event.Err, output: output}, false
		}
	}
	return nil, false
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

func makeStatus(state internalState, proc childProcess, startedAt time.Time, lastExit *LastExit, cfg config.Config, version int, browser BrowserState, autoReloadPaused bool) Status {
	pid := 0
	process := ProcessStatus{LastExit: lastExit}
	if proc != nil {
		pid = proc.PID()
		process.Running = true
		started := startedAt.UTC()
		uptime := time.Since(startedAt).Milliseconds()
		process.StartedAt = &started
		process.UptimeMS = &uptime
	}
	if process.LastExit != nil {
		exit := *process.LastExit
		exit.PassedMS = time.Since(exit.At).Milliseconds()
		process.LastExit = &exit
	}
	return Status{
		State:            externalState(state),
		PID:              pid,
		AppPort:          cfg.AppPort,
		ProxyPort:        cfg.ProxyPort,
		Version:          version,
		BrowserReady:     browser.Ready,
		BrowserError:     browser.Error,
		AutoReloadPaused: autoReloadPaused,
		Process:          process,
	}
}

func makeLastExit(event process.ExitEvent, output *processOutput) *LastExit {
	exit := &LastExit{Code: event.Code, At: time.Now().UTC(), Error: event.Err != nil || event.Code != 0}
	if exit.Error && output != nil {
		exit.Logs = &ProcessLogs{Stdout: output.stdout.String(), Stderr: output.stderr.String()}
	}
	return exit
}

func makeTaskLastExit(failure *taskFailure) *LastExit {
	exit := &LastExit{
		Code:    failure.code,
		At:      time.Now().UTC(),
		Error:   true,
		Phase:   failure.phase,
		Command: failure.command,
	}
	if failure.output != nil {
		exit.Logs = &ProcessLogs{Stdout: failure.output.stdout.String(), Stderr: failure.output.stderr.String()}
	}
	return exit
}

func taskBrowserError(failure *taskFailure) string {
	return fmt.Sprintf("%s failed: %s (exit %d)", failure.phase, failure.command, failure.code)
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
