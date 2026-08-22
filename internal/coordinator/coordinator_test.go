package coordinator

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dector/gust/internal/config"
	"github.com/dector/gust/internal/process"
)

type fakeRunner struct {
	mu      sync.Mutex
	starts  int
	started chan *fakeProcess
	blocks  []chan struct{}
	err     error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{started: make(chan *fakeProcess, 16)}
}

func (r *fakeRunner) Start(process.Options) (childProcess, error) {
	r.mu.Lock()
	r.starts++
	startNumber := r.starts
	var block chan struct{}
	if len(r.blocks) > 0 {
		block = r.blocks[0]
		r.blocks = r.blocks[1:]
	}
	err := r.err
	r.mu.Unlock()

	if block != nil {
		<-block
	}
	if err != nil {
		return nil, err
	}
	proc := newFakeProcess(startNumber)
	r.started <- proc
	return proc, nil
}

func (r *fakeRunner) blockNext() chan struct{} {
	ch := make(chan struct{})
	r.mu.Lock()
	r.blocks = append(r.blocks, ch)
	r.mu.Unlock()
	return ch
}

func (r *fakeRunner) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.starts
}

type fakeProcess struct {
	pid  int
	done chan struct{}
	once sync.Once
}

func newFakeProcess(pid int) *fakeProcess {
	return &fakeProcess{pid: pid, done: make(chan struct{})}
}

func (p *fakeProcess) PID() int { return p.pid }

func (p *fakeProcess) Done() <-chan struct{} { return p.done }

func (p *fakeProcess) ExitEvent() (process.ExitEvent, bool) {
	select {
	case <-p.done:
		return process.ExitEvent{PID: p.pid, Code: 0}, true
	default:
		return process.ExitEvent{}, false
	}
}

func (p *fakeProcess) Stop(context.Context) (process.ExitEvent, error) {
	p.once.Do(func() { close(p.done) })
	return process.ExitEvent{PID: p.pid, Code: 0}, nil
}

func TestCoordinatorInitialStartAndStatus(t *testing.T) {
	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test", AppPort: 8080, ProxyPort: 5000}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	proc := waitStarted(t, runner)
	status := waitStatus(t, coord, ExternalRunning)
	if status.PID != proc.PID() {
		t.Fatalf("PID = %d, want %d", status.PID, proc.PID())
	}
	if status.AppPort != 8080 || status.ProxyPort != 5000 {
		t.Fatalf("ports = %d:%d", status.AppPort, status.ProxyPort)
	}

	cancel()
	<-done
}

func TestCoordinatorReportsRestartingWhileStartInProgress(t *testing.T) {
	runner := newFakeRunner()
	block := runner.blockNext()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitFor(t, func() bool { return runner.startCount() == 1 })
	status := waitStatus(t, coord, ExternalRestarting)
	if status.PID != 0 {
		t.Fatalf("PID = %d, want 0", status.PID)
	}
	close(block)
	waitStarted(t, runner)

	cancel()
	<-done
}

func TestCoordinatorCoalescesTriggersDuringRestart(t *testing.T) {
	runner := newFakeRunner()
	block := runner.blockNext()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitFor(t, func() bool { return runner.startCount() == 1 })
	coord.Trigger(TriggerManual, "first")
	coord.Trigger(TriggerAgent, "second")
	close(block)
	first := waitStarted(t, runner)
	second := waitStarted(t, runner)

	if first.PID() == second.PID() {
		t.Fatalf("second start reused pid %d", first.PID())
	}
	if runner.startCount() != 2 {
		t.Fatalf("starts = %d, want 2", runner.startCount())
	}

	cancel()
	<-done
}

func TestCoordinatorIgnoresStaleProcessExit(t *testing.T) {
	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	proc := waitStarted(t, runner)
	coord.events <- processExitedEvent{runID: 0, event: process.ExitEvent{PID: proc.PID(), Code: 0}}
	status := waitStatus(t, coord, ExternalRunning)
	if status.PID != proc.PID() {
		t.Fatalf("PID = %d, want %d", status.PID, proc.PID())
	}

	cancel()
	<-done
}

func TestCoordinatorStartFailureLeavesStopped(t *testing.T) {
	runner := newFakeRunner()
	runner.err = errors.New("boom")
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitFor(t, func() bool { return runner.startCount() == 1 })
	status := waitStatus(t, coord, ExternalStopped)
	if status.PID != 0 {
		t.Fatalf("PID = %d, want 0", status.PID)
	}

	cancel()
	<-done
}

