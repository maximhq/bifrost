package azure

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// azureChatPromptBlockedBody is Azure's answer to a chat request whose prompt its content
// filter blocked, as captured for a streamed request (vercel/ai#4220).
const azureChatPromptBlockedBody = `{"error":{"message":"The response was filtered due to the prompt triggering Azure OpenAI's content management policy. Please modify your prompt and retry. To learn more about our content filtering policies please read our documentation: https://go.microsoft.com/fwlink/?linkid=2198766","type":null,"param":"prompt","code":"content_filter","status":400,"innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_result":{"hate":{"filtered":true,"severity":"low"},"jailbreak":{"filtered":false,"detected":false},"self_harm":{"filtered":false,"severity":"safe"},"sexual":{"filtered":false,"severity":"safe"},"violence":{"filtered":true,"severity":"medium"}}}}}`

// newAzureErrorResponse builds the reply Azure sends for an error: the status and a JSON error body.
func newAzureErrorResponse(status int, body string) *fasthttp.Response {
	resp := &fasthttp.Response{}
	resp.SetStatusCode(status)
	resp.Header.SetContentType("application/json")
	resp.SetBodyString(body)
	return resp
}

// filteredCategory is how parseAzureError reports a severity-scored category Azure filtered.
func filteredCategory(name, severity string) schemas.ContentFilterCategory {
	return schemas.ContentFilterCategory{Category: name, Severity: schemas.Ptr(severity), Filtered: true}
}

// detectedCategory is how parseAzureError reports a detector, such as jailbreak or profanity, that Azure filtered.
func detectedCategory(name string) schemas.ContentFilterCategory {
	return schemas.ContentFilterCategory{Category: name, Filtered: true, Detected: schemas.Ptr(true)}
}

