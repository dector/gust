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
	}, "/tmp/gust-1000/hash.sock", true)

	want := "[gust] exec: go run ./cmd/server\n" +
		"[gust] app: http://127.0.0.1:8080\n" +
		"[gust] proxy: http://127.0.0.1:5000\n" +
		"[gust] health: http://127.0.0.1:8080/health\n" +
		"[gust] socket: /tmp/gust-1000/hash.sock\n" +
		"[gust] keys: r=rerun, s=pause/resume auto-reload, q=quit\n"
	if got := out.String(); got != want {
		t.Fatalf("startup output = %q, want %q", got, want)
	}
}
