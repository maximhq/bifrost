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

	t.Run("a streamed reply priced by its size is billed like a buffered one", func(t *testing.T) {
		sized := &schemas.GenericProviderConfig{
			Usage:  &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.5), UnitsSource: schemas.GenericUnitsFromResponse, UnitsMeasure: schemas.GenericUnitsBodyBytes, CostPerUnit: schemas.Ptr(0.001)},
			Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/tts", Stream: schemas.Ptr(true)}},
		}
		out, err := PreviewUsage(sized, nil, UsagePreviewInput{Method: "POST", Path: "/tts", RequestBody: `{"text":"hi"}`, ResponseContentType: "audio/mpeg", ResponseBody: "ID3\x00\x00binary"}, false)
		require.NoError(t, err)
		require.True(t, out.Streams)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 11, *out.Units, 0, "every forwarded byte")
		assert.InDelta(t, 0.5+0.011, out.Cost, 1e-9)

		sized.Usage.UnitsMeasure = schemas.GenericUnitsBodyChars
		sse := "data: {\"delta\":\"h\xc3\xa9\"}\n\ndata: [DONE]\n\n"
		out, err = PreviewUsage(sized, nil, UsagePreviewInput{Method: "POST", Path: "/tts", RequestBody: `{}`, ResponseContentType: "text/event-stream", ResponseBody: sse}, false)
		require.NoError(t, err)
		require.True(t, out.Streams)
		require.NotNil(t, out.Units)
		assert.InDelta(t, float64(len(sse)-1), *out.Units, 0, "runes of the whole SSE body, the two-byte é counted once")
	})

	t.Run("header picks the operation's rate", func(t *testing.T) {
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}
		out, err := PreviewUsage(cfg, nil, in, false)
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
		out, err := PreviewUsage(cfg, nil, in, false)
		require.NoError(t, err)
		assert.Equal(t, 1, *out.RouteIndex, "header names are matched case-insensitively")
		assert.False(t, out.Billable)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "not billable")
	})
	t.Run("a query is split off the path and ?stream=true counts only under detect_from_body", func(t *testing.T) {
		polls := &schemas.GenericProviderConfig{Routes: []schemas.GenericRouteConfig{{Method: "GET", Path: "/jobs/*", Billable: schemas.Ptr(true)}}}
		out, err := PreviewUsage(polls, nil, UsagePreviewInput{Method: "GET", Path: "/jobs/1?stream=true"}, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex, "the route is matched on the path without its query")
		assert.False(t, out.Streams, "a caller's ?stream=true is not a signal unless the operator opted in")
		polls.Streaming = &schemas.GenericStreamingConfig{DetectFromBody: true}
		out, err = PreviewUsage(polls, nil, UsagePreviewInput{Method: "GET", Path: "/jobs/1?stream=true"}, false)
		require.NoError(t, err)
		assert.True(t, out.Streams, "with detect_from_body the preview streams as the relay would")
		out, err = PreviewUsage(polls, nil, UsagePreviewInput{Method: "POST", Path: "/jobs/1?stream=true"}, false)
		require.NoError(t, err)
		assert.False(t, out.Streams, "the query flag is read on GET only, as in the transport")
	})
	t.Run("routes match on the operator's extra headers over the sample's, as on the relay", func(t *testing.T) {
		extra := map[string]schemas.SecretVar{"X-Amz-Target": *schemas.NewSecretVar("Textract.AnalyzeDocument")}
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}
		out, err := PreviewUsage(cfg, extra, in, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.Equal(t, 0, *out.RouteIndex, "the operator's X-Amz-Target picks the route, not the sample's")
	})
	t.Run("the sample path is decoded before matching, as the transport decodes it", func(t *testing.T) {
		files := &schemas.GenericProviderConfig{StrictRoutes: true, Routes: []schemas.GenericRouteConfig{{Method: "GET", Path: "/files/*"}}}
		out, err := PreviewUsage(files, nil, UsagePreviewInput{Method: "GET", Path: "/files/a%2Fb"}, false)
		require.NoError(t, err)
		assert.False(t, out.RouteAllowed, "a%2Fb is a/b on the relay, which /files/* does not match")
		_, err = PreviewUsage(files, nil, UsagePreviewInput{Method: "GET", Path: "/files/%zz"}, false)
		require.Error(t, err)
	})
	t.Run("a Content-Type sample header is the content type the body is read under", func(t *testing.T) {
		optIn := &schemas.GenericProviderConfig{Streaming: &schemas.GenericStreamingConfig{DetectFromBody: true}}
		in := UsagePreviewInput{Method: "POST", Path: "/chat", Headers: map[string]string{"Content-Type": "text/plain"}, RequestBody: `{"stream":true}`}
		out, err := PreviewUsage(optIn, nil, in, false)
		require.NoError(t, err)
		assert.False(t, out.Streams, "the relay reads the declared text/plain, not a sniffed JSON body, so it stays buffered")
		in.Headers = nil
		out, err = PreviewUsage(optIn, nil, in, false)
		require.NoError(t, err)
		assert.True(t, out.Streams, "with no declared type the body is sniffed, as in the transport")
	})
	t.Run("the Accept header is read from the headers map when the field is empty", func(t *testing.T) {
		sse := &schemas.GenericProviderConfig{Streaming: &schemas.GenericStreamingConfig{DetectFromAcceptHeader: true}}
		out, err := PreviewUsage(sse, nil, UsagePreviewInput{Method: "POST", Path: "/chat", Headers: map[string]string{"Accept": "text/event-stream"}}, false)
		require.NoError(t, err)
		assert.True(t, out.Streams, "the UI sends Accept among the sample headers")
		out, err = PreviewUsage(sse, nil, UsagePreviewInput{Method: "POST", Path: "/chat", Accept: "text/event-stream"}, false)
		require.NoError(t, err)
		assert.True(t, out.Streams)
	})
	t.Run("one Accept decides routes and streaming when the header and the field disagree", func(t *testing.T) {
		sse := &schemas.GenericProviderConfig{Streaming: &schemas.GenericStreamingConfig{DetectFromAcceptHeader: true}}
		out, err := PreviewUsage(sse, nil, UsagePreviewInput{Method: "POST", Path: "/chat", Headers: map[string]string{"Accept": "application/json"}, Accept: "text/event-stream"}, false)
		require.NoError(t, err)
		assert.False(t, out.Streams, "the sample's Accept header is what the relay would see")
	})
	t.Run("an SSE response is read per event as the relay reads it", func(t *testing.T) {
		llm := &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{CostPerRequest: schemas.Ptr(0.01)}, Streaming: &schemas.GenericStreamingConfig{DetectFromAcceptHeader: true}}
		body := "data: {\"usage\":{\"input_tokens\":12}}\n\ndata: {\"delta\":\"hi\"}\n\ndata: {\"usage\":{\"output_tokens\":5,\"cost\":0.5}}\n\ndata: [DONE]\n\n"
		out, err := PreviewUsage(llm, nil, UsagePreviewInput{Method: "POST", Path: "/chat", Accept: "text/event-stream", ResponseContentType: "text/event-stream", ResponseBody: body}, false)
		require.NoError(t, err)
		assert.True(t, out.Streams)
		require.NotNil(t, out.Tokens)
		assert.Equal(t, 12, out.Tokens.PromptTokens)
		assert.Equal(t, 5, out.Tokens.CompletionTokens)
		assert.Equal(t, 17, out.Tokens.TotalTokens, "fields merge across events")
		require.NotNil(t, out.ExtractedCost)
		assert.InDelta(t, 0.5, *out.ExtractedCost, 1e-9)
		assert.InDelta(t, 0.51, out.Cost, 1e-9)
		assert.Contains(t, notes(out), "4 events, 2 carried")
	})
	t.Run("an ignored upstream cost is named, not reported as missing", func(t *testing.T) {
		withCost := &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01)}}
		out, err := PreviewUsage(withCost, nil, UsagePreviewInput{Method: "POST", Path: "/x", ResponseContentType: "application/json", ResponseBody: `{"usage":{"cost":9}}`}, true)
		require.NoError(t, err)
		assert.Nil(t, out.ExtractedCost)
		assert.InDelta(t, 0.01, out.Cost, 1e-9)
		assert.Contains(t, notes(out), "ignore_provider_cost is on")
		assert.NotContains(t, notes(out), "nothing at cost path")
	})
	t.Run("a request-billed body the relay would refuse is reported, not priced", func(t *testing.T) {
		translate := &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.001)}}
		out, err := PreviewUsage(translate, nil, UsagePreviewInput{Method: "POST", Path: "/t", RequestBody: `{"Text":"a","text":"bbbb"}`}, false)
		require.NoError(t, err)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "refuses this request with 400")
		out, err = PreviewUsage(translate, nil, UsagePreviewInput{Method: "POST", Path: "/t", RequestContentType: "text/plain", RequestBody: `{"Text":"hello"}`}, false)
		require.NoError(t, err)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 5, *out.Units, 0, "measured as JSON whatever the declared content type, as the relay does")
	})
	t.Run("the preview refuses what the relay refuses, in the relay's order", func(t *testing.T) {
		plain := &schemas.GenericProviderConfig{}
		out, err := PreviewUsage(plain, nil, UsagePreviewInput{Method: "POST", Path: "/x", RequestBody: `{"model":"a","Model":"b"}`}, false)
		require.NoError(t, err)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "refuses this request with 400")
		translate := &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, CostPerUnit: schemas.Ptr(0.001)}}
		out, err = PreviewUsage(translate, nil, UsagePreviewInput{Method: "POST", Path: "/t", RequestBody: `not json`, StatusCode: 500}, false)
		require.NoError(t, err)
		assert.Contains(t, notes(out), "refuses this request with 400", "the body refusal comes before the sample's status")
	})
	t.Run("a buffered call does not read SSE events, and a stream flag in an untyped JSON body counts", func(t *testing.T) {
		buffered := &schemas.GenericProviderConfig{}
		out, err := PreviewUsage(buffered, nil, UsagePreviewInput{Method: "POST", Path: "/x", ResponseContentType: "text/event-stream", ResponseBody: "data: {\"usage\":{\"cost\":0.5}}\n\n"}, false)
		require.NoError(t, err)
		assert.False(t, out.Streams)
		assert.Nil(t, out.ExtractedCost, "the buffered relay reads no cost from an SSE body")
		optIn := &schemas.GenericProviderConfig{Streaming: &schemas.GenericStreamingConfig{DetectFromBody: true}}
		out, err = PreviewUsage(optIn, nil, UsagePreviewInput{Method: "POST", Path: "/x", RequestBody: `{"stream":true}`}, false)
		require.NoError(t, err)
		assert.True(t, out.Streams, "no declared content type sniffs JSON, as the transport does")
	})
	t.Run("the sample's content type matches a route that requires it, as the header would", func(t *testing.T) {
		typed := &schemas.GenericProviderConfig{StrictRoutes: true, Routes: []schemas.GenericRouteConfig{{Method: "POST", Path: "/x", Headers: map[string]string{"Content-Type": "application/json"}}}}
		out, err := PreviewUsage(typed, nil, UsagePreviewInput{Method: "POST", Path: "/x", RequestContentType: "application/json", RequestBody: `{}`}, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.True(t, out.RouteAllowed)
	})
	t.Run("an unlisted operation is refused under strict routes", func(t *testing.T) {
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Translate.TranslateText"}
		out, err := PreviewUsage(cfg, nil, in, false)
		require.NoError(t, err)
		assert.Nil(t, out.RouteIndex)
		assert.False(t, out.RouteAllowed)
		assert.Contains(t, notes(out), "strict_routes")
	})
	t.Run("rounding shows raw and billed units", func(t *testing.T) {
		rounded := &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "Text", UnitsSource: schemas.GenericUnitsFromRequest, UnitsMeasure: schemas.GenericUnitsChars, UnitsBlockSize: schemas.Ptr(100.0), UnitsMinimum: schemas.Ptr(1.0), CostPerUnit: schemas.Ptr(0.01)}}
		out, err := PreviewUsage(rounded, nil, UsagePreviewInput{Method: "POST", Path: "/", RequestContentType: "application/json", RequestBody: `{"Text":"hello"}`, ResponseBody: `{}`}, false)
		require.NoError(t, err)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 5, *out.Units, 0)
		require.NotNil(t, out.UnitsBilled)
		assert.InDelta(t, 1, *out.UnitsBilled, 0, "five characters start one block")
		assert.InDelta(t, 100, out.UnitsBlockSize, 0)
		assert.InDelta(t, 0.01, out.Cost, 1e-9)
	})
	t.Run("a wrong path is explained, not silent", func(t *testing.T) {
		out, err := PreviewUsage(&schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{UnitsPath: "Pages", CostPerUnit: schemas.Ptr(0.1)}}, nil, UsagePreviewInput{Method: "POST", Path: "/x", ResponseBody: `{"DocumentMetadata":{"Pages":7}}`}, false)
		require.NoError(t, err)
		assert.Nil(t, out.Units)
		assert.InDelta(t, 0, out.Cost, 0)
		assert.Contains(t, notes(out), "at path Pages")
		assert.Contains(t, notes(out), "cost is 0")
	})
	t.Run("ignore_provider_cost drops the upstream-reported cost, as the relay does", func(t *testing.T) {
		cfg := &schemas.GenericProviderConfig{Usage: &schemas.GenericUsageConfig{CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01)}}
		in := UsagePreviewInput{Method: "POST", Path: "/x", ResponseBody: `{"usage":{"cost":0.5}}`}
		trusting, err := PreviewUsage(cfg, nil, in, false)
		require.NoError(t, err)
		assert.InDelta(t, 0.51, trusting.Cost, 1e-9)
		ignoring, err := PreviewUsage(cfg, nil, in, true)
		require.NoError(t, err)
		assert.Nil(t, ignoring.ExtractedCost)
		assert.InDelta(t, 0.01, ignoring.Cost, 1e-9, "only the operator's own fee remains")
	})
	t.Run("invalid config is refused", func(t *testing.T) {
		_, err := PreviewUsage(&schemas.GenericProviderConfig{Auth: schemas.GenericAuthConfig{Type: "magic"}}, nil, UsagePreviewInput{Method: "POST", Path: "/"}, false)
		require.Error(t, err)
	})
}
