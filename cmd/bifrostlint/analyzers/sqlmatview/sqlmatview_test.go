package sqlmatview_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/maximhq/bifrost/cmd/bifrostlint/analyzers/sqlmatview"
)

func TestSqlmatview(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), sqlmatview.Analyzer, "matview")
}
