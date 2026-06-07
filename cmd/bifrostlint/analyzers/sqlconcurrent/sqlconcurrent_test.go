package sqlconcurrent_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/maximhq/bifrost/cmd/bifrostlint/analyzers/sqlconcurrent"
)

func TestSqlconcurrent(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), sqlconcurrent.Analyzer, "concurrent")
}
