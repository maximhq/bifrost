package generic

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func notes(out *UsagePreview) string { return strings.Join(out.Notes, "\n") }

// TestPreviewUsage pins what the mapping preview reports for a sample call: the route it matched,
// whether the call counts, the quantity before and after rounding, and the notes that explain an
// empty field. It runs the same code the relay uses and sends nothing.
func TestPreviewUsage(t *testing.T) {
	cfg := &schemas.GenericProviderConfig{
		Auth:         schemas.GenericAuthConfig{Type: schemas.GenericAuthAWSSigV4, AWS: &schemas.GenericAWSAuthConfig{Service: "textract"}},
		Usage:        &schemas.GenericUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015)},
		StrictRoutes: true,
		Routes: []schemas.GenericRouteConfig{
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}, Usage: &schemas.GenericUsageConfig{CostPerUnit: schemas.Ptr(0.05)}},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}, Billable: schemas.Ptr(false)},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.*"}},
		},
	}
	textract := UsagePreviewInput{Method: "POST", Path: "/", RequestContentType: "application/x-amz-json-1.1", RequestBody: `{}`, ResponseContentType: "application/x-amz-json-1.1", ResponseBody: `{"DocumentMetadata":{"Pages":7}}`}

	t.Run("header picks the operation's rate", func(t *testing.T) {
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}
		out, err := PreviewUsage(cfg, in)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.Equal(t, 0, *out.RouteIndex)
		assert.True(t, out.Counted)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 7, *out.Units, 0)
		assert.InDelta(t, 7*0.05, out.Cost, 1e-9, "the route's rate over the provider's units path")
	})
	t.Run("result polls are relayed but not counted", func(t *testing.T) {
		in := textract
		in.Headers = map[string]string{"x-amz-target": "Textract.GetDocumentAnalysis"}
		out, err := PreviewUsage(cfg, in)
		require.NoError(t, err)
		assert.Equal(t, 1, *out.RouteIndex, "header names are matched case-insensitively")
		assert.False(t, out.Billable)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "not billable")
	})
	t.Run("an unlisted operation is refused under strict routes", func(t *testing.T) {
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Translate.TranslateText"}
		out, err := PreviewUsage(cfg, in)
		require.NoError(t, err)
		assert.Nil(t, out.RouteIndex)
		assert.False(t, out.RouteAllowed)
		assert.Contains(t, notes(out), "strict_routes")
	})
	t.Run("rounding shows raw and billed units", func(t *testing.T) {
		rounded := &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, UnitsBlockSize: schemas.Ptr(100.0), UnitsMinimum: schemas.Ptr(1.0), CostPerUnit: schemas.Ptr(0.01)}}
		out, err := PreviewUsage(rounded, UsagePreviewInput{Method: "POST", Path: "/", RequestContentType: "application/json", RequestBody: `{"Text":"hello"}`, ResponseBody: `{}`})
		require.NoError(t, err)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 5, *out.Units, 0)
		require.NotNil(t, out.UnitsBilled)
		assert.InDelta(t, 1, *out.UnitsBilled, 0, "five characters start one block")
		assert.InDelta(t, 100, out.UnitsBlockSize, 0)
		assert.InDelta(t, 0.01, out.Cost, 1e-9)
	})
	t.Run("a wrong path is explained, not silent", func(t *testing.T) {
		out, err := PreviewUsage(&schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "Pages", CostPerUnit: schemas.Ptr(0.1)}}, UsagePreviewInput{Method: "POST", Path: "/x", ResponseBody: `{"DocumentMetadata":{"Pages":7}}`})
		require.NoError(t, err)
		assert.Nil(t, out.Units)
		assert.InDelta(t, 0, out.Cost, 0)
		assert.Contains(t, notes(out), "at path Pages")
		assert.Contains(t, notes(out), "cost is 0")
	})
	t.Run("invalid config is refused", func(t *testing.T) {
		_, err := PreviewUsage(&schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: "magic"}}, UsagePreviewInput{Method: "POST", Path: "/"})
		require.Error(t, err)
	})
}
