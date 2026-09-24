package man

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dector/gust/docs"
)

func runMan(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestOverview(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"-h"}, {"--help"}} {
		code, out, _ := runMan(t, args...)
		if code != 0 {
			t.Fatalf("args %v code = %d, want 0", args, code)
		}
		for _, want := range []string{"gust - local development runner", "Usage:", "gust ctl status", "gust man run", "gust man skill", "gust skill comments"} {
			if !strings.Contains(out, want) {
				t.Fatalf("args %v output missing %q:\n%s", args, want, out)
			}
		}
	}
}

func TestTopics(t *testing.T) {
	tests := []struct {
		topic string
		want  []string
	}{
		{topic: "run", want: []string{"gust run - run and watch a command", "-e <cmd>", "--e.before", "gust -e 'go run ./cmd/server'", "Keys"}},
		{topic: "ctl", want: []string{"gust ctl - control a running instance", "status", "pause", "rerun", "resume", "Exit codes"}},
		{topic: "skill", want: []string{"gust skill comments", "gust skill comments run", "gust skill comments watch", "gust skill --help", "Only the plural"}},
	}
	for _, tt := range tests {
		t.Run(tt.topic, func(t *testing.T) {
			code, out, _ := runMan(t, tt.topic)
			if code != 0 {
				t.Fatalf("code = %d, want 0", code)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("topic %q output missing %q:\n%s", tt.topic, want, out)
				}
			}
		})
	}
}

func TestAllTopicsEmbedded(t *testing.T) {
	for topic, path := range topics {
		if _, err := docs.Man.ReadFile(path); err != nil {
			t.Fatalf("topic %q path %q: %v", topic, path, err)
		}
	}
}

func TestUnknownTopic(t *testing.T) {
	code, _, errOut := runMan(t, "bogus")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown topic") || !strings.Contains(errOut, "run, ctl, skill") {
		t.Fatalf("stderr = %q, want unknown topic", errOut)
	}
}

func TestUnexpectedArgument(t *testing.T) {
	code, _, errOut := runMan(t, "run", "extra")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unexpected argument") {
		t.Fatalf("stderr = %q, want unexpected argument", errOut)
	}
}
