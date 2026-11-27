package relay

import (
	"encoding/json"
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
	cfg := &schemas.RelayConfig{
		Auth:  schemas.RelayAuthConfig{Type: schemas.RelayAuthAWSSigV4, AWS: &schemas.RelayAWSAuthConfig{Service: "textract"}},
		Usage: &schemas.RelayUsageConfig{UnitsPath: "DocumentMetadata.Pages", CostPerUnit: schemas.Ptr(0.0015)},
		Routes: []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}, Usage: &schemas.RelayUsageConfig{CostPerUnit: schemas.Ptr(0.05)}},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.Get*"}, Billable: schemas.Ptr(false)},
			{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.*"}},
		},
	}
	textract := UsagePreviewInput{Method: "POST", Path: "/", RequestContentType: "application/x-amz-json-1.1", RequestBody: `{}`, ResponseContentType: "application/x-amz-json-1.1", ResponseBody: `{"DocumentMetadata":{"Pages":7}}`}

	t.Run("a deeply nested response body is refused before the recursive JSON check", func(t *testing.T) {
		deep := strings.Repeat("[", 2_000_000) + strings.Repeat("]", 2_000_000)
		_, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "POST", Path: "/", RequestBody: `{}`, ResponseBody: deep}, false)
		require.ErrorContains(t, err, "response_content_type is required")
	})

	t.Run("a streamed reply priced by its size is billed like a buffered one", func(t *testing.T) {
		sized := &schemas.RelayConfig{
			Usage:  &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.5), UnitsSource: schemas.RelayUnitsFromResponse, UnitsMeasure: schemas.RelayUnitsBodyBytes, CostPerUnit: schemas.Ptr(0.001)},
			Routes: []schemas.RelayRouteConfig{{Method: "POST", Path: "/tts", Stream: schemas.Ptr(true)}},
		}
		out, err := previewUsage(sized, nil, UsagePreviewInput{Method: "POST", Path: "/tts", RequestBody: `{"text":"hi"}`, ResponseContentType: "audio/mpeg", ResponseBody: "ID3\x00\x00binary"}, false)
		require.NoError(t, err)
		require.True(t, out.Streams)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 11, *out.Units, 0, "every forwarded byte")
		assert.InDelta(t, 0.5+0.011, out.Cost, 1e-9)

		sized.Usage.UnitsMeasure = schemas.RelayUnitsBodyChars
		sse := "data: {\"delta\":\"h\xc3\xa9\"}\n\ndata: [DONE]\n\n"
		out, err = previewUsage(sized, nil, UsagePreviewInput{Method: "POST", Path: "/tts", RequestBody: `{}`, ResponseContentType: "text/event-stream", ResponseBody: sse}, false)
		require.NoError(t, err)
		require.True(t, out.Streams)
		require.NotNil(t, out.Units)
		assert.InDelta(t, float64(len(sse)-1), *out.Units, 0, "runes of the whole SSE body, the two-byte é counted once")
	})

	t.Run("header picks the operation's rate", func(t *testing.T) {
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Textract.AnalyzeDocument"}
		out, err := previewUsage(cfg, nil, in, false)
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
		out, err := previewUsage(cfg, nil, in, false)
		require.NoError(t, err)
		assert.Equal(t, 1, *out.RouteIndex, "header names are matched case-insensitively")
		assert.False(t, out.Billable)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "not billable")
	})
	t.Run("a query is split off the path and ?stream=true never selects streaming", func(t *testing.T) {
		polls := &schemas.RelayConfig{SkipRouteCheck: true, Routes: []schemas.RelayRouteConfig{{Method: "GET", Path: "/jobs/*", Billable: schemas.Ptr(true)}}}
		out, err := previewUsage(polls, nil, UsagePreviewInput{Method: "GET", Path: "/jobs/1?stream=true", ResponseContentType: "application/json"}, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex, "the route is matched on the path without its query")
		assert.False(t, out.Streams, "a JSON reply is read and priced whatever the caller asked for")
	})
	t.Run("routes match on the operator's extra headers over the sample's, as on the relay", func(t *testing.T) {
		extra := map[string]schemas.SecretVar{"X-Amz-Target": *schemas.NewSecretVar("Textract.AnalyzeDocument")}
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}
		out, err := previewUsage(cfg, extra, in, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.Equal(t, 0, *out.RouteIndex, "the operator's X-Amz-Target is what is sent, so it picks the route whatever the sample says")
		in.Headers = nil
		out, err = previewUsage(cfg, extra, in, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.Equal(t, 0, *out.RouteIndex, "with no sample copy the operator's X-Amz-Target picks the route")
	})
	t.Run("under sigv4 an x-amz header the upstream never receives selects no route, as on the relay", func(t *testing.T) {
		s3 := &schemas.RelayConfig{
			Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthAWSSigV4, AWS: &schemas.RelayAWSAuthConfig{Service: "s3"}},
			Routes: []schemas.RelayRouteConfig{
				{Method: "PUT", Path: "/**", Headers: map[string]string{"X-Amz-Storage-Class": "GLACIER"}, Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.02)}},
				{Method: "PUT", Path: "/**", Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01)}},
			},
		}
		require.NoError(t, s3.Validate())
		out, err := previewUsage(s3, nil, UsagePreviewInput{Method: "PUT", Path: "/bucket/key", Headers: map[string]string{"X-Amz-Storage-Class": "GLACIER"}}, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.Equal(t, 1, *out.RouteIndex, "the caller's x-amz-storage-class is dropped under SigV4, so it picks nothing")
	})
	t.Run("the sample path is decoded before matching, as the transport decodes it", func(t *testing.T) {
		files := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{{Method: "GET", Path: "/files/*"}}}
		out, err := previewUsage(files, nil, UsagePreviewInput{Method: "GET", Path: "/files/a%2Fb"}, false)
		require.NoError(t, err)
		assert.False(t, out.RouteAllowed, "a%2Fb is a/b on the relay, which /files/* does not match")
		_, err = previewUsage(files, nil, UsagePreviewInput{Method: "GET", Path: "/files/%zz"}, false)
		require.Error(t, err)
	})
	t.Run("the reply's content type decides streaming, not the caller's Accept or stream flag", func(t *testing.T) {
		auto := &schemas.RelayConfig{SkipRouteCheck: true}
		caller := UsagePreviewInput{Method: "POST", Path: "/chat", Headers: map[string]string{"Accept": "text/event-stream"}, RequestBody: `{"stream":true}`}
		in := caller
		in.ResponseContentType = "application/json"
		out, err := previewUsage(auto, nil, in, false)
		require.NoError(t, err)
		assert.False(t, out.Streams, "a JSON reply is buffered and priced even when the caller asked for SSE")
		in.ResponseContentType = "text/event-stream"
		out, err = previewUsage(auto, nil, in, false)
		require.NoError(t, err)
		assert.True(t, out.Streams, "an event-stream reply is streamed")
	})
	t.Run("an SSE response is read per event as the relay reads it", func(t *testing.T) {
		llm := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{InputTokensPath: "usage.input_tokens", OutputTokensPath: "usage.output_tokens", CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01)}}
		body := "data: {\"usage\":{\"input_tokens\":12}}\n\ndata: {\"delta\":\"hi\"}\n\ndata: {\"usage\":{\"output_tokens\":5,\"cost\":0.5}}\n\ndata: [DONE]\n\n"
		out, err := previewUsage(llm, nil, UsagePreviewInput{Method: "POST", Path: "/chat", Accept: "text/event-stream", ResponseContentType: "text/event-stream", ResponseBody: body}, false)
		require.NoError(t, err)
		assert.True(t, out.Streams)
		require.NotNil(t, out.Tokens)
		assert.Equal(t, 12, out.Tokens.Input)
		assert.Equal(t, 5, out.Tokens.Output)
		assert.Equal(t, 17, out.Tokens.Total, "fields merge across events")
		require.NotNil(t, out.ExtractedCost)
		assert.InDelta(t, 0.5, *out.ExtractedCost, 1e-9)
		assert.InDelta(t, 0.51, out.Cost, 1e-9)
		assert.Contains(t, notes(out), "4 events, 2 carried")
	})
	t.Run("an ignored upstream cost is named, not reported as missing", func(t *testing.T) {
		withCost := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01)}}
		out, err := previewUsage(withCost, nil, UsagePreviewInput{Method: "POST", Path: "/x", ResponseContentType: "application/json", ResponseBody: `{"usage":{"cost":9}}`}, true)
		require.NoError(t, err)
		assert.Nil(t, out.ExtractedCost)
		assert.InDelta(t, 0.01, out.Cost, 1e-9)
		assert.Contains(t, notes(out), "ignore_upstream_cost is on")
		assert.NotContains(t, notes(out), "nothing at cost path")
	})
	t.Run("a request-billed body the relay would refuse is reported, not priced", func(t *testing.T) {
		translate := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{UnitsPath: "Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(0.001)}}
		out, err := previewUsage(translate, nil, UsagePreviewInput{Method: "POST", Path: "/t", RequestBody: `{"Text":"a","text":"bbbb"}`}, false)
		require.NoError(t, err)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "refuses this request with 400")
		out, err = previewUsage(translate, nil, UsagePreviewInput{Method: "POST", Path: "/t", RequestContentType: "text/plain", RequestBody: `{"Text":"hello"}`}, false)
		require.NoError(t, err)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 5, *out.Units, 0, "measured as JSON whatever the declared content type, as the relay does")
	})
	t.Run("a model read from the query", func(t *testing.T) {
		cfg := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{ModelPath: "model", ModelSource: schemas.RelayModelFromQuery}}
		out, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "POST", Path: "/v1/listen?model=nova-3"}, false)
		require.NoError(t, err)
		assert.Equal(t, "nova-3", out.Model)
		assert.Equal(t, schemas.RelayModelFromQuery, out.ModelSource)
		out, err = previewUsage(cfg, nil, UsagePreviewInput{Method: "POST", Path: "/v1/listen?model=a&model=b"}, false)
		require.NoError(t, err)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "refuses this request with 400")
	})
	t.Run("the preview refuses what the relay refuses, in the relay's order", func(t *testing.T) {
		// A model named twice is refused wherever the model is read: here a mapped model field.
		plain := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{ModelPath: "model"}}
		out, err := previewUsage(plain, nil, UsagePreviewInput{Method: "POST", Path: "/x", RequestBody: `{"model":"a","Model":"b"}`}, false)
		require.NoError(t, err)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "refuses this request with 400")
		translate := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{UnitsPath: "Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(0.001)}}
		out, err = previewUsage(translate, nil, UsagePreviewInput{Method: "POST", Path: "/t", RequestBody: `not json`, StatusCode: 500}, false)
		require.NoError(t, err)
		assert.Contains(t, notes(out), "refuses this request with 400", "the body refusal comes before the sample's status")
	})
	t.Run("a route forced to buffer does not read SSE events", func(t *testing.T) {
		buffered := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{{Method: "POST", Path: "/x", Stream: schemas.Ptr(false)}}}
		out, err := previewUsage(buffered, nil, UsagePreviewInput{Method: "POST", Path: "/x", ResponseContentType: "text/event-stream", ResponseBody: "data: {\"usage\":{\"cost\":0.5}}\n\n"}, false)
		require.NoError(t, err)
		assert.False(t, out.Streams)
		assert.Nil(t, out.ExtractedCost, "the buffered relay reads no cost from an SSE body")
	})
	t.Run("the sample's content type matches a route that requires it, as the header would", func(t *testing.T) {
		typed := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{{Method: "POST", Path: "/x", Headers: map[string]string{"Content-Type": "application/json"}}}}
		out, err := previewUsage(typed, nil, UsagePreviewInput{Method: "POST", Path: "/x", RequestContentType: "application/json", RequestBody: `{}`}, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.True(t, out.RouteAllowed)
	})
	t.Run("an unlisted operation is refused by the route check", func(t *testing.T) {
		in := textract
		in.Headers = map[string]string{"X-Amz-Target": "Translate.TranslateText"}
		out, err := previewUsage(cfg, nil, in, false)
		require.NoError(t, err)
		assert.Nil(t, out.RouteIndex)
		assert.False(t, out.RouteAllowed)
		assert.Contains(t, notes(out), "skip_route_check")
	})
	t.Run("rounding shows raw and billed units", func(t *testing.T) {
		rounded := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{UnitsPath: "Text", UnitsSource: schemas.RelayUnitsFromRequest, UnitsMeasure: schemas.RelayUnitsChars, UnitsBlockSize: schemas.Ptr(100.0), UnitsMinimum: schemas.Ptr(1.0), CostPerUnit: schemas.Ptr(0.01)}}
		out, err := previewUsage(rounded, nil, UsagePreviewInput{Method: "POST", Path: "/", RequestContentType: "application/json", RequestBody: `{"Text":"hello"}`, ResponseBody: `{}`}, false)
		require.NoError(t, err)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 5, *out.Units, 0)
		require.NotNil(t, out.UnitsBilled)
		assert.InDelta(t, 1, *out.UnitsBilled, 0, "five characters start one block")
		assert.InDelta(t, 100, out.UnitsBlockSize, 0)
		assert.InDelta(t, 0.01, out.Cost, 1e-9)
	})
	t.Run("a wrong path is explained, not silent", func(t *testing.T) {
		out, err := previewUsage(&schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{UnitsPath: "Pages", CostPerUnit: schemas.Ptr(0.1)}}, nil, UsagePreviewInput{Method: "POST", Path: "/x", ResponseBody: `{"DocumentMetadata":{"Pages":7}}`}, false)
		require.NoError(t, err)
		assert.Nil(t, out.Units)
		assert.InDelta(t, 0, out.Cost, 0)
		assert.Contains(t, notes(out), "at path Pages")
		assert.Contains(t, notes(out), "cost is 0")
	})
	t.Run("ignore_provider_cost drops the upstream-reported cost, as the relay does", func(t *testing.T) {
		cfg := &schemas.RelayConfig{SkipRouteCheck: true, Usage: &schemas.RelayUsageConfig{CostPath: "usage.cost", CostPerRequest: schemas.Ptr(0.01)}}
		in := UsagePreviewInput{Method: "POST", Path: "/x", ResponseBody: `{"usage":{"cost":0.5}}`}
		trusting, err := previewUsage(cfg, nil, in, false)
		require.NoError(t, err)
		assert.InDelta(t, 0.51, trusting.Cost, 1e-9)
		ignoring, err := previewUsage(cfg, nil, in, true)
		require.NoError(t, err)
		assert.Nil(t, ignoring.ExtractedCost)
		assert.InDelta(t, 0.01, ignoring.Cost, 1e-9, "only the operator's own fee remains")
	})
	t.Run("forced body fields are measured and read as the relay sends them", func(t *testing.T) {
		cfg := &schemas.RelayConfig{
			BodyOverrides: map[string]json.RawMessage{"model": json.RawMessage(`"tavily-basic"`)},
			Usage:         &schemas.RelayUsageConfig{ModelPath: "model"},
			Routes: []schemas.RelayRouteConfig{
				{Method: "POST", Path: "/translate", BodyOverrides: map[string]json.RawMessage{"text": json.RawMessage(`"forced text"`)},
					Usage: &schemas.RelayUsageConfig{UnitsSource: schemas.RelayUnitsFromRequest, UnitsPath: "text", UnitsMeasure: schemas.RelayUnitsChars, CostPerUnit: schemas.Ptr(0.01)}},
				{Method: "POST", Path: "/search"},
			},
		}
		out, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "POST", Path: "/translate", RequestBody: `{"text":"hi","model":"mine"}`}, false)
		require.NoError(t, err)
		require.NotNil(t, out.Units)
		assert.InDelta(t, 11, *out.Units, 0, "the forced text is what the relay measures")
		assert.InDelta(t, 0.11, out.Cost, 1e-9)
		assert.Equal(t, "tavily-basic", out.Model, "the route inherits the relay's forced model")

		out, err = previewUsage(cfg, nil, UsagePreviewInput{Method: "POST", Path: "/search"}, false)
		require.NoError(t, err)
		assert.Equal(t, "tavily-basic", out.Model, "an empty POST gets a created body, as on the wire")

		out, err = previewUsage(cfg, nil, UsagePreviewInput{Method: "POST", Path: "/search", RequestContentType: "text/plain", RequestBody: "query=x"}, false)
		require.NoError(t, err)
		assert.False(t, out.Counted)
		assert.Contains(t, notes(out), "refuses this request with 400")
		assert.Contains(t, notes(out), "must be a JSON object")
	})
	t.Run("a sample x-bf-eh-* header routes on the name the upstream receives", func(t *testing.T) {
		modes := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/run", Headers: map[string]string{"X-Mode": "check"}, Billable: schemas.Ptr(false)},
			{Method: "POST", Path: "/run"},
		}}
		out, err := previewUsage(modes, nil, UsagePreviewInput{Method: "POST", Path: "/run", Headers: map[string]string{"X-Bf-Eh-X-Mode": "check"}}, false)
		require.NoError(t, err)
		require.NotNil(t, out.RouteIndex)
		assert.Equal(t, 0, *out.RouteIndex)

		keyed := &schemas.RelayConfig{SkipRouteCheck: true, Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthNone}, Routes: []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/run", Headers: map[string]string{"X-Api-Key": "demo"}, Billable: schemas.Ptr(false)},
		}}
		out, err = previewUsage(keyed, nil, UsagePreviewInput{Method: "POST", Path: "/run", Headers: map[string]string{"X-Bf-Eh-X-Api-Key": "demo"}}, false)
		require.NoError(t, err)
		assert.Nil(t, out.RouteIndex, "a header the gateway drops selects no route in the preview either")
	})
	t.Run("a trailing or doubled slash is refused by a disabled route, as on a live call", func(t *testing.T) {
		files := &schemas.RelayConfig{Auth: schemas.RelayAuthConfig{Type: schemas.RelayAuthNone}, Routes: []schemas.RelayRouteConfig{
			{Method: "GET", Path: "/files/**/secret", Enabled: schemas.Ptr(false)},
			{Method: "GET", Path: "/files/**"},
		}}
		for _, path := range []string{"/files/x/secret/", "/files/x//secret"} {
			out, err := previewUsage(files, nil, UsagePreviewInput{Method: "GET", Path: path}, false)
			require.NoError(t, err)
			assert.False(t, out.RouteAllowed, path)
		}
	})
	t.Run("invalid config is refused", func(t *testing.T) {
		_, err := previewUsage(&schemas.RelayConfig{SkipRouteCheck: true, Auth: schemas.RelayAuthConfig{Type: "magic"}}, nil, UsagePreviewInput{Method: "POST", Path: "/"}, false)
		require.Error(t, err)
	})
}

