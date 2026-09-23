package config

import "time"

// Fixed timing constants from the v1 specification.
const (
	FSDebounce         = 500 * time.Millisecond
	HealthTimeout      = 10 * time.Second
	HealthInterval     = 200 * time.Millisecond
	StabilityWindow    = 300 * time.Millisecond
	ShutdownTimeout    = 2 * time.Second
	ProxyRetryTimeout  = 10 * time.Second
	ProxyRetryInterval = 200 * time.Millisecond
)

// Config holds Gust runtime configuration.
type Config struct {
	Exec         string
	Before       []string
	After        []string
	AppPort      int
	HasAppPort   bool
	ProxyPort    int
	ProxyEnabled bool
	HealthPath   string
	Tailscale    bool
	Excludes     []string
	ExcludeGlobs []string
	Verbose      bool
	Info         bool
	Root         string
}

// ExposurePort returns the browser-facing proxy port when available, otherwise the app port.
func (c Config) ExposurePort() int {
	if c.ProxyEnabled {
		return c.ProxyPort
	}
	return c.AppPort
}
