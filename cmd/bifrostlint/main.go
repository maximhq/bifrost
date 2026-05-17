// Command bifrostlint runs Bifrost-specific static checks.
//
// Usage:
//
//	bifrostlint ./...                            # runs sqlmatview + sqldialect
//	bifrostlint exportedtests ./...              # checks exported symbols have tests
//	bifrostlint exportedtests -emit-baseline ./... > cmd/bifrostlint/baseline.txt
//
// See AGENTS.md Code Style and cmd/bifrostlint/README for the
// // bifrostlint:ignore directive.
package main

import (
	"os"

	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/maximhq/bifrost/cmd/bifrostlint/analyzers/exportedtests"
	"github.com/maximhq/bifrost/cmd/bifrostlint/analyzers/sqlconcurrent"
	"github.com/maximhq/bifrost/cmd/bifrostlint/analyzers/sqldialect"
	"github.com/maximhq/bifrost/cmd/bifrostlint/analyzers/sqlmatview"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "exportedtests" {
		os.Args = append([]string{os.Args[0]}, os.Args[2:]...)
		os.Exit(exportedtests.Run())
	}
	multichecker.Main(
		sqlconcurrent.Analyzer,
		sqlmatview.Analyzer,
		sqldialect.Analyzer,
	)
}
