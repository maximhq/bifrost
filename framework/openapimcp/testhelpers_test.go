package openapimcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

func parseFixture(t *testing.T, name string) *Document {
	t.Helper()
	doc, err := Parse(fixture(t, name), ParseOptions{})
	require.NoError(t, err, "parse %s", name)
	return doc
}

func findOp(t *testing.T, doc *Document, method, path string) Operation {
	t.Helper()
	for _, op := range doc.Operations {
		if op.Method == method && op.Path == path {
			return op
		}
	}
	t.Fatalf("operation %s %s not found", method, path)
	return Operation{}
}

func findTool(t *testing.T, syn *Synthesis, name string) *Tool {
	t.Helper()
	for i := range syn.Tools {
		if syn.Tools[i].Name == name {
			return &syn.Tools[i]
		}
	}
	t.Fatalf("tool %q not found in %v", name, syn.ToolNames())
	return nil
}

func str(s string) *string { return &s }
