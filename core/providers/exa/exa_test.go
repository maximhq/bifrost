package exa_test

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/providers/exa"
	"github.com/maximhq/bifrost/core/schemas"
)

type testLogger struct{}

func (l testLogger) Debug(string, ...any)                   {}
func (l testLogger) Info(string, ...any)                    {}
func (l testLogger) Warn(string, ...any)                    {}
func (l testLogger) Error(string, ...any)                   {}
func (l testLogger) Fatal(string, ...any)                   {}
func (l testLogger) SetLevel(schemas.LogLevel)              {}
func (l testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (l testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestExaProvider(t *testing.T, baseURL string) *exa.ExaProvider {
	t.Helper()
	provider, err := exa.NewExaProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 5,
			MaxConnsPerHost:                1,
			AllowPrivateNetwork:            true,
		},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewExaProvider: %v", err)
	}
	return provider
}

// capture is what the test server saw on one call.
type capture struct {
	path   string
	apiKey string
	auth   string
	body   map[string]any
}

// newExaServer answers every path with status and body, gzipped when gzipped is set, and records
// the request.
func newExaServer(t *testing.T, status int, body string, gzipped bool, captured *capture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if captured != nil {
			captured.path = r.URL.Path
			captured.apiKey = r.Header.Get("x-api-key")
			captured.auth = r.Header.Get("Authorization")
			_ = json.Unmarshal(raw, &captured.body)
		}
		w.Header().Set("Content-Type", "application/json")
		if gzipped {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(status)
			zw := gzip.NewWriter(w)
			_, _ = io.WriteString(zw, body)
			_ = zw.Close()
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func newCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

var testKey = schemas.Key{Value: schemas.SecretVar{Val: "exa-test-key"}}

func TestExaImplementsToolsProvider(t *testing.T) {
	var _ schemas.ToolsProvider = newTestExaProvider(t, "http://127.0.0.1:1")
}

const searchResponse = `{"requestId":"r1","results":[` +
	`{"id":"https://go.dev/doc","title":"Go docs","url":"https://go.dev/doc","publishedDate":"2026-01-02T00:00:00.000Z","author":"Go team","text":"page text","highlights":["first highlight","second"],"favicon":"https://go.dev/favicon.ico"}` +
	`],"costDollars":{"total":0.012,"search":{"neural":0.007},"contents":{"text":0.005}}}`

// Every neutral field has an Exa equivalent, and a call is priced at what Exa reports for it.
func TestSearch_SendsExaContractAndReportsExaCost(t *testing.T) {
	captured := &capture{}
	provider := newTestExaProvider(t, newExaServer(t, http.StatusOK, searchResponse, false, captured).URL)

	maxResults, includeContent := 3, true
	after, before := "2026-01-01", "2026-10-01T00:00:00Z"
	resp, bifrostErr := provider.Search(newCtx(), testKey, &schemas.BifrostSearchRequest{
		Provider:        schemas.Exa,
		Query:           "go generics",
		MaxResults:      &maxResults,
		IncludeContent:  &includeContent,
		IncludeDomains:  []string{"go.dev"},
		ExcludeDomains:  []string{"example.com"},
		PublishedAfter:  &after,
		PublishedBefore: &before,
	})
	if bifrostErr != nil {
		t.Fatalf("Search: %v", bifrostErr.Error.Message)
	}

	if captured.path != "/search" {
		t.Fatalf("path = %q, want /search", captured.path)
	}
	if captured.apiKey != "exa-test-key" || captured.auth != "" {
		t.Fatalf("x-api-key = %q, Authorization = %q: want the key in x-api-key only", captured.apiKey, captured.auth)
	}
	want := map[string]any{
		"query":              "go generics",
		"numResults":         float64(3),
		"includeDomains":     []any{"go.dev"},
		"excludeDomains":     []any{"example.com"},
		"startPublishedDate": "2026-01-01",
		"endPublishedDate":   "2026-10-01T00:00:00Z",
		"contents":           map[string]any{"text": true},
	}
	for field, value := range want {
		got, _ := json.Marshal(captured.body[field])
		expected, _ := json.Marshal(value)
		if string(got) != string(expected) {
			t.Fatalf("%s = %s, want %s", field, got, expected)
		}
	}
	if _, present := captured.body["model"]; present {
		t.Fatal("a routing model must never reach Exa")
	}

	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
	result := resp.Results[0]
	if result.URL != "https://go.dev/doc" || result.Title != "Go docs" || result.Content != "page text" {
		t.Fatalf("result = %+v, want Exa's url, title and text", result)
	}
	if result.Snippet != "first highlight" {
		t.Fatalf("snippet = %q, want the first highlight", result.Snippet)
	}
	if result.PublishedAt == nil || *result.PublishedAt != "2026-01-02T00:00:00.000Z" {
		t.Fatalf("published_at = %v, want Exa's publishedDate", result.PublishedAt)
	}
	if result.ProviderFields["author"] != "Go team" || result.ProviderFields["favicon"] != "https://go.dev/favicon.ico" {
		t.Fatalf("provider_fields = %v, want author and favicon under Exa's names", result.ProviderFields)
	}
	if resp.Usage == nil || resp.Usage.Cost == nil || resp.Usage.Cost.TotalCost != 0.012 {
		t.Fatalf("usage = %+v, want costDollars.total as the call's cost", resp.Usage)
	}
	if resp.ExtraFields.RequestType != schemas.SearchRequest {
		t.Fatalf("RequestType = %q, want search", resp.ExtraFields.RequestType)
	}
}

// Without include_content no contents are requested: Exa prices page text on top of the search.
func TestSearch_RequestsNoContentsUnlessAsked(t *testing.T) {
	captured := &capture{}
	provider := newTestExaProvider(t, newExaServer(t, http.StatusOK, `{"results":[],"costDollars":{"total":0.007}}`, false, captured).URL)

	resp, bifrostErr := provider.Search(newCtx(), testKey, &schemas.BifrostSearchRequest{Provider: schemas.Exa, Query: "q"})
	if bifrostErr != nil {
		t.Fatalf("Search: %v", bifrostErr.Error.Message)
	}
	if _, present := captured.body["contents"]; present {
		t.Fatalf("contents = %v, want none", captured.body["contents"])
	}
	// Exa bills a search that found nothing, and reports it; the gateway charges what it reports.
	if len(resp.Results) != 0 || resp.Usage == nil || resp.Usage.Cost.TotalCost != 0.007 {
		t.Fatalf("results = %v, usage = %+v: want no results, priced as reported", resp.Results, resp.Usage)
	}
}

// fasthttp returns its response buffers to a pool, so a body used after release can be
// overwritten by the next request; a gzipped body read raw would not decode at all. Both are
// covered by decoding into a body the call owns.
func TestSearch_DecodesACompressedResponse(t *testing.T) {
	provider := newTestExaProvider(t, newExaServer(t, http.StatusOK, searchResponse, true, nil).URL)

	resp, bifrostErr := provider.Search(newCtx(), testKey, &schemas.BifrostSearchRequest{Provider: schemas.Exa, Query: "q"})
	if bifrostErr != nil {
		t.Fatalf("Search: %v", bifrostErr.Error.Message)
	}
	if len(resp.Results) != 1 || resp.Results[0].URL != "https://go.dev/doc" {
		t.Fatalf("results = %+v, want the decoded page", resp.Results)
	}
}

func TestSearch_RefusesMoreResultsThanExaServes(t *testing.T) {
	provider := newTestExaProvider(t, "http://127.0.0.1:1")
	tooMany := 101
	_, bifrostErr := provider.Search(newCtx(), testKey, &schemas.BifrostSearchRequest{Provider: schemas.Exa, Query: "q", MaxResults: &tooMany})
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %+v, want a 400", bifrostErr)
	}
}

// Exa's status codes say what to do: 401 a wrong key, 402 an account out of credit, 429 a rate
// limit. Flattening them would hide the fix.
func TestSearch_PreservesUpstreamStatusCodes(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			provider := newTestExaProvider(t, newExaServer(t, status, `{"error":"nope"}`, false, nil).URL)
			_, bifrostErr := provider.Search(newCtx(), testKey, &schemas.BifrostSearchRequest{Provider: schemas.Exa, Query: "q"})
			if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != status {
				t.Fatalf("error = %+v, want status %d", bifrostErr, status)
			}
		})
	}
}

