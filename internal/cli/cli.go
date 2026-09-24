package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dector/gust/internal/config"
)

// Parse converts command-line arguments into a Gust configuration.
func Parse(args []string) (config.Config, error) {
	return ParseWithOutput(args, os.Stderr)
}

// ParseWithOutput converts command-line arguments into a Gust configuration and
// writes flag errors and usage to out.
func ParseWithOutput(args []string, out io.Writer) (config.Config, error) {
	if out == nil {
		out = io.Discard
	}

	var excludes repeatableStrings
	var excludeGlobs repeatableStrings
	var optins repeatableStrings
	var befores repeatableStrings
	var afters repeatableStrings
	var cfg config.Config
	cfg.Info = os.Getenv("GUST_INFO") == "1"
	var portSpec string

	fs := flag.NewFlagSet("gust", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&cfg.Exec, "e", "", "command to execute via /bin/sh -c")
	fs.Var(&befores, "e.before", "command to run before each rerun, repeatable")
	fs.Var(&afters, "e.after", "command to run after each start, repeatable")
	fs.StringVar(&portSpec, "p", "", "app port, or app:proxy ports")
	fs.StringVar(&cfg.HealthPath, "h", "", "health endpoint path")
	fs.BoolVar(&cfg.Tailscale, "T", false, "expose app port via Tailscale Serve")
	fs.Var(&excludes, "exclude", "path exclude, repeatable")
	fs.Var(&excludeGlobs, "exclude.glob", "glob exclude, repeatable")
	fs.Var(&optins, "optin", "opt-in feature, repeatable (comments)")
	fs.BoolVar(&cfg.Verbose, "v", false, "enable verbose Gust logs")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usageText)
	}

	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return config.Config{}, fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}
	if strings.TrimSpace(cfg.Exec) == "" {
		fs.Usage()
		return config.Config{}, errors.New("missing required -e")
	}

	for _, value := range optins {
		if strings.TrimSpace(value) == "" {
			fs.Usage()
			return config.Config{}, errors.New("--optin requires a non-empty value")
		}
		if value != "comments" {
			fs.Usage()
			return config.Config{}, fmt.Errorf("unknown --optin value %q", value)
		}
		cfg.CommentsEnabled = true
	}

	if portSpec != "" {
		appPort, proxyPort, proxyEnabled, err := parsePortSpec(portSpec)
		if err != nil {
			fs.Usage()
			return config.Config{}, err
		}
		cfg.AppPort = appPort
		cfg.HasAppPort = true
		cfg.ProxyPort = proxyPort
		cfg.ProxyEnabled = proxyEnabled
	}

	if cfg.Tailscale && !cfg.HasAppPort {
		fs.Usage()
		return config.Config{}, errors.New("-T requires -p app port")
	}

	if cfg.HealthPath != "" {
		if !cfg.HasAppPort {
			fs.Usage()
			return config.Config{}, errors.New("-h requires -p app port")
		}
		if !strings.HasPrefix(cfg.HealthPath, "/") {
			fs.Usage()
			return config.Config{}, errors.New("-h requires an absolute path starting with /")
		}
	}

	cleanedBefore, err := cleanCommands(befores, "--e.before")
	if err != nil {
		fs.Usage()
		return config.Config{}, err
	}
	cfg.Before = cleanedBefore

	cleanedAfter, err := cleanCommands(afters, "--e.after")
	if err != nil {
		fs.Usage()
		return config.Config{}, err
	}
	cfg.After = cleanedAfter

	cleanedExcludes, err := cleanExcludes(excludes)
	if err != nil {
		fs.Usage()
		return config.Config{}, err
	}
	cfg.Excludes = cleanedExcludes

	cleanedExcludeGlobs, err := cleanExcludeGlobs(excludeGlobs)
	if err != nil {
		fs.Usage()
		return config.Config{}, err
	}
	cfg.ExcludeGlobs = cleanedExcludeGlobs

	root, err := filepath.Abs(".")
	if err != nil {
		return config.Config{}, err
	}
	cfg.Root = root

	return cfg, nil
}

const usageText = `Usage: gust -e <cmd> [--e.before <cmd>]... [--e.after <cmd>]... [-p <port>|<app:proxy>] [-h <path>] [-T] [--exclude <path>] [--exclude.glob <glob>] [--optin <feature>]... [-v]

Flags:
  -e <cmd>              required command, executed via /bin/sh -c
  --e.before <cmd>      command to run before each rerun, repeatable, fail-fast
  --e.after <cmd>       command to run after each start, repeatable
  -p <port>             app port, or app:proxy ports
  -h <path>             health endpoint path, requires app port
  -T                    expose app port via Tailscale Serve, requires -p
  --exclude <path>      path exclude, repeatable
  --exclude.glob <glob> glob exclude, repeatable
  --optin <feature>     opt-in feature, repeatable (comments)
  -v                    enable verbose Gust logs

Run "gust man" for a brief manual and samples.
`

func cleanCommands(values []string, flagName string) ([]string, error) {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s requires a non-empty command", flagName)
		}
		cleaned = append(cleaned, value)
	}
	return cleaned, nil
}

func parsePortSpec(spec string) (appPort int, proxyPort int, proxyEnabled bool, err error) {
	if strings.Count(spec, ":") > 1 {
		return 0, 0, false, fmt.Errorf("invalid -p value %q", spec)
	}

	parts := strings.Split(spec, ":")
	appPort, err = parsePort(parts[0])
	if err != nil {
		return 0, 0, false, fmt.Errorf("invalid app port: %w", err)
	}
	if len(parts) == 1 {
		return appPort, 0, false, nil
	}

	proxyPort, err = parsePort(parts[1])
	if err != nil {
		return 0, 0, false, fmt.Errorf("invalid proxy port: %w", err)
	}
	if proxyPort == appPort {
		return 0, 0, false, errors.New("proxy port must differ from app port")
	}
	return appPort, proxyPort, true, nil
}

func parsePort(value string) (int, error) {
	if value == "" {
		return 0, errors.New("empty port")
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port %d out of range 1..65535", port)
	}
	return port, nil
}

func cleanExcludes(values []string) ([]string, error) {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("--exclude requires a non-empty path")
		}
		path := filepath.Clean(value)
		if filepath.IsAbs(path) {
			return nil, fmt.Errorf("--exclude must be relative: %s", value)
		}
		if path == "." {
			return nil, errors.New("--exclude cannot be project root")
		}
		cleaned = append(cleaned, path)
	}
	return cleaned, nil
}

func cleanExcludeGlobs(values []string) ([]string, error) {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("--exclude.glob requires a non-empty glob")
		}
		if filepath.IsAbs(value) {
			return nil, fmt.Errorf("--exclude.glob must be relative: %s", value)
		}
		if value == "." {
			return nil, errors.New("--exclude.glob cannot be project root")
		}
		if _, err := filepath.Match(value, ""); err != nil {
			return nil, fmt.Errorf("--exclude.glob has invalid syntax %q: %w", value, err)
		}
		cleaned = append(cleaned, filepath.Clean(value))
	}
	return cleaned, nil
}

type repeatableStrings []string

func (s *repeatableStrings) String() string {
	return strings.Join(*s, ",")
}

func (s *repeatableStrings) Set(value string) error {
	*s = append(*s, value)
	return nil
}
