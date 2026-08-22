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

func TestParsePortValidation(t *testing.T) {
	tests := []struct {
		name string
		port string
	}{
		{name: "zero", port: "0"},
		{name: "too high", port: "65536"},
		{name: "not number", port: "abc"},
		{name: "missing app", port: ":5000"},
		{name: "missing proxy", port: "8080:"},
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
