package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRequiredExec(t *testing.T) {
	var out bytes.Buffer
	_, err := ParseWithOutput(nil, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "missing required -e") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Usage: gust") {
		t.Fatalf("expected usage output, got %q", out.String())
	}
}

func TestParseInfoFromEnvironment(t *testing.T) {
	t.Setenv("GUST_INFO", "1")
	cfg, err := ParseWithOutput([]string{"-e", "run"}, nil)
	if err != nil {
		t.Fatalf("ParseWithOutput returned error: %v", err)
	}
	if !cfg.Info {
		t.Fatal("Info = false, want true")
	}
}

func TestParseMinimal(t *testing.T) {
	var out bytes.Buffer
	cfg, err := ParseWithOutput([]string{"-e", "go test ./..."}, &out)
	if err != nil {
		t.Fatalf("ParseWithOutput returned error: %v", err)
	}
	if cfg.Exec != "go test ./..." {
		t.Fatalf("Exec = %q", cfg.Exec)
	}
	if cfg.HasAppPort || cfg.AppPort != 0 || cfg.ProxyEnabled || cfg.ProxyPort != 0 {
		t.Fatalf("unexpected port config: %+v", cfg)
	}
	if cfg.Root == "" || !filepath.IsAbs(cfg.Root) {
		t.Fatalf("Root = %q, want absolute path", cfg.Root)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestParseCommentsOptIn(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{args: []string{"-e", "run"}},
		{args: []string{"-e", "run", "--optin", "comments"}, want: true},
		{args: []string{"-e", "run", "--optin=comments", "--optin", "comments"}, want: true},
	} {
		cfg, err := ParseWithOutput(tc.args, nil)
		if err != nil || cfg.CommentsEnabled != tc.want {
			t.Fatalf("ParseWithOutput(%v) = comments %v, err %v; want %v", tc.args, cfg.CommentsEnabled, err, tc.want)
		}
	}
	for _, value := range []string{"", "unknown"} {
		_, err := ParseWithOutput([]string{"-e", "run", "--optin", value}, nil)
		if err == nil {
			t.Fatalf("--optin %q should fail", value)
		}
	}
}

func TestParseSelfDev(t *testing.T) {
	for _, args := range [][]string{
		{"-e", "run", "--self-dev", "-p", "8000:8001"},
		{"-e", "run", "--self-dev", "--optin", "comments"},
	} {
		if _, err := ParseWithOutput(args, nil); err == nil {
			t.Fatalf("expected invalid self-dev combination: %v", args)
		}
	}
	cfg, err := ParseWithOutput([]string{"-e", "run", "--self-dev", "--optin", "comments", "-p", "8000:8001", "-T"}, nil)
	if err != nil || !cfg.SelfDev || !cfg.Tailscale {
		t.Fatalf("self-dev with Tailscale: cfg=%+v err=%v", cfg, err)
	}
}

func TestParseAppPort(t *testing.T) {
	cfg, err := ParseWithOutput([]string{"-e", "run", "-p", "8080"}, nil)
	if err != nil {
		t.Fatalf("ParseWithOutput returned error: %v", err)
	}
	if !cfg.HasAppPort || cfg.AppPort != 8080 {
		t.Fatalf("unexpected app port config: %+v", cfg)
	}
	if cfg.ProxyEnabled || cfg.ProxyPort != 0 {
		t.Fatalf("unexpected proxy config: %+v", cfg)
	}
}

func TestParseProxyPorts(t *testing.T) {
	cfg, err := ParseWithOutput([]string{"-e", "run", "-p", "8080:5000"}, nil)
	if err != nil {
		t.Fatalf("ParseWithOutput returned error: %v", err)
	}
	if !cfg.HasAppPort || cfg.AppPort != 8080 || !cfg.ProxyEnabled || cfg.ProxyPort != 5000 {
		t.Fatalf("unexpected port config: %+v", cfg)
	}
}

func TestParseTailscale(t *testing.T) {
	for _, args := range [][]string{
		{"-e", "run", "-p", "8080", "-T"},
		{"-e", "run", "-p", "8080:5000", "-h", "/health", "-T"},
	} {
		cfg, err := ParseWithOutput(args, nil)
		if err != nil || !cfg.Tailscale || cfg.AppPort != 8080 {
			t.Fatalf("ParseWithOutput(%v) = %+v, %v", args, cfg, err)
		}
	}
	var out bytes.Buffer
	_, err := ParseWithOutput([]string{"-e", "run", "-T"}, &out)
	if err == nil || !strings.Contains(err.Error(), "-T requires -p") {
		t.Fatalf("missing port error = %v", err)
	}
	if !strings.Contains(out.String(), "-T") {
		t.Fatalf("usage missing -T: %q", out.String())
	}
	_, err = ParseWithOutput([]string{"-e", "run", "-T=true", "-p", "8080"}, nil)
	if err != nil {
		t.Fatalf("bool flag rejected: %v", err)
	}
}

func TestParseHealthRequiresAppPort(t *testing.T) {
	var out bytes.Buffer
	_, err := ParseWithOutput([]string{"-e", "run", "-h", "/health"}, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "-h requires -p") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Usage: gust") {
		t.Fatalf("expected usage output, got %q", out.String())
	}
}