func TestCoordinatorHealthReadinessSuccessIncrementsVersion(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond)
	port, closeServer := startHealthServer(t, http.StatusNoContent)
	defer closeServer()

	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test", AppPort: port, HasAppPort: true, HealthPath: "/health"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	status := waitStatus(t, coord, ExternalRunning)
	if status.Version != 1 || !status.BrowserReady || status.BrowserError != "" {
		t.Fatalf("browser status = version %d ready %v error %q, want version 1 ready no error", status.Version, status.BrowserReady, status.BrowserError)
	}

	cancel()
	<-done
}

func TestCoordinatorHealthTimeoutDoesNotIncrementVersion(t *testing.T) {
	withReadinessTimings(t, 25*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond)
	port, closeServer := startHealthServer(t, http.StatusInternalServerError)
	defer closeServer()

	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test", AppPort: port, HasAppPort: true, HealthPath: "/health"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	status := waitStatus(t, coord, ExternalRunning)
	waitFor(t, func() bool {
		status = mustStatus(t, coord)
		return status.BrowserError == "health check timed out"
	})
	if status.Version != 0 || status.BrowserReady {
		t.Fatalf("browser status = version %d ready %v, want version 0 not ready", status.Version, status.BrowserReady)
	}

	cancel()
	<-done
}

func TestCoordinatorHealthStopsOnEarlyProcessExit(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond)
	port, closeServer := startHealthServer(t, http.StatusInternalServerError)
	defer closeServer()

	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test", AppPort: port, HasAppPort: true, HealthPath: "/health"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	proc := waitStarted(t, runner)
	_, _ = proc.Stop(context.Background())
	status := waitStatus(t, coord, ExternalStopped)
	if status.Version != 0 || status.BrowserReady {
		t.Fatalf("browser status = version %d ready %v, want version 0 not ready", status.Version, status.BrowserReady)
	}
	if status.BrowserError == "" {
		t.Fatal("BrowserError is empty, want process exit error")
	}

	cancel()
	<-done
}

func TestCoordinatorStabilityWindowReadiness(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, 30*time.Millisecond)
	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	status := waitStatus(t, coord, ExternalRestarting)
	if status.Version != 0 || status.BrowserReady {
		t.Fatalf("early browser status = version %d ready %v, want version 0 not ready", status.Version, status.BrowserReady)
	}
	status = waitStatus(t, coord, ExternalRunning)
	if status.Version != 1 || !status.BrowserReady {
		t.Fatalf("browser status = version %d ready %v, want version 1 ready", status.Version, status.BrowserReady)
	}

	cancel()
	<-done
}

func TestCoordinatorVersionIncrementsOnlyAfterReadinessSuccess(t *testing.T) {
	withReadinessTimings(t, 25*time.Millisecond, 5*time.Millisecond, 20*time.Millisecond)
	var statusCode atomic.Int32
	statusCode.Store(http.StatusInternalServerError)
	port, closeServer := startDynamicHealthServer(t, func() int { return int(statusCode.Load()) })
	defer closeServer()

	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test", AppPort: port, HasAppPort: true, HealthPath: "/health"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	waitFor(t, func() bool { return mustStatus(t, coord).BrowserError == "health check timed out" })
	if status := mustStatus(t, coord); status.Version != 0 {
		t.Fatalf("version after failed readiness = %d, want 0", status.Version)
	}

	statusCode.Store(http.StatusOK)
	coord.Trigger(TriggerManual, "retry")
	waitStarted(t, runner)
	status := waitStatus(t, coord, ExternalRunning)
	if status.Version != 1 || !status.BrowserReady {
		t.Fatalf("browser status = version %d ready %v, want version 1 ready", status.Version, status.BrowserReady)
	}

	cancel()
	<-done
}

func TestCoordinatorDebouncesFilesystemTriggers(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, time.Millisecond)
	withFSDebounce(t, 25*time.Millisecond)
	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	waitStatus(t, coord, ExternalRunning)
	coord.Trigger(TriggerFS, "one")
	time.Sleep(10 * time.Millisecond)
	coord.Trigger(TriggerFS, "two")
	time.Sleep(15 * time.Millisecond)
	if starts := runner.startCount(); starts != 1 {
		t.Fatalf("starts before debounce = %d, want 1", starts)
	}
	waitFor(t, func() bool { return runner.startCount() == 2 })

	cancel()
	<-done
}