// previewUsage runs PreviewUsage with the extra headers and cost policy set on a copy of cfg, the
// shape the provider-coupled preview took them in.
func previewUsage(cfg *schemas.RelayConfig, extra map[string]schemas.SecretVar, in UsagePreviewInput, ignoreUpstreamCost bool) (*UsagePreview, error) {
	cfg = cfg.Clone()
	if cfg == nil {
		cfg = &schemas.RelayConfig{}
	}
	cfg.ExtraHeaders = extra
	cfg.IgnoreUpstreamCost = ignoreUpstreamCost
	return PreviewUsage(cfg, in)
}

// The preview matches the path the transport hands a live call: . and .. segments resolved.
func TestPreviewResolvesDotSegmentsAsTheTransportDoes(t *testing.T) {
	cfg := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{{Method: "GET", Path: "/w/x", Billable: schemas.Ptr(true), Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.1)}}}}
	for _, path := range []string{"/w/a/../x", "/w/./x", "/w/x"} {
		out, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "GET", Path: path, ResponseContentType: "application/json"}, false)
		require.NoError(t, err, path)
		require.NotNil(t, out.RouteIndex, "%s is /w/x on the relay", path)
		assert.True(t, out.RouteAllowed, path)
		assert.InDelta(t, 0.1, out.Cost, 1e-12, path)
	}
}

