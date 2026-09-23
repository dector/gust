package logger

import (
	"bytes"
	"testing"
)

func TestPrintfPrefixesGustLogs(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, false)

	log.Printf("hello %s", "world")

	if got, want := out.String(), "[gust] hello world\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestVerbosefHonorsVerboseMode(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, false)
	log.Verbosef("hidden")
	if out.Len() != 0 {
		t.Fatalf("expected no output, got %q", out.String())
	}

	log = New(&out, true)
	log.Verbosef("shown")
	if got, want := out.String(), "[gust] shown\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrintfColorsPrefixWhenEnabled(t *testing.T) {
	var out bytes.Buffer
	log := NewWithOptions(&out, false, Options{Color: AlwaysColor})

	log.Printf("hello")

	if got, want := out.String(), "\x1b[36m[gust]\x1b[0m hello\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestErrorfColorsMessageWhenEnabled(t *testing.T) {
	var out bytes.Buffer
	log := NewWithOptions(&out, false, Options{Color: AlwaysColor})

	log.Errorf("restart failed: %v", "boom")

	if got, want := out.String(), "\x1b[36m[gust]\x1b[0m \x1b[31mrestart failed: boom\x1b[0m\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestWarnfColorsMessageWhenEnabled(t *testing.T) {
	var out bytes.Buffer
	log := NewWithOptions(&out, false, Options{Color: AlwaysColor})

	log.Warnf("%s task failed: %s (exit %d)", "before", "templ generate", 1)

	if got, want := out.String(), "\x1b[36m[gust]\x1b[0m \x1b[38;5;208mbefore task failed: templ generate (exit 1)\x1b[0m\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestErrorfAndWarnfPlainWhenColorDisabled(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, false)

	log.Errorf("restart failed: %v", "boom")
	log.Warnf("%s task failed", "before")

	if got, want := out.String(), "[gust] restart failed: boom\n[gust] before task failed\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestNoColorDisablesForcedColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var out bytes.Buffer
	log := NewWithOptions(&out, false, Options{Color: AlwaysColor})

	log.Printf("hello")

	if got, want := out.String(), "[gust] hello\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrintStartupHighlightsKeysWhenColored(t *testing.T) {
	var out bytes.Buffer
	log := NewWithOptions(&out, false, Options{Color: AlwaysColor})

	log.PrintStartup(StartupConfig{Exec: "command"}, "", true)

	if got, want := out.String(), "\x1b[36m[gust]\x1b[0m exec: command\n\x1b[36m[gust]\x1b[0m key: \x1b[33mr\x1b[0m — rerun\n\x1b[36m[gust]\x1b[0m key: \x1b[33ms\x1b[0m — pause/resume auto-reload\n\x1b[36m[gust]\x1b[0m key: \x1b[33mi\x1b[0m — toggle info logs\n\x1b[36m[gust]\x1b[0m key: \x1b[33mq\x1b[0m — quit\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrintStartupSelectedOutput(t *testing.T) {
	var out bytes.Buffer
	log := New(&out, false)

	log.PrintStartup(StartupConfig{
		Exec:         "go run ./cmd/server",
		HasAppPort:   true,
		AppPort:      8080,
		ProxyEnabled: true,
		ProxyPort:    5000,
		HealthPath:   "/health",
		TailscaleURL: "https://gust.example.ts.net",
	}, "/tmp/gust-1000/hash.sock", true)

	want := "[gust] exec: go run ./cmd/server\n" +
		"[gust] app: http://127.0.0.1:8080\n" +
		"[gust] proxy: http://127.0.0.1:5000\n" +
		"[gust] health: http://127.0.0.1:8080/health\n" +
		"[gust] tailscale: https://gust.example.ts.net\n" +
		"[gust] socket: /tmp/gust-1000/hash.sock\n" +
		"[gust] key: r — rerun\n" +
		"[gust] key: s — pause/resume auto-reload\n" +
		"[gust] key: i — toggle info logs\n" +
		"[gust] key: D — toggle debug lines\n" +
		"[gust] key: q — quit\n"
	if got := out.String(); got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}
