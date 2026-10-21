// Package version exposes crane-oss's release metadata. This file is the single
// source of truth the release workflow bumps: it parses the Version line below,
// and on every push to main either auto-bumps the patch or honors a hand-edited
// value. Commit and Date are injected at build time via -ldflags (see
// Dockerfile.* and .goreleaser.yml).
package version

import "fmt"

// Version is synced by the release workflow (keep the `var Version = "x.y.z"`
// form on a single line — the workflow parses it with sed).
var Version = "0.2.5"

// Commit is the git SHA the binary was built from, injected via -ldflags.
var Commit = "unknown"

// Date is the build timestamp, injected via -ldflags.
var Date = "unknown"

// String returns a human-readable, build-stamped version string.
func String() string {
	commit := Commit
	if len(commit) > 7 {
		commit = commit[:7]
	}
	return fmt.Sprintf("%s (commit %s, built %s)", Version, commit, Date)
}
