package logger

import (
	"bytes"
	"strings"
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

	want := "\x1b[36m[gust]\x1b[0m \x1b[1m\x1b[36m-------------------- gust --------------------\x1b[0m\n" +
		"\n\x1b[36m[gust]\x1b[0m exec: command\n" +
		"\n\x1b[36m[gust]\x1b[0m \x1b[1mKeys\x1b[0m\n" +
		"\x1b[36m[gust]\x1b[0m key: \x1b[33mr\x1b[0m — rerun\n" +
		"\x1b[36m[gust]\x1b[0m key: \x1b[33ms\x1b[0m — pause/resume auto-reload\n" +
		"\x1b[36m[gust]\x1b[0m key: \x1b[33mi\x1b[0m — toggle info logs\n" +
		"\x1b[36m[gust]\x1b[0m key: \x1b[33mq\x1b[0m — quit\n" +
		"\n\x1b[36m[gust]\x1b[0m \x1b[2m----------------------------------------------\x1b[0m\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPrintStartupColorsURLs(t *testing.T) {
	var out bytes.Buffer
	log := NewWithOptions(&out, false, Options{Color: AlwaysColor})
	log.PrintStartup(StartupConfig{Exec: "command", ProxyEnabled: true, ProxyPort: 5000}, "", false)

	if !strings.Contains(out.String(), "\x1b[1mproxy (browser)\x1b[0m: \x1b[1m\x1b[32mhttp://127.0.0.1:5000\x1b[0m") {
		t.Fatalf("URL not highlighted: %q", out.String())
	}
	if strings.Contains(out.String(), "Keys") {
		t.Fatalf("keys shown when disabled: %q", out.String())
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

	want := "[gust] -------------------- gust --------------------\n" +
		"\n[gust] exec: go run ./cmd/server\n" +
		"\n[gust] URLs\n" +
		"[gust] proxy (browser): http://127.0.0.1:5000\n" +
		"[gust] tailscale (HTTPS): https://gust.example.ts.net\n" +
		"[gust] app (direct): http://127.0.0.1:8080\n" +
		"[gust] health check: http://127.0.0.1:8080/health\n" +
		"\n[gust] socket (control): /tmp/gust-1000/hash.sock\n" +
		"\n[gust] Keys\n" +
		"[gust] key: r — rerun\n" +
		"[gust] key: s — pause/resume auto-reload\n" +
		"[gust] key: i — toggle info logs\n" +
		"[gust] key: D — toggle debug lines\n" +
		"[gust] key: q — quit\n" +
		"\n[gust] ----------------------------------------------\n"
	if got := out.String(); got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}
