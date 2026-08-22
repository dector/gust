package logger

import (
	"fmt"
	"io"
)

// Logger writes Gust-owned log lines.
type Logger struct {
	out     io.Writer
	verbose bool
}

// New returns a logger that writes to out.
func New(out io.Writer, verbose bool) *Logger {
	return &Logger{out: out, verbose: verbose}
}

// Printf writes a Gust-prefixed log line.
func (l *Logger) Printf(format string, args ...any) {
	if l == nil || l.out == nil {
		return
	}
	fmt.Fprintf(l.out, "[gust] "+format+"\n", args...)
}

// Verbosef writes a Gust-prefixed log line when verbose logging is enabled.
func (l *Logger) Verbosef(format string, args ...any) {
	if l == nil || !l.verbose {
		return
	}
	l.Printf(format, args...)
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
		l.Printf("keys: r=rerun, q=quit")
	}
}