func TestCoordinatorManualTriggerCancelsPendingFilesystemDebounce(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, time.Millisecond)
	withFSDebounce(t, 100*time.Millisecond)
	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	waitStatus(t, coord, ExternalRunning)
	coord.Trigger(TriggerFS, "fs")
	time.Sleep(10 * time.Millisecond)
	coord.Trigger(TriggerManual, "manual")
	waitFor(t, func() bool { return runner.startCount() == 2 })
	time.Sleep(120 * time.Millisecond)
	if starts := runner.startCount(); starts != 2 {
		t.Fatalf("starts after canceled debounce = %d, want 2", starts)
	}

	cancel()
	<-done
}

func TestCoordinatorRestartsFromWatcherEvent(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, time.Millisecond)
	withFSDebounce(t, 10*time.Millisecond)
	runner := newFakeRunner()
	root := t.TempDir()
	coord := newWithRunner(config.Config{Exec: "test", Root: root}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	waitStatus(t, coord, ExternalRunning)
	if err := os.WriteFile(filepath.Join(root, "changed.txt"), []byte("change"), 0o644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}
	waitFor(t, func() bool { return runner.startCount() == 2 })

	cancel()
	<-done
}

func TestCoordinatorTriggerDuringReadinessCancelsStaleReadiness(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, 100*time.Millisecond)
	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	waitStatus(t, coord, ExternalRestarting)
	coord.Trigger(TriggerAgent, "rerun")
	waitStarted(t, runner)
	status := waitStatus(t, coord, ExternalRunning)
	if status.Version != 1 {
		t.Fatalf("version = %d, want only latest readiness to increment once", status.Version)
	}

	cancel()
	<-done
}

func TestCoordinatorDebouncedFilesystemTriggerCoalescesDuringRestart(t *testing.T) {
	withReadinessTimings(t, 80*time.Millisecond, 5*time.Millisecond, time.Millisecond)
	withFSDebounce(t, 10*time.Millisecond)
	runner := newFakeRunner()
	coord := newWithRunner(config.Config{Exec: "test"}, nil, runner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCoordinator(t, coord, ctx)

	waitStarted(t, runner)
	waitStatus(t, coord, ExternalRunning)
	block := runner.blockNext()
	coord.Trigger(TriggerManual, "manual")
	waitFor(t, func() bool { return runner.startCount() == 2 })
	coord.Trigger(TriggerFS, "one")
	coord.Trigger(TriggerFS, "two")
	time.Sleep(25 * time.Millisecond)
	close(block)
	waitStarted(t, runner)
	waitStarted(t, runner)
	if starts := runner.startCount(); starts != 3 {
		t.Fatalf("starts = %d, want 3", starts)
	}

	cancel()
	<-done
}

func runCoordinator(t *testing.T, coord *Coordinator, ctx context.Context) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- coord.Run(ctx) }()
	return done
}

func waitStarted(t *testing.T, runner *fakeRunner) *fakeProcess {
	t.Helper()
	select {
	case proc := <-runner.started:
		return proc
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for process start")
		return nil
	}
}

func waitStatus(t *testing.T, coord *Coordinator, want ExternalState) Status {
	t.Helper()
	var last Status
	waitFor(t, func() bool {
		status, err := statusSnapshot(coord)
		if err != nil {
			return false
		}
		last = status
		return status.State == want
	})
	return last
}

func mustStatus(t *testing.T, coord *Coordinator) Status {
	t.Helper()
	status, err := statusSnapshot(coord)
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	return status
}

func statusSnapshot(coord *Coordinator) (Status, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return coord.Status(ctx)
}

func withReadinessTimings(t *testing.T, healthTimeout, healthInterval, stabilityWindow time.Duration) {
	t.Helper()
	oldHealthTimeout := readinessHealthTimeout
	oldHealthInterval := readinessHealthInterval
	oldStabilityWindow := readinessStabilityWindow
	readinessHealthTimeout = healthTimeout
	readinessHealthInterval = healthInterval
	readinessStabilityWindow = stabilityWindow
	t.Cleanup(func() {
		readinessHealthTimeout = oldHealthTimeout
		readinessHealthInterval = oldHealthInterval
		readinessStabilityWindow = oldStabilityWindow
	})
}

func withFSDebounce(t *testing.T, delay time.Duration) {
	t.Helper()
	oldDelay := fsDebounceDelay
	fsDebounceDelay = delay
	t.Cleanup(func() { fsDebounceDelay = oldDelay })
}

func startHealthServer(t *testing.T, code int) (int, func()) {
	t.Helper()
	return startDynamicHealthServer(t, func() int { return code })
}

func startDynamicHealthServer(t *testing.T, code func() int) (int, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(code())
	}))
	t.Cleanup(server.Close)
	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split server addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return port, server.Close
}

func waitFor(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition timed out")
}
