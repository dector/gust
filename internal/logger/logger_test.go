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