func TestParseHealthWithAppPort(t *testing.T) {
	cfg, err := ParseWithOutput([]string{"-e", "run", "-p", "8080", "-h", "/health"}, nil)
	if err != nil {
		t.Fatalf("ParseWithOutput returned error: %v", err)
	}
	if cfg.HealthPath != "/health" {
		t.Fatalf("HealthPath = %q", cfg.HealthPath)
	}
}

func TestParseHealthRequiresAbsolutePath(t *testing.T) {
	var out bytes.Buffer
	_, err := ParseWithOutput([]string{"-e", "run", "-p", "8080", "-h", "health"}, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "starting with /") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Usage: gust") {
		t.Fatalf("expected usage output, got %q", out.String())
	}
}

func TestParseVerboseAndRepeatableExcludes(t *testing.T) {
	cfg, err := ParseWithOutput([]string{
		"-e", "run",
		"--exclude", "frontend/../frontend/node_modules",
		"--exclude", "tmp/cache",
		"--exclude.glob", "*_templ.go",
		"--exclude.glob", "assets/*.tmp",
		"-v",
	}, nil)
	if err != nil {
		t.Fatalf("ParseWithOutput returned error: %v", err)
	}
	if !cfg.Verbose {
		t.Fatal("Verbose = false")
	}
	want := []string{"frontend/node_modules", "tmp/cache"}
	if len(cfg.Excludes) != len(want) {
		t.Fatalf("Excludes = %#v", cfg.Excludes)
	}
	for i := range want {
		if cfg.Excludes[i] != want[i] {
			t.Fatalf("Excludes = %#v, want %#v", cfg.Excludes, want)
		}
	}
	wantGlobs := []string{"*_templ.go", "assets/*.tmp"}
	if len(cfg.ExcludeGlobs) != len(wantGlobs) {
		t.Fatalf("ExcludeGlobs = %#v", cfg.ExcludeGlobs)
	}
	for i := range wantGlobs {
		if cfg.ExcludeGlobs[i] != wantGlobs[i] {
			t.Fatalf("ExcludeGlobs = %#v, want %#v", cfg.ExcludeGlobs, wantGlobs)
		}
	}
}

func TestParseBeforeAndAfter(t *testing.T) {
	cfg, err := ParseWithOutput([]string{
		"-e", "run",
		"--e.before", "templ generate",
		"--e.before", "sqlc generate",
		"--e.after", "notify",
	}, nil)
	if err != nil {
		t.Fatalf("ParseWithOutput returned error: %v", err)
	}
	wantBefore := []string{"templ generate", "sqlc generate"}
	if len(cfg.Before) != len(wantBefore) {
		t.Fatalf("Before = %#v, want %#v", cfg.Before, wantBefore)
	}
	for i := range wantBefore {
		if cfg.Before[i] != wantBefore[i] {
			t.Fatalf("Before = %#v, want %#v", cfg.Before, wantBefore)
		}
	}
	if len(cfg.After) != 1 || cfg.After[0] != "notify" {
		t.Fatalf("After = %#v, want [notify]", cfg.After)
	}
}