func TestFetch_ReadsOnePageThroughContents(t *testing.T) {
	captured := &capture{}
	body := `{"results":[{"id":"https://go.dev/doc","url":"https://go.dev/doc","title":"Go docs","text":"# Go"}],` +
		`"statuses":[{"id":"https://go.dev/doc","status":"success","source":"cached"}],"costDollars":{"total":0.001}}`
	provider := newTestExaProvider(t, newExaServer(t, http.StatusOK, body, false, captured).URL)

	resp, bifrostErr := provider.Fetch(newCtx(), testKey, &schemas.BifrostFetchRequest{Provider: schemas.Exa, URL: "https://go.dev/doc"})
	if bifrostErr != nil {
		t.Fatalf("Fetch: %v", bifrostErr.Error.Message)
	}
	if captured.path != "/contents" {
		t.Fatalf("path = %q, want /contents", captured.path)
	}
	urls, _ := json.Marshal(captured.body["urls"])
	if string(urls) != `["https://go.dev/doc"]` || captured.body["text"] != true {
		t.Fatalf("body = %v, want the one URL with text", captured.body)
	}
	if resp.Title != "Go docs" || resp.Content != "# Go" {
		t.Fatalf("response = %+v, want the page title and text", resp)
	}
	if resp.Usage == nil || resp.Usage.Cost.TotalCost != 0.001 {
		t.Fatalf("usage = %+v, want costDollars.total", resp.Usage)
	}
}

// /contents answers 200 for a page it could not read and says so in statuses. That is a failed
// Fetch, carrying the status Exa gives for the page, never an empty success.
func TestFetch_TurnsAFailedPageIntoAnError(t *testing.T) {
	body := `{"results":[],"statuses":[{"id":"https://example.com/missing","status":"error","error":{"tag":"CRAWL_NOT_FOUND","httpStatusCode":404}}],"costDollars":{"total":0}}`
	provider := newTestExaProvider(t, newExaServer(t, http.StatusOK, body, false, nil).URL)

	_, bifrostErr := provider.Fetch(newCtx(), testKey, &schemas.BifrostFetchRequest{Provider: schemas.Exa, URL: "https://example.com/missing"})
	if bifrostErr == nil || bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusNotFound {
		t.Fatalf("error = %+v, want a 404", bifrostErr)
	}
	if bifrostErr.Type == nil || *bifrostErr.Type != "CRAWL_NOT_FOUND" {
		t.Fatalf("type = %v, want Exa's tag", bifrostErr.Type)
	}
}

// Every inference operation is refused as unsupported rather than sent to an API that has none.
func TestExa_RefusesInferenceAsUnsupported(t *testing.T) {
	provider := newTestExaProvider(t, "http://127.0.0.1:1")
	_, bifrostErr := provider.ChatCompletion(newCtx(), testKey, &schemas.BifrostChatRequest{Provider: schemas.Exa, Model: "m"})
	if bifrostErr == nil || bifrostErr.Error == nil {
		t.Fatal("expected an unsupported-operation error")
	}
}
