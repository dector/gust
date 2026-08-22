package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/dector/gust/internal/config"
)

// Options configures a child process start.
type Options struct {
	Command string
	Root    string

	AppPort      int
	HasAppPort   bool
	ProxyPort    int
	ProxyEnabled bool

	Stdout io.Writer
	Stderr io.Writer
}

// ExitEvent describes a reaped child process.
type ExitEvent struct {
	PID  int
	Code int
	Err  error
}

// Process is a running Gust child command.
type Process struct {
	cmd *exec.Cmd
	pid int

	done chan struct{}

	mu    sync.Mutex
	event ExitEvent
}

// Start runs opts.Command through /bin/sh -c in its own process group.
func Start(opts Options) (*Process, error) {
	if opts.Command == "" {
		return nil, errors.New("empty command")
	}

	cmd := exec.Command("/bin/sh", "-c", opts.Command)
	if opts.Root != "" {
		cmd.Dir = opts.Root
	}
	cmd.Env = gustEnv(opts)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	p := &Process{
		cmd:  cmd,
		pid:  cmd.Process.Pid,
		done: make(chan struct{}),
	}
	go p.wait()
	return p, nil
}

// PID returns the shell process id. It is also the process group id.
func (p *Process) PID() int {
	if p == nil {
		return 0
	}
	return p.pid
}

// Done is closed after the child has exited and been reaped.
func (p *Process) Done() <-chan struct{} {
	if p == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return p.done
}

// ExitEvent returns the process exit event after Done is closed.
func (p *Process) ExitEvent() (ExitEvent, bool) {
	if p == nil {
		return ExitEvent{}, false
	}
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.event, true
	default:
		return ExitEvent{}, false
	}
}

// Wait waits until the process exits and is reaped or ctx is canceled.
func (p *Process) Wait(ctx context.Context) (ExitEvent, error) {
	if p == nil {
		return ExitEvent{}, errors.New("nil process")
	}
	select {
	case <-p.done:
		event, _ := p.ExitEvent()
		return event, nil
	case <-ctx.Done():
		return ExitEvent{}, ctx.Err()
	}
}

// Stop terminates the process group, waits up to the configured shutdown
// timeout, then kills the process group and waits until the child is reaped.
func (p *Process) Stop(ctx context.Context) (ExitEvent, error) {
	if p == nil {
		return ExitEvent{}, errors.New("nil process")
	}

	select {
	case <-p.done:
		event, _ := p.ExitEvent()
		return event, nil
	default:
	}

	termErr := signalGroup(p.pid, syscall.SIGTERM)

	timer := time.NewTimer(config.ShutdownTimeout)
	defer timer.Stop()

	select {
	case <-p.done:
		event, _ := p.ExitEvent()
		return event, termErr
	case <-timer.C:
		if err := signalGroup(p.pid, syscall.SIGKILL); err != nil && termErr == nil {
			termErr = err
		}
	case <-ctx.Done():
		return ExitEvent{}, ctx.Err()
	}

	select {
	case <-p.done:
		event, _ := p.ExitEvent()
		return event, termErr
	case <-ctx.Done():
		return ExitEvent{}, ctx.Err()
	}
}

func (p *Process) wait() {
	err := p.cmd.Wait()
	event := ExitEvent{PID: p.pid, Code: p.cmd.ProcessState.ExitCode(), Err: err}

	p.mu.Lock()
	p.event = event
	p.mu.Unlock()
	close(p.done)
}

func gustEnv(opts Options) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env, "GUST=1")
	if opts.HasAppPort {
		env = append(env, fmt.Sprintf("GUST_APP_PORT=%d", opts.AppPort))
	}
	if opts.ProxyEnabled || opts.ProxyPort != 0 {
		env = append(env, fmt.Sprintf("GUST_PROXY_PORT=%d", opts.ProxyPort))
	}
	return env
}

func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return errors.New("invalid pid")
	}
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
