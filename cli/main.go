package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/maximhq/bifrost/cli/internal/app"
)

var (
	version = "dev"
	commit  = "none"
)

// main is the CLI entry-point
func main() {
	var cfgPath string
	var noResume bool
	var worktree string

	flag.StringVar(&cfgPath, "config", "", "Path to config.json")
	flag.BoolVar(&noResume, "no-resume", false, "Skip resume flow and open setup")
	flag.StringVar(&worktree, "worktree", "", "Create a git worktree for the session (harness must support it)")
	flag.Parse()

	a := app.New(os.Stdin, os.Stdout, os.Stderr, app.Options{
		Version:  version,
		Commit:   commit,
		NoResume: noResume,
		Config:   cfgPath,
		Worktree: worktree,
	})

	if err := a.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "bifrost: %v\n", err)
		os.Exit(1)
	}
}