// A sample matching a job's status check is shown as relayed free, as a live call is once the job
// lookup confirms Bifrost started it, instead of the 404 an unrouted call gets.
func TestPreviewShowsAStatusCheckAsFree(t *testing.T) {
	cfg := &schemas.RelayConfig{Routes: []schemas.RelayRouteConfig{{
		Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.StartDocumentAnalysis"},
		Usage: &schemas.RelayUsageConfig{CostPerRequest: schemas.Ptr(0.01)},
		Job: &schemas.RelayJobConfig{IDPath: "JobId", StatusPath: "JobStatus", Succeeded: []string{"SUCCEEDED"},
			Poll: schemas.RelayJobPoll{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"}, Body: `{"JobId":"{{job.id}}"}`, ContentType: "application/x-amz-json-1.1"}},
	}}}
	check := UsagePreviewInput{Method: "POST", Path: "/", Headers: map[string]string{"X-Amz-Target": "Textract.GetDocumentAnalysis"},
		RequestContentType: "application/x-amz-json-1.1", RequestBody: `{"JobId":"j-1"}`, ResponseContentType: "application/json", ResponseBody: `{"JobStatus":"IN_PROGRESS"}`}
	out, err := previewUsage(cfg, nil, check, false)
	require.NoError(t, err)
	assert.True(t, out.StatusCheck)
	assert.True(t, out.RouteAllowed, "relayed, not refused with 404")
	assert.False(t, out.Counted)
	assert.Zero(t, out.Cost)
	assert.Contains(t, strings.Join(out.Notes, " "), "status check")

	start := check
	start.Headers = map[string]string{"X-Amz-Target": "Textract.StartDocumentAnalysis"}
	start.RequestBody = `{"Document":{}}`
	start.ResponseBody = `{"JobId":"j-1"}`
	out, err = previewUsage(cfg, nil, start, false)
	require.NoError(t, err)
	assert.False(t, out.StatusCheck, "the start call is priced as itself")
	assert.InDelta(t, 0.01, out.Cost, 1e-12)
}

// A sample that climbs out of the relay with .. never reaches it on a live call, since the transport
// resolves the whole /relay/<name><path>; it is not matched against this relay's routes.
func TestPreviewRefusesAPathThatClimbsOutOfTheRelay(t *testing.T) {
	cfg := &schemas.RelayConfig{Name: "demo", SkipRouteCheck: true, Routes: []schemas.RelayRouteConfig{{Method: "GET", Path: "/x", Billable: schemas.Ptr(true)}}}
	for _, path := range []string{"/../x", "/../../w/x", "/a/../../x"} {
		out, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "GET", Path: path}, false)
		require.NoError(t, err, path)
		assert.False(t, out.RouteAllowed, path)
		assert.Nil(t, out.RouteIndex, path)
		assert.Contains(t, strings.Join(out.Notes, " "), "climbs out of the relay", path)
	}
	out, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "GET", Path: "/a/../x"}, false)
	require.NoError(t, err)
	require.NotNil(t, out.RouteIndex, "a .. that stays inside the relay still matches")
}

