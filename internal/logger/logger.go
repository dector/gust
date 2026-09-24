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
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiGreen  = "\x1b[32m"
	ansiCyan   = "\x1b[36m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
	ansiOrange = "\x1b[38;5;208m"
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

// Errorf writes a Gust-prefixed log line in red when colors are enabled.
func (l *Logger) Errorf(format string, args ...any) {
	l.colorf(ansiRed, format, args...)
}

// Warnf writes a Gust-prefixed log line in orange when colors are enabled.
func (l *Logger) Warnf(format string, args ...any) {
	l.colorf(ansiOrange, format, args...)
}

func (l *Logger) colorf(color, format string, args ...any) {
	if l == nil || l.out == nil {
		return
	}
	message := fmt.Sprintf(format, args...)
	if l.color {
		message = color + message + ansiReset
	}
	fmt.Fprintf(l.out, "%s%s\n", l.prefix(), message)
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
	Before       []string
	After        []string
	HasAppPort   bool
	AppPort      int
	ProxyEnabled bool
	ProxyPort    int
	HealthPath   string
	TailscaleURL string
}

// PrintStartup writes the user-facing startup summary.
func (l *Logger) PrintStartup(cfg StartupConfig, socketPath string, keysEnabled bool) {
	if l == nil || l.out == nil {
		return
	}
	l.startupLine("-------------------- gust --------------------", ansiBold+ansiCyan)
	l.startupBlank()
	l.Printf("exec: %s", cfg.Exec)
	for _, command := range cfg.Before {
		l.Printf("before: %s", command)
	}
	for _, command := range cfg.After {
		l.Printf("after: %s", command)
	}
	if cfg.HasAppPort || cfg.ProxyEnabled || cfg.HealthPath != "" || cfg.TailscaleURL != "" {
		l.startupBlank()
		l.startupLine("URLs", ansiBold)
		if cfg.ProxyEnabled {
			l.startupURL("proxy (browser)", fmt.Sprintf("http://127.0.0.1:%d", cfg.ProxyPort))
		}
		if cfg.TailscaleURL != "" {
			l.startupURL("tailscale (HTTPS)", cfg.TailscaleURL)
		}
		if cfg.HasAppPort {
			l.startupURL("app (direct)", fmt.Sprintf("http://127.0.0.1:%d", cfg.AppPort))
		}
		if cfg.HealthPath != "" {
			l.startupURL("health check", fmt.Sprintf("http://127.0.0.1:%d%s", cfg.AppPort, cfg.HealthPath))
		}
	}
	if socketPath != "" {
		l.startupBlank()
		l.Printf("socket (control): %s", socketPath)
	}
	if keysEnabled {
		l.startupBlank()
		l.startupLine("Keys", ansiBold)
		l.printKey("r", "rerun")
		l.printKey("s", "pause/resume auto-reload")
		l.printKey("i", "toggle info logs")
		if cfg.ProxyEnabled {
			l.printKey("D", "toggle debug lines")
		}
		l.printKey("q", "quit")
	}
	l.startupBlank()
	l.startupLine("----------------------------------------------", ansiDim)
}

func (l *Logger) startupBlank() {
	fmt.Fprintln(l.out)
}

func (l *Logger) startupLine(text, style string) {
	if l.color {
		text = style + text + ansiReset
	}
	l.Printf("%s", text)
}

func (l *Logger) startupURL(label, url string) {
	if l.color {
		l.Printf("%s%s%s: %s%s%s", ansiBold, label, ansiReset, ansiBold+ansiGreen, url, ansiReset)
		return
	}
	l.Printf("%s: %s", label, url)
}

func (l *Logger) printKey(key, action string) {
	if l.color {
		l.Printf("key: %s%s%s — %s", ansiYellow, key, ansiReset, action)
		return
	}
	l.Printf("key: %s — %s", key, action)
}