func TestParseRejectsEmptyTaskCommand(t *testing.T) {
	for _, flagName := range []string{"--e.before", "--e.after"} {
		t.Run(flagName, func(t *testing.T) {
			var out bytes.Buffer
			_, err := ParseWithOutput([]string{"-e", "run", flagName, "   "}, &out)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(out.String(), "Usage: gust") {
				t.Fatalf("expected usage output, got %q", out.String())
			}
		})
	}
}

func TestParseUsageShowsTaskFlags(t *testing.T) {
	var out bytes.Buffer
	_, _ = ParseWithOutput(nil, &out)
	usage := out.String()
	if !strings.Contains(usage, "--e.before") || !strings.Contains(usage, "--e.after") {
		t.Fatalf("usage does not document task flags: %q", usage)
	}
}

func TestParseRejectsUnexpectedArg(t *testing.T) {
	var out bytes.Buffer
	_, err := ParseWithOutput([]string{"-e", "run", "extra"}, &out)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Usage: gust") {
		t.Fatalf("expected usage output, got %q", out.String())
	}
}

func TestParseRandomPorts(t *testing.T) {
	for _, tt := range []struct {
		spec       string
		proxy      bool
		fixedApp   int
		fixedProxy int
	}{
		{spec: "?"},
		{spec: "?:?", proxy: true},
		{spec: "?:", proxy: false},
		{spec: "8080:?", proxy: true, fixedApp: 8080},
		{spec: "?:5000", proxy: true, fixedProxy: 5000},
		{spec: "8080:", fixedApp: 8080},
	} {
		t.Run(tt.spec, func(t *testing.T) {
			cfg, err := ParseWithOutput([]string{"-e", "run", "-p", tt.spec, "-T", "-h", "/health"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !cfg.HasAppPort || cfg.AppPort < 1 || cfg.AppPort > 65535 || cfg.ProxyEnabled != tt.proxy {
				t.Fatalf("unexpected ports: %+v", cfg)
			}
			if tt.fixedApp != 0 && cfg.AppPort != tt.fixedApp {
				t.Fatalf("app port = %d, want %d", cfg.AppPort, tt.fixedApp)
			}
			if tt.proxy {
				if cfg.ProxyPort < 1 || cfg.ProxyPort > 65535 || cfg.ProxyPort == cfg.AppPort {
					t.Fatalf("invalid proxy port: %+v", cfg)
				}
			} else if cfg.ProxyPort != 0 {
				t.Fatalf("unexpected proxy port: %d", cfg.ProxyPort)
			}
			if tt.fixedProxy != 0 && cfg.ProxyPort != tt.fixedProxy {
				t.Fatalf("proxy port = %d, want %d", cfg.ProxyPort, tt.fixedProxy)
			}
		})
	}
}

func TestParsePortValidation(t *testing.T) {
	tests := []struct {
		name string
		port string
	}{
		{name: "zero", port: "0"},
		{name: "too high", port: "65536"},
		{name: "not number", port: "abc"},
		{name: "missing app", port: ":5000"},
		{name: "unknown app for proxy", port: ":?"},
		{name: "too many parts", port: "8080:5000:1"},
		{name: "same ports", port: "8080:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := ParseWithOutput([]string{"-e", "run", "-p", tt.port}, &out)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(out.String(), "Usage: gust") {
				t.Fatalf("expected usage output, got %q", out.String())
			}
		})
	}
}

func TestParseExcludeValidation(t *testing.T) {
	tests := []struct {
		name    string
		exclude string
	}{
		{name: "empty", exclude: ""},
		{name: "absolute", exclude: "/tmp/cache"},
		{name: "root", exclude: "."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := ParseWithOutput([]string{"-e", "run", "--exclude", tt.exclude}, &out)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(out.String(), "Usage: gust") {
				t.Fatalf("expected usage output, got %q", out.String())
			}
		})
	}
}

func TestParseExcludeGlobValidation(t *testing.T) {
	tests := []struct {
		name string
		glob string
	}{
		{name: "empty", glob: ""},
		{name: "absolute", glob: "/tmp/*.go"},
		{name: "root", glob: "."},
		{name: "invalid", glob: "["},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			_, err := ParseWithOutput([]string{"-e", "run", "--exclude.glob", tt.glob}, &out)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(out.String(), "Usage: gust") {
				t.Fatalf("expected usage output, got %q", out.String())
			}
		})
	}
}
