package coordinator

import (
	"context"
	"errors"
	"sync"
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		status, err := coord.Status(ctx)
		if err != nil {
			return false
		}
		last = status
		return status.State == want
	})
	return last
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
