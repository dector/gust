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

func TestStartNonTTYDisablesKeyboardControls(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	var out bytes.Buffer
	ctl := New(r, logger.New(&out, false), nil, nil)
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
