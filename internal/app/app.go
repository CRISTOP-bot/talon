// Package app carries build-time metadata about the binary.
package app

import (
	"fmt"
	"runtime"
)

// These values are overridden at build time with -ldflags -X.
var (
	Name    = "talon"
	Version = "0.1.0"
	Commit  = "none"
	Date    = "unknown"
)

// UserAgent is sent to every remote endpoint.
func UserAgent() string {
	return fmt.Sprintf("%s/%s (%s; %s/%s)", Name, Version, runtime.GOOS, runtime.GOARCH, Commit)
}

// Banner returns the ASCII art + tagline shown on start.
func Banner() string {
	return `  ┌──────────────────────────────────────┐
  │   T A L O N                          │
  │   agentic coding cli                 │
  └──────────────────────────────────────┘`
}
