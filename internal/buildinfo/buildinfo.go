// Copyright (C) 2026 Gerald Hofbauer <info@geraldhofbauer.net> — AGPLv3.
//
// Package buildinfo answers "which build is this?" for every ProjectHub binary —
// the sidecar, the MCP bridge and the WASM UI alike. Go already stamps the git
// revision into binaries built inside the work tree (`vcs.revision`, `vcs.time`,
// `vcs.modified`), so the common case needs no build flags at all; the vars below
// exist for releases built outside a checkout, where -ldflags can fill them in.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strings"
)

// Overridable at link time:
//
//	go build -ldflags "-X github.com/Gerry3010/projecthub/internal/buildinfo.Version=1.2.3"
var (
	Version = "0.1.0" // product version — keep in step with app/package.json
	Commit  = ""      // git sha; empty ⇒ taken from the embedded VCS stamp
	Time    = ""      // build time (RFC3339); empty ⇒ commit time of the VCS stamp
)

// Info is what a binary knows about itself. It is serialised straight into the
// app_info MCP tool, so json tags are part of that contract.
type Info struct {
	Version    string `json:"version"`
	Commit     string `json:"commit,omitempty"`
	CommitTime string `json:"commit_time,omitempty"`
	Dirty      bool   `json:"dirty"`
	BuiltAt    string `json:"built_at,omitempty"`
	GoVersion  string `json:"go_version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

// Get reads the embedded stamp, letting -ldflags values win over it.
func Get() Info {
	i := Info{
		Version:   Version,
		Commit:    Commit,
		BuiltAt:   Time,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return i
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if i.Commit == "" {
				i.Commit = s.Value
			}
		case "vcs.time":
			i.CommitTime = s.Value
		case "vcs.modified":
			i.Dirty = s.Value == "true"
		}
	}
	return i
}

// ShortCommit is the 7-character form used in logs and the "Über" tab.
func (i Info) ShortCommit() string {
	if len(i.Commit) > 7 {
		return i.Commit[:7]
	}
	return i.Commit
}

// String is the one-line identity: "0.1.0 · 269e464 · 2026-08-16" (with a
// "+dirty" marker when the tree had uncommitted changes at build time).
func (i Info) String() string {
	parts := []string{i.Version}
	if c := i.ShortCommit(); c != "" {
		if i.Dirty {
			c += "+dirty"
		}
		parts = append(parts, c)
	}
	if d, _, ok := strings.Cut(i.CommitTime, "T"); ok {
		parts = append(parts, d)
	}
	return strings.Join(parts, " · ")
}
