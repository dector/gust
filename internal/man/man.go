// Package man implements the `gust man` built-in manual.
package man

import (
	"fmt"
	"io"

	"github.com/dector/gust/docs"
)

// topics maps a topic name to its embedded manual file.
var topics = map[string]string{
	"overview": "man/overview.txt",
	"run":      "man/run.txt",
	"ctl":      "man/ctl.txt",
	"skill":    "man/skill.txt",
}

// Run executes a `gust man` invocation and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintf(stderr, "gust man: unexpected argument: %s\n\n", args[1])
		printPage(stderr, "overview")
		return 2
	}

	topic := "overview"
	if len(args) == 1 && args[0] != "help" && args[0] != "-h" && args[0] != "--help" {
		topic = args[0]
	}
	if _, ok := topics[topic]; !ok {
		fmt.Fprintf(stderr, "gust man: unknown topic %q (topics: run, ctl, skill)\n\n", topic)
		printPage(stderr, "overview")
		return 2
	}

	printPage(stdout, topic)
	return 0
}

func printPage(w io.Writer, topic string) {
	data, err := docs.Man.ReadFile(topics[topic])
	if err != nil {
		fmt.Fprintf(w, "gust man: cannot read %s: %v\n", topic, err)
		return
	}
	_, _ = w.Write(data)
}