// TestParseAzureErrorContentFilterDetails pins #8799: a prompt Azure's content filter
// blocked reports the categories that caused the block in Error.ContentFilterDetails,
// read from both of Azure's error shapes, while the rest of the error stays exactly what
// the OpenAI parser produced and nothing else from Azure's classification leaks into it.
func TestParseAzureErrorContentFilterDetails(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   *schemas.ContentFilterDetails
	}{
		{
			name:   "chat prompt block keeps only the filtered categories",
			status: fasthttp.StatusBadRequest,
			body:   azureChatPromptBlockedBody,
			want:   &schemas.ContentFilterDetails{Categories: []schemas.ContentFilterCategory{filteredCategory("hate", "low"), filteredCategory("violence", "medium")}},
		},
		{
			name:   "issue reporter's violence block",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","message":"The response was filtered due to the prompt triggering Azure OpenAI's content management policy.","param":"prompt","innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_result":{"hate":{"filtered":false,"severity":"safe"},"jailbreak":{"detected":false,"filtered":false},"self_harm":{"filtered":false,"severity":"safe"},"sexual":{"filtered":false,"severity":"safe"},"violence":{"filtered":true,"severity":"medium"}}}}}`,
			want:   &schemas.ContentFilterDetails{Categories: []schemas.ContentFilterCategory{filteredCategory("violence", "medium")}},
		},
		{
			name:   "content_filter_results spelling from Azure's API spec",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","message":"The response was filtered","param":"prompt","innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_results":{"sexual":{"filtered":true,"severity":"high"},"violence":{"filtered":false,"severity":"low"}}}}}`,
			want:   &schemas.ContentFilterDetails{Categories: []schemas.ContentFilterCategory{filteredCategory("sexual", "high")}},
		},
		{
			// The Responses API classifies per source; a category repeated across sources is
			// reported once, at its worst severity.
			name:   "responses content_filters list merges categories across sources",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"message":"The response was filtered due to the prompt triggering Azure OpenAI's content management policy.","type":"invalid_request_error","param":"prompt","code":"content_filter","innererror":{"code":"ContentFiltered"},"content_filters":[{"blocked":true,"source_type":"prompt","content_filter_raw":[],"content_filter_results":{"self_harm":{"filtered":true,"severity":"medium"},"jailbreak":{"detected":false,"filtered":false}},"content_filter_offsets":{"start_offset":0,"end_offset":120,"check_offset":0}},{"blocked":true,"source_type":"prompt","content_filter_results":{"self_harm":{"filtered":true,"severity":"high"},"jailbreak":{"detected":true,"filtered":true}}}]}}`,
			want:   &schemas.ContentFilterDetails{Categories: []schemas.ContentFilterCategory{filteredCategory("self_harm", "high"), detectedCategory("jailbreak")}},
		},
		{
			name:   "prompt shield block has no severity",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","message":"The response was filtered","param":"prompt","innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_result":{"hate":{"filtered":false,"severity":"safe"},"jailbreak":{"detected":true,"filtered":true}}}}}`,
			want:   &schemas.ContentFilterDetails{Categories: []schemas.ContentFilterCategory{detectedCategory("jailbreak")}},
		},
		{
			name:   "blocklist hits report the category, never the blocklist name",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","message":"The response was filtered","param":"prompt","innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_result":{"custom_blocklists":{"filtered":true,"details":[{"filtered":true,"id":"internal-codenames"}]},"profanity":{"detected":true,"filtered":true}}}}}`,
			want:   &schemas.ContentFilterDetails{Categories: []schemas.ContentFilterCategory{{Category: "custom_blocklists", Filtered: true}, detectedCategory("profanity")}},
		},
		{
			name:   "legacy custom_blocklists list form",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","message":"The response was filtered","param":"prompt","innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_result":{"custom_blocklists":[{"filtered":false,"id":"allowed-terms"},{"filtered":true,"id":"internal-codenames"}]}}}}`,
			want:   &schemas.ContentFilterDetails{Categories: []schemas.ContentFilterCategory{{Category: "custom_blocklists", Filtered: true}}},
		},
		{
			name:   "content_filter error without a classification falls back to the plain error",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"message":"The response was filtered","type":null,"param":"prompt","code":"content_filter","status":400}}`,
		},
		{
			name:   "filter service error object is not a category",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","message":"The response was filtered","param":"prompt","innererror":{"code":"ResponsibleAIPolicyViolation","content_filter_result":{"error":{"code":"content_filter_error","message":"The contents are not filtered"}}}}}`,
		},
		{
			name:   "unexpected classification shapes are ignored",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","message":"The response was filtered","param":"prompt","innererror":{"content_filter_result":"blocked"},"content_filters":{"blocked":true}}}`,
		},
		{
			name:   "other error codes are left untouched",
			status: fasthttp.StatusTooManyRequests,
			body:   `{"error":{"code":"rate_limit_exceeded","message":"Rate limit reached","innererror":{"content_filter_result":{"violence":{"filtered":true,"severity":"high"}}}}}`,
		},
		{
			name:   "truncated body",
			status: fasthttp.StatusBadRequest,
			body:   `{"error":{"code":"content_filter","innererror":{"content_filter_result":{"violence":`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAzureError(newAzureErrorResponse(tc.status, tc.body))

			// Everything but the classification is the OpenAI parser's own result.
			want := openai.ParseOpenAIError(newAzureErrorResponse(tc.status, tc.body))
			want.Error.ContentFilterDetails = tc.want
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("parseAzureError =\n%s\nwant\n%s", got, want)
			}

			// The OpenAI parser echoes a body it cannot decode into the message, unchanged
			// here, so only a decoded body can be checked for leaks.
			if !json.Valid([]byte(tc.body)) {
				return
			}
			encoded, err := json.Marshal(got.Error)
			if err != nil {
				t.Fatalf("encode error field: %v", err)
			}
			for _, leaked := range []string{"innererror", "ResponsibleAIPolicyViolation", "ContentFiltered", "content_filter_result", "content_filters", "internal-codenames", "allowed-terms", "offset", "source_type"} {
				if strings.Contains(string(encoded), leaked) {
					t.Errorf("error field leaks %q from Azure's body: %s", leaked, encoded)
				}
			}
		})
	}
}

// BenchmarkParseAzureError records what the content-filter extraction adds over the
// OpenAI parser Azure used before, on an ordinary error and on a blocked prompt.
func BenchmarkParseAzureError(b *testing.B) {
	bodies := []struct {
		name   string
		status int
		body   string
	}{
		{"rate_limit", fasthttp.StatusTooManyRequests, `{"error":{"code":"429","message":"Requests to the ChatCompletions_Create Operation under Azure OpenAI API version 2024-10-21 have exceeded token rate limit of your current OpenAI S0 pricing tier."}}`},
		{"content_filter", fasthttp.StatusBadRequest, azureChatPromptBlockedBody},
	}
	for _, bc := range bodies {
		resp := newAzureErrorResponse(bc.status, bc.body)
		b.Run("ParseOpenAIError/"+bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				openai.ParseOpenAIError(resp)
			}
		})
		b.Run("parseAzureError/"+bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				parseAzureError(resp)
			}
		})
	}
}