// The preview refuses a status check the gateway would refuse: Serve still reads the model after the
// status check matches, so a model sent twice in the query is a 400, not a free status check.
func TestPreviewStatusCheckStillReadsTheModel(t *testing.T) {
	cfg := &schemas.RelayConfig{
		Usage: &schemas.RelayUsageConfig{ModelPath: "model", ModelSource: schemas.RelayModelFromQuery},
		Routes: []schemas.RelayRouteConfig{{Method: "POST", Path: "/jobs", Job: &schemas.RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"done"},
			Poll: schemas.RelayJobPoll{Method: "GET", Path: "/jobs/{{job.id}}"}}}},
	}
	out, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "GET", Path: "/jobs/j-1?model=a&model=b"}, false)
	require.NoError(t, err)
	assert.False(t, out.StatusCheck, "the gateway refuses it, so it is no free status check")
	assert.True(t, out.RouteAllowed, "admitted, then refused, so the UI shows the 400 rather than no route")
	assert.Contains(t, strings.Join(out.Notes, " "), "refuses this request with 400")
	out, err = previewUsage(cfg, nil, UsagePreviewInput{Method: "GET", Path: "/jobs/j-1?model=a"}, false)
	require.NoError(t, err)
	assert.True(t, out.StatusCheck)
	assert.Equal(t, "a", out.Model, "the model a live status check logs is shown")
}

