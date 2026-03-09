package main

import (
	"fmt"
	"os"

	"github.com/sch0tten/hauntty/cmd"
)

// Set by -ldflags at build time:
//
//	go build -ldflags "-X main.version=1.0.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	cmd.Version = version
	cmd.Commit = commit
	cmd.BuildDate = buildDate
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
