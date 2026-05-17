package sqldialect_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/maximhq/bifrost/cmd/bifrostlint/analyzers/sqldialect"
)

func TestSqldialect(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), sqldialect.Analyzer, "dialect")
}
