package logger

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// Logger writes Gust-owned log lines.
type Logger struct {
	out     io.Writer
	verbose bool
	color   bool
}

// ColorMode controls ANSI color output for Gust-owned log lines.
type ColorMode int

const (
	// AutoColor enables colors only when writing to a terminal.
	AutoColor ColorMode = iota
	// AlwaysColor always enables colors unless NO_COLOR is set.
	AlwaysColor
	// NeverColor always disables colors.
	NeverColor
)

// Options controls logger behavior beyond verbosity.
type Options struct {
	Color ColorMode
}

const (
	ansiReset = "\x1b[0m"
	ansiCyan  = "\x1b[36m"
)

// New returns a logger that writes to out.
func New(out io.Writer, verbose bool) *Logger {
	return NewWithOptions(out, verbose, Options{Color: AutoColor})
}

// NewWithOptions returns a logger that writes to out with explicit options.
func NewWithOptions(out io.Writer, verbose bool, opts Options) *Logger {
	return &Logger{out: out, verbose: verbose, color: shouldColor(out, opts.Color)}
}

// Printf writes a Gust-prefixed log line.
func (l *Logger) Printf(format string, args ...any) {
	if l == nil || l.out == nil {
		return
	}
	fmt.Fprintf(l.out, l.prefix()+format+"\n", args...)
}

// Verbosef writes a Gust-prefixed log line when verbose logging is enabled.
func (l *Logger) Verbosef(format string, args ...any) {
	if l == nil || !l.verbose {
		return
	}
	l.Printf(format, args...)
}

func (l *Logger) prefix() string {
	if l.color {
		return ansiCyan + "[gust]" + ansiReset + " "
	}
	return "[gust] "
}

func shouldColor(out io.Writer, mode ColorMode) bool {
	if os.Getenv("NO_COLOR") != "" || mode == NeverColor {
		return false
	}
	if mode == AlwaysColor || os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	return isTerminal(out)
}

func isTerminal(out io.Writer) bool {
	file, ok := out.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}

// StartupConfig is the subset of runtime config printed at startup.
type StartupConfig struct {
	Exec         string
	HasAppPort   bool
	AppPort      int
	ProxyEnabled bool
	ProxyPort    int
	HealthPath   string
}

// PrintStartup writes the user-facing startup summary.
func (l *Logger) PrintStartup(cfg StartupConfig, socketPath string, keysEnabled bool) {
	l.Printf("exec: %s", cfg.Exec)
	if cfg.HasAppPort {
		l.Printf("app: http://127.0.0.1:%d", cfg.AppPort)
	}
	if cfg.ProxyEnabled {
		l.Printf("proxy: http://127.0.0.1:%d", cfg.ProxyPort)
	}
	if cfg.HealthPath != "" {
		l.Printf("health: http://127.0.0.1:%d%s", cfg.AppPort, cfg.HealthPath)
	}
	if socketPath != "" {
		l.Printf("socket: %s", socketPath)
	}
	if keysEnabled {
		l.Printf("keys: r=rerun, s=pause/resume auto-reload, q=quit")
	}
}