// As in Serve, a JSON body created for forced fields under the relay's non-JSON Content-Type is refused.
func TestPreviewRefusesACreatedBodyUnderARelayNonJSONContentType(t *testing.T) {
	cfg := &schemas.RelayConfig{SkipRouteCheck: true, BodyOverrides: map[string]json.RawMessage{"include_usage": json.RawMessage("true")}}
	out, err := previewUsage(cfg, map[string]schemas.SecretVar{"Content-Type": *schemas.NewSecretVar("text/plain")}, UsagePreviewInput{Method: "POST", Path: "/search"}, false)
	require.NoError(t, err)
	assert.Contains(t, strings.Join(out.Notes, " "), `Content-Type to "text/plain"`)
}

// A status-check sample whose created body falls under the relay's non-JSON Content-Type is refused,
// as Serve refuses it before its status check, not shown as a free status check.
func TestPreviewRefusesAStatusCheckUnderARelayNonJSONContentType(t *testing.T) {
	cfg := &schemas.RelayConfig{
		BodyOverrides: map[string]json.RawMessage{"tenant": json.RawMessage(`"acme"`)},
		Routes: []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/start", Job: &schemas.RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"done"},
				Poll: schemas.RelayJobPoll{Method: "POST", Path: "/jobs/{{job.id}}"}}},
			{Method: "POST", Path: "/jobs/*"},
		},
	}
	extra := map[string]schemas.SecretVar{"Content-Type": *schemas.NewSecretVar("text/plain")}
	out, err := previewUsage(cfg, extra, UsagePreviewInput{Method: "POST", Path: "/jobs/j-1"}, false)
	require.NoError(t, err)
	assert.False(t, out.StatusCheck)
	assert.Contains(t, strings.Join(out.Notes, " "), "refuses this request with 400")
}

// A status check whose created JSON Content-Type selects a disabled route is refused with 404, as
// Serve's recheck of the call as sent refuses it, not shown as free.
func TestPreviewStatusCheckOnADisabledRouteIsRefused(t *testing.T) {
	cfg := &schemas.RelayConfig{
		BodyOverrides: map[string]json.RawMessage{"tenant": json.RawMessage(`"acme"`)},
		Routes: []schemas.RelayRouteConfig{
			{Method: "POST", Path: "/start", Job: &schemas.RelayJobConfig{IDPath: "id", StatusPath: "status", Succeeded: []string{"done"},
				Poll: schemas.RelayJobPoll{Method: "POST", Path: "/jobs/{{job.id}}"}}},
			{Method: "POST", Path: "/jobs/*", Headers: map[string]string{"Content-Type": "application/json"}, Enabled: schemas.Ptr(false)},
		},
	}
	out, err := previewUsage(cfg, nil, UsagePreviewInput{Method: "POST", Path: "/jobs/j-1"}, false)
	require.NoError(t, err)
	assert.False(t, out.StatusCheck)
	assert.False(t, out.RouteAllowed)
	assert.Contains(t, strings.Join(out.Notes, " "), "disabled")
}
