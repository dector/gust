package term

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/dector/gust/internal/logger"
	"golang.org/x/sys/unix"
)

const (
	keyCtrlC = 0x03
)

// Controller owns raw terminal keyboard handling.
type Controller struct {
	file *os.File
	log  *logger.Logger

	onRerun func()
	onQuit  func()

	mu       sync.Mutex
	oldState *unix.Termios
	restored bool
}

// New creates a terminal controller for stdin.
func New(stdin *os.File, log *logger.Logger, onRerun, onQuit func()) *Controller {
	return &Controller{file: stdin, log: log, onRerun: onRerun, onQuit: onQuit}
}

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

// Start enables raw keyboard controls when stdin is a TTY.
func (c *Controller) Start(ctx context.Context) error {
	if c == nil || c.file == nil {
		return nil
	}
	if !IsTerminal(c.file) {
		if c.log != nil {
			c.log.Printf("stdin is not a TTY; keyboard controls disabled, socket controls still work")
		}
		return nil
	}
	oldState, err := makeRaw(int(c.file.Fd()))
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.oldState = oldState
	c.restored = false
	c.mu.Unlock()

	go c.readKeys(ctx)
	return nil
}

// Restore restores the terminal mode saved by Start.
func (c *Controller) Restore() error {
	if c == nil || c.file == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.oldState == nil || c.restored {
		return nil
	}
	c.restored = true
	return unix.IoctlSetTermios(int(c.file.Fd()), unix.TCSETS, c.oldState)
}

func (c *Controller) readKeys(ctx context.Context) {
	buf := make([]byte, 1)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := c.file.Read(buf)
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return
			}
			return
		}
		if n == 0 {
			continue
		}
		switch buf[0] {
		case 'r', 'R':
			if c.onRerun != nil {
				c.onRerun()
			}
		case 'q', 'Q', keyCtrlC:
			if c.onQuit != nil {
				c.onQuit()
			}
			return
		}
	}
}

func makeRaw(fd int) (*unix.Termios, error) {
	oldState, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	newState := *oldState
	newState.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	// Keep output post-processing enabled so \n still returns the cursor to
	// column 0. Disabling OPOST causes "staircase" output while Gust is in
	// raw input mode.
	newState.Cflag |= unix.CS8
	newState.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	newState.Cc[unix.VMIN] = 1
	newState.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &newState); err != nil {
		return nil, err
	}
	return oldState, nil
}
