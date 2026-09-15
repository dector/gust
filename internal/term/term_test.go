package term

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dector/gust/internal/logger"
)

func TestIsTerminalReportsPipeAsFalse(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	if IsTerminal(r) {
		t.Fatal("pipe reported as terminal")
	}
}

func TestHandleKeyTogglesDebug(t *testing.T) {
	count := 0
	ctl := New(nil, nil, nil, nil, func() { count++ }, nil, nil)

	if ctl.handleKey('d') || ctl.handleKey('D') {
		t.Fatal("debug key requested quit")
	}
	if count != 2 {
		t.Fatalf("debug callback count = %d, want 2", count)
	}
}

func TestHandleKeyTogglesInfo(t *testing.T) {
	count := 0
	ctl := New(nil, nil, nil, nil, nil, func() { count++ }, nil)

	if ctl.handleKey('i') || ctl.handleKey('I') {
		t.Fatal("info key requested quit")
	}
	if count != 2 {
		t.Fatalf("info callback count = %d, want 2", count)
	}
}

func TestStartNonTTYDisablesKeyboardControls(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	var out bytes.Buffer
	ctl := New(r, logger.New(&out, false), nil, nil, nil, nil, nil)
	if err := ctl.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := ctl.Restore(); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if !strings.Contains(out.String(), "keyboard controls disabled") {
		t.Fatalf("log = %q, want disabled keyboard message", out.String())
	}
}
