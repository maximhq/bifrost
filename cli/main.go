package main

import (
	"context"
	"fmt"
	"os"

	"github.com/maximhq/bifrost/cli/internal/command"
)

var (
	version = "dev"
	commit  = "none"
)

// main is the CLI entry point.
func main() {
	runner := command.New(os.Stdin, os.Stdout, os.Stderr, command.BuildInfo{Version: version, Commit: commit})
	if err := runner.Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "bifrost: %v\n", err)
		os.Exit(command.ExitCode(err))
	}
}
