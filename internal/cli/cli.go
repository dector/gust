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
	"github.com/dector/nettw"
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
	fs.StringVar(&portSpec, "p", "", "app port, or app:proxy ports (? selects a free port)")
	fs.StringVar(&cfg.HealthPath, "h", "", "health endpoint path")
	fs.BoolVar(&cfg.Tailscale, "T", false, "expose app port via Tailscale Serve")
	fs.Var(&excludes, "exclude", "path exclude, repeatable")
	fs.Var(&excludeGlobs, "exclude.glob", "glob exclude, repeatable")
	fs.Var(&optins, "optin", "opt-in feature, repeatable (comments, sounds)")
	fs.BoolVar(&cfg.Verbose, "v", false, "enable verbose Gust logs")
	fs.BoolVar(&cfg.SelfDev, "self-dev", false, "enable selecting Gust panel elements and reload after proxy restart")
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
		switch value {
		case "comments":
			cfg.CommentsEnabled = true
		case "sounds":
			cfg.SoundsEnabled = true
		default:
			fs.Usage()
			return config.Config{}, fmt.Errorf("unknown --optin value %q", value)
		}
	}

	root, err := filepath.Abs(".")
	if err != nil {
		return config.Config{}, err
	}
	cfg.Root = root

	if portSpec != "" {
		appPort, proxyPort, proxyEnabled, err := parsePortSpec(root, portSpec)
		if err != nil {
			fs.Usage()
			return config.Config{}, err
		}
		cfg.AppPort = appPort
		cfg.HasAppPort = true
		cfg.ProxyPort = proxyPort
		cfg.ProxyEnabled = proxyEnabled
	}

	if cfg.SelfDev && (!cfg.CommentsEnabled || !cfg.ProxyEnabled) {
		fs.Usage()
		return config.Config{}, errors.New("--self-dev requires --optin comments and a proxy port (-p app:proxy)")
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

	return cfg, nil
}

const usageText = `Usage: gust -e <cmd> [--e.before <cmd>]... [--e.after <cmd>]... [-p <port>|<app:proxy>] [-h <path>] [-T] [--exclude <path>] [--exclude.glob <glob>] [--optin <feature>]... [--self-dev] [-v]

Flags:
  -e <cmd>              required command, executed via /bin/sh -c
  --e.before <cmd>      command to run before each rerun, repeatable, fail-fast
  --e.after <cmd>       command to run after each start, repeatable
  -p <port>             app port, or app:proxy ports; ? selects a free port
  -h <path>             health endpoint path, requires app port
  -T                    expose app port via Tailscale Serve, requires -p
  --exclude <path>      path exclude, repeatable
  --exclude.glob <glob> glob exclude, repeatable
  --optin <feature>     opt-in feature, repeatable (comments, sounds)
  --self-dev            allow Ctrl+click selection of Gust panel and reload after proxy restart
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

func parsePortSpec(root, spec string) (appPort int, proxyPort int, proxyEnabled bool, err error) {
	if strings.Count(spec, ":") > 1 {
		return 0, 0, false, fmt.Errorf("invalid -p value %q", spec)
	}

	parts := strings.Split(spec, ":")
	if parts[0] == "" {
		return 0, 0, false, errors.New("invalid app port: proxy requires a known app port; use ? for a free port")
	}
	proxyEnabled = len(parts) == 2 && parts[1] != ""
	// Resolve fixed ports first so a random app port can avoid a fixed proxy.
	if parts[0] != "?" {
		appPort, err = parsePort(parts[0])
		if err != nil {
			return 0, 0, false, fmt.Errorf("invalid app port: %w", err)
		}
	}
	if proxyEnabled && parts[1] != "?" {
		proxyPort, err = parsePort(parts[1])
		if err != nil {
			return 0, 0, false, fmt.Errorf("invalid proxy port: %w", err)
		}
	}
	if proxyEnabled && appPort != 0 && appPort == proxyPort {
		return 0, 0, false, errors.New("proxy port must differ from app port")
	}
	if parts[0] == "?" {
		appPort, err = stablePort(root, "app", proxyPort)
		if err != nil {
			return 0, 0, false, fmt.Errorf("select app port: %w", err)
		}
	}
	if proxyEnabled && parts[1] == "?" {
		proxyPort, err = stablePort(root, "proxy", appPort)
		if err != nil {
			return 0, 0, false, fmt.Errorf("select proxy port: %w", err)
		}
	}
	return appPort, proxyPort, proxyEnabled, nil
}

const (
	firstLocalPort = 10000
	localPortCount = 20000 // below Tailscale's HTTPS port range and typical ephemeral ports
)

// stablePort lets nettw probe path-derived candidates. Port selection is not a
// reservation: the app needs the port in its environment before it can bind.
func stablePort(root, role string, exclude int) (int, error) {
	seed := root + "\x00" + role
	for i := range 10 {
		candidateSeed := seed
		if i > 0 {
			candidateSeed += "\x00" + strconv.Itoa(i)
		}
		port, err := nettw.ParsePortOrPickAnother("?",
			nettw.WithIgnoreInvalidPort(true),
			nettw.WithSeed(candidateSeed),
			nettw.WithPortRange(firstLocalPort, firstLocalPort+localPortCount-1),
			nettw.WithMaxTries(localPortCount),
		)
		if err != nil {
			return 0, err
		}
		if port.Int != exclude {
			return port.Int, nil
		}
	}
	return 0, errors.New("could not find a distinct free port")
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
