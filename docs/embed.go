// Package docs exposes Gust's embedded documentation files.
package docs

import "embed"

// Man holds the manual pages under docs/man: overview.txt, run.txt,
// ctl.txt, and skill.txt.
//
//go:embed man/*.txt
var Man embed.FS
