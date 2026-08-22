package config

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
