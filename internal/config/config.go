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
	AppPort      int
	HasAppPort   bool
	ProxyPort    int
	ProxyEnabled bool
	HealthPath   string
	Excludes     []string
	Verbose      bool
	Root         string
}
