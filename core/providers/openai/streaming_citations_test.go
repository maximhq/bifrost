package openai_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"

	bifrost "github.com/maximhq/bifrost/core"
	openaiprovider "github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// noopPostHook is a minimal PostHookRunner that passes through responses unchanged.
func noopPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, err
}

// newTestOpenAIProvider creates an OpenAI provider pointing at the given base URL.
func newTestOpenAIProvider(t *testing.T, baseURL string) *openaiprovider.OpenAIProvider {
	t.Helper()
	return openaiprovider.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{
			BaseURL:                        baseURL,
			DefaultRequestTimeoutInSeconds: 10,
		},
	}, bifrost.NewNoOpLogger())
}

// drainStreamCitations collects all chat-response chunks from the stream channel
// and returns the Citations, SearchResults and Videos from the final synthetic
// (terminal) chunk — i.e. the last non-nil BifrostChatResponse.
func drainStreamCitations(t *testing.T, stream chan *schemas.BifrostStreamChunk) ([]string, []schemas.SearchResult, []schemas.VideoResult) {
	t.Helper()

	var lastChatResp *schemas.BifrostChatResponse
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			t.Fatalf("stream returned error: %v", chunk.BifrostError)
		}
		if chunk.BifrostChatResponse != nil {
			lastChatResp = chunk.BifrostChatResponse
		}
	}
	if lastChatResp == nil {
		t.Fatal("stream produced no BifrostChatResponse chunks")
	}
	return lastChatResp.Citations, lastChatResp.SearchResults, lastChatResp.Videos
}

// TestHandleOpenAIChatCompletionStreaming_CitationsCollectedFromEmptyChoicesChunk
// verifies that citations arriving in an empty-choices final chunk (the Perplexity
// pattern) are accumulated and attached to the terminal synthetic chunk, not dropped.
func TestHandleOpenAIChatCompletionStreaming_CitationsCollectedFromEmptyChoicesChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Chunk 1: content delta
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"The answer.\"},\"finish_reason\":null}]}\n\n")
		// Chunk 2: finish_reason chunk
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		// Chunk 3: empty choices + citations (Perplexity pattern)
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"citations\":[\"https://example.com/a\",\"https://example.com/b\"],\"search_results\":[{\"url\":\"https://example.com/a\",\"title\":\"Source A\"},{\"url\":\"https://example.com/b\",\"title\":\"Source B\"}]}\n\n")
		// Terminator
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "sonar",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	citations, searchResults, _ := drainStreamCitations(t, stream)

	if len(citations) != 2 {
		t.Errorf("expected 2 citations, got %d: %v", len(citations), citations)
	}
	if len(searchResults) != 2 {
		t.Errorf("expected 2 search_results, got %d: %v", len(searchResults), searchResults)
	}
	if len(citations) > 0 && citations[0] != "https://example.com/a" {
		t.Errorf("expected first citation %q, got %q", "https://example.com/a", citations[0])
	}
}

// TestHandleOpenAIChatCompletionStreaming_CitationsDeduplicated verifies that a
// citation URL appearing in multiple chunks is only kept once in the terminal chunk.
func TestHandleOpenAIChatCompletionStreaming_CitationsDeduplicated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// First chunk with citations
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"citations\":[\"https://example.com/a\",\"https://example.com/b\"]}\n\n")
		// Second chunk that repeats the same citations plus one new one
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"citations\":[\"https://example.com/b\",\"https://example.com/c\"]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "sonar",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	citations, _, _ := drainStreamCitations(t, stream)

	// Expect exactly 3 unique citations in first-seen order: a, b, c
	if len(citations) != 3 {
		t.Errorf("expected 3 deduplicated citations, got %d: %v", len(citations), citations)
	}
	expected := []string{"https://example.com/a", "https://example.com/b", "https://example.com/c"}
	for i, want := range expected {
		if i >= len(citations) {
			break
		}
		if citations[i] != want {
			t.Errorf("citations[%d]: expected %q, got %q", i, want, citations[i])
		}
	}
}

// TestHandleOpenAIChatCompletionStreaming_SearchResultsDeduplicated verifies that
// SearchResult entries sharing the same URL are deduplicated across chunks.
func TestHandleOpenAIChatCompletionStreaming_SearchResultsDeduplicated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"search_results\":[{\"url\":\"https://example.com/x\",\"title\":\"X\"}]}\n\n")
		// Same URL repeated
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"search_results\":[{\"url\":\"https://example.com/x\",\"title\":\"X-duplicate\"},{\"url\":\"https://example.com/y\",\"title\":\"Y\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "sonar",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	_, searchResults, _ := drainStreamCitations(t, stream)

	if len(searchResults) != 2 {
		t.Errorf("expected 2 deduplicated search_results, got %d: %v", len(searchResults), searchResults)
	}
	if len(searchResults) > 0 && searchResults[0].URL != "https://example.com/x" {
		t.Errorf("expected first search result URL %q, got %q", "https://example.com/x", searchResults[0].URL)
	}
}

// TestHandleOpenAIChatCompletionStreaming_CitationsInContentAndEmptyChunk validates the
// realistic mixed scenario: a provider emits partial citations alongside content
// and then repeats/extends them in a trailing empty-choices chunk. The final
// result must contain exactly the deduplicated union, in first-seen order.
func TestHandleOpenAIChatCompletionStreaming_CitationsInContentAndEmptyChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Content chunk that already carries one citation
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"The answer.\"},\"finish_reason\":null}],\"citations\":[\"https://example.com/a\"]}\n\n")
		// Finish-reason chunk (no citations)
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		// Empty-choices chunk with the same citation repeated plus a new one
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"citations\":[\"https://example.com/a\",\"https://example.com/b\"]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "sonar",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	citations, _, _ := drainStreamCitations(t, stream)

	// Expect exactly 2 citations: "a" (first-seen) and "b" (new), with "a" not duplicated.
	if len(citations) != 2 {
		t.Errorf("expected 2 deduplicated citations, got %d: %v", len(citations), citations)
	}
	expected := []string{"https://example.com/a", "https://example.com/b"}
	for i, want := range expected {
		if i >= len(citations) {
			break
		}
		if citations[i] != want {
			t.Errorf("citations[%d]: expected %q, got %q", i, want, citations[i])
		}
	}
}

// TestHandleOpenAIChatCompletionStreaming_SearchResultKeepLast verifies that when a
// provider sends two chunks carrying the same search-result URL, the later entry
// (with potentially richer metadata) overwrites the first in the final output,
// preserving insertion order of the URL slot.
func TestHandleOpenAIChatCompletionStreaming_SearchResultKeepLast(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// First chunk: partial metadata
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"search_results\":[{\"url\":\"https://example.com/x\",\"title\":\"Old title\"}]}\n\n")
		// Second chunk: same URL with a richer title (simulates provider updating metadata)
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"search_results\":[{\"url\":\"https://example.com/x\",\"title\":\"Better title\"},{\"url\":\"https://example.com/y\",\"title\":\"Y\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "sonar",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	_, searchResults, _ := drainStreamCitations(t, stream)

	// Expect 2 entries preserving insertion order (x first, y second).
	if len(searchResults) != 2 {
		t.Errorf("expected 2 search_results, got %d: %v", len(searchResults), searchResults)
	}
	// The entry for URL "x" must carry the latest title.
	if len(searchResults) > 0 {
		if searchResults[0].URL != "https://example.com/x" {
			t.Errorf("expected first URL %q, got %q", "https://example.com/x", searchResults[0].URL)
		}
		if searchResults[0].Title != "Better title" {
			t.Errorf("expected keep-last title %q, got %q", "Better title", searchResults[0].Title)
		}
	}
}

// TestHandleOpenAIChatCompletionStreaming_EmptyURLSearchResultsNotCollapsed verifies
// that search results with an empty URL are never collapsed into one slot. Each is
// appended as a distinct entry regardless of how many empty-URL results appear.
func TestHandleOpenAIChatCompletionStreaming_EmptyURLSearchResultsNotCollapsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Two search results with no URL — must not be deduplicated against each other.
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-abc\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"sonar\",\"choices\":[],\"search_results\":[{\"url\":\"\",\"title\":\"No-URL result A\"},{\"url\":\"\",\"title\":\"No-URL result B\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "sonar",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	_, searchResults, _ := drainStreamCitations(t, stream)

	// Both results must survive — empty URL must not collapse them into one.
	if len(searchResults) != 2 {
		t.Errorf("expected 2 search_results (empty-URL entries kept distinct), got %d: %v", len(searchResults), searchResults)
	}
	if len(searchResults) >= 1 && searchResults[0].Title != "No-URL result A" {
		t.Errorf("expected title %q, got %q", "No-URL result A", searchResults[0].Title)
	}
	if len(searchResults) >= 2 && searchResults[1].Title != "No-URL result B" {
		t.Errorf("expected title %q, got %q", "No-URL result B", searchResults[1].Title)
	}
}

// drainStreamAnnotations drains the stream and returns:
//   - chunksWithAnnotations: count of received chunks (intermediate + final synthetic)
//     whose delta.annotations slice is non-empty. Use this to verify annotation-only
//     chunks are not dropped by the forwarding gate.
//   - finalChunkAnnotations: annotations on the last BifrostChatResponse chunk (the
//     terminal synthetic chunk produced by Bifrost).
func drainStreamAnnotations(t *testing.T, stream chan *schemas.BifrostStreamChunk) (chunksWithAnnotations int, finalChunkAnnotations []schemas.ChatAssistantMessageAnnotation) {
	t.Helper()
	var lastChatResp *schemas.BifrostChatResponse
	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			t.Fatalf("stream returned error: %v", chunk.BifrostError)
		}
		if chunk.BifrostChatResponse != nil {
			if len(chunk.BifrostChatResponse.Choices) > 0 {
				choice := chunk.BifrostChatResponse.Choices[0]
				if choice.ChatStreamResponseChoice != nil &&
					choice.ChatStreamResponseChoice.Delta != nil &&
					len(choice.ChatStreamResponseChoice.Delta.Annotations) > 0 {
					chunksWithAnnotations++
				}
			}
			lastChatResp = chunk.BifrostChatResponse
		}
	}
	if lastChatResp == nil {
		t.Fatal("stream produced no BifrostChatResponse chunks")
	}
	if len(lastChatResp.Choices) > 0 {
		choice := lastChatResp.Choices[0]
		if choice.ChatStreamResponseChoice != nil && choice.ChatStreamResponseChoice.Delta != nil {
			finalChunkAnnotations = choice.ChatStreamResponseChoice.Delta.Annotations
		}
	}
	return
}

// TestHandleOpenAIChatCompletionStreaming_AnnotationOnlyChunkForwarded verifies that
// a chunk carrying only delta.annotations (no content, reasoning, or tool calls) is
// not dropped by the forwarding gate and reaches the stream consumer.
func TestHandleOpenAIChatCompletionStreaming_AnnotationOnlyChunkForwarded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Chunk 1: role header
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-ann\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
		// Chunk 2: annotation-only — no content, purely delta.annotations
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-ann\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/src\",\"title\":\"Source\",\"start_index\":0,\"end_index\":3}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 3: finish
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-ann\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	chunksWithAnnotations, finalAnnotations := drainStreamAnnotations(t, stream)

	// At least 2 annotation-bearing chunks: the intermediate annotation-only chunk AND
	// the final synthetic chunk (both should carry the annotation).
	if chunksWithAnnotations < 2 {
		t.Errorf("expected >= 2 chunks with annotations (intermediate + synthetic), got %d — annotation-only chunk may have been dropped by forwarding gate", chunksWithAnnotations)
	}
	if len(finalAnnotations) != 1 {
		t.Errorf("expected 1 annotation on final synthetic chunk, got %d", len(finalAnnotations))
	}
	if len(finalAnnotations) > 0 && finalAnnotations[0].URLCitation.Title != "Source" {
		t.Errorf("expected annotation title %q, got %q", "Source", finalAnnotations[0].URLCitation.Title)
	}
}

// TestHandleOpenAIChatCompletionStreaming_AnnotationsAggregatedOnFinalChunk verifies
// that annotations from multiple streaming chunks are collected and attached to the
// terminal synthetic chunk's delta.Annotations so non-streaming consumers get the
// full set in one place.
func TestHandleOpenAIChatCompletionStreaming_AnnotationsAggregatedOnFinalChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Chunk 1: content with first annotation
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-agg\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"See \",\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/a\",\"title\":\"A\",\"start_index\":0,\"end_index\":3}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 2: second annotation (different URL and position)
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-agg\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/b\",\"title\":\"B\",\"start_index\":4,\"end_index\":7}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 3: finish
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-agg\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	_, finalAnnotations := drainStreamAnnotations(t, stream)

	// Both annotations must be present on the synthetic final chunk.
	if len(finalAnnotations) != 2 {
		t.Errorf("expected 2 aggregated annotations on final chunk, got %d: %v", len(finalAnnotations), finalAnnotations)
	}
	if len(finalAnnotations) >= 1 && finalAnnotations[0].URLCitation.Title != "A" {
		t.Errorf("expected first annotation title %q, got %q", "A", finalAnnotations[0].URLCitation.Title)
	}
	if len(finalAnnotations) >= 2 && finalAnnotations[1].URLCitation.Title != "B" {
		t.Errorf("expected second annotation title %q, got %q", "B", finalAnnotations[1].URLCitation.Title)
	}
}

// TestHandleOpenAIChatCompletionStreaming_AnnotationsDeduplicatedByCompositeKey verifies
// composite-key deduplication semantics:
//   - Same URL + same position across chunks → deduplicated to one entry (keep-last).
//   - Same URL + different positions → kept as two distinct entries.
func TestHandleOpenAIChatCompletionStreaming_AnnotationsDeduplicatedByCompositeKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Chunk 1: annotation at position 0-5 with old title
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-dedup\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/src\",\"title\":\"Old title\",\"start_index\":0,\"end_index\":5}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 2: same URL + same position → should replace chunk 1 (keep-last)
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-dedup\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/src\",\"title\":\"New title\",\"start_index\":0,\"end_index\":5}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 3: same URL but different position → must NOT be deduplicated
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-dedup\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/src\",\"title\":\"Different position\",\"start_index\":10,\"end_index\":15}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 4: finish
		fmt.Fprint(w, "data: {\"id\":\"chatcmpl-dedup\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	_, finalAnnotations := drainStreamAnnotations(t, stream)

	// Expect 2 entries: position 0-5 (keep-last title) and position 10-15.
	if len(finalAnnotations) != 2 {
		t.Errorf("expected 2 annotations (same-position dedup, different-position kept), got %d: %v", len(finalAnnotations), finalAnnotations)
	}
	// Position 0-5 should carry the keep-last title, not the old one.
	if len(finalAnnotations) >= 1 {
		if finalAnnotations[0].URLCitation.StartIndex != 0 || finalAnnotations[0].URLCitation.EndIndex != 5 {
			t.Errorf("expected first annotation at 0-5, got %d-%d", finalAnnotations[0].URLCitation.StartIndex, finalAnnotations[0].URLCitation.EndIndex)
		}
		if finalAnnotations[0].URLCitation.Title != "New title" {
			t.Errorf("expected keep-last title %q, got %q", "New title", finalAnnotations[0].URLCitation.Title)
		}
	}
	// Position 10-15 must be a distinct entry.
	if len(finalAnnotations) >= 2 {
		if finalAnnotations[1].URLCitation.StartIndex != 10 || finalAnnotations[1].URLCitation.EndIndex != 15 {
			t.Errorf("expected second annotation at 10-15, got %d-%d", finalAnnotations[1].URLCitation.StartIndex, finalAnnotations[1].URLCitation.EndIndex)
		}
	}
}

// TestHandleOpenAIChatCompletionStreaming_AnnotationsInSerializedJSON verifies that
// annotations appear in the actual serialized JSON output of each BifrostStreamChunk,
// not just in the Go struct fields. This catches serialization issues (e.g., sonic
// JIT mishandling embedded struct pointers, build caching, or field tag problems)
// that struct-level assertions would miss.
//
// The test simulates the exact pattern seen with OpenRouter + Perplexity Sonar:
// an initial chunk carries delta.annotations alongside content:"" and role:"assistant".
func TestHandleOpenAIChatCompletionStreaming_AnnotationsInSerializedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Chunk 1: content + role + annotations (OpenRouter + Perplexity pattern)
		fmt.Fprint(w, "data: {\"id\":\"gen-test-json\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"perplexity/sonar\",\"provider\":\"Perplexity\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\",\"role\":\"assistant\",\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/a\",\"title\":\"Source A\",\"start_index\":0,\"end_index\":5}},{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/b\",\"title\":\"Source B\",\"start_index\":10,\"end_index\":15}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 2: regular content
		fmt.Fprint(w, "data: {\"id\":\"gen-test-json\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"perplexity/sonar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Saturn has rings\"},\"finish_reason\":null}]}\n\n")
		// Chunk 3: annotation-only (no content)
		fmt.Fprint(w, "data: {\"id\":\"gen-test-json\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"perplexity/sonar\",\"choices\":[{\"index\":0,\"delta\":{\"annotations\":[{\"type\":\"url_citation\",\"url_citation\":{\"url\":\"https://example.com/c\",\"title\":\"Source C\",\"start_index\":20,\"end_index\":25}}]},\"finish_reason\":null}]}\n\n")
		// Chunk 4: finish
		fmt.Fprint(w, "data: {\"id\":\"gen-test-json\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"perplexity/sonar\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := newTestOpenAIProvider(t, server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: *schemas.NewEnvVar("test-key")}

	stream, bifrostErr := provider.ChatCompletionStream(ctx, noopPostHook, nil, key, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "perplexity/sonar",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Hello")}},
		},
	})
	if bifrostErr != nil {
		t.Fatalf("ChatCompletionStream returned error: %v", bifrostErr)
	}

	// Track chunks where the serialized JSON contains "annotations" in delta
	var jsonAnnotationChunks int
	var allChunks int
	var finalChunkJSON string

	for chunk := range stream {
		if chunk == nil {
			continue
		}
		if chunk.BifrostError != nil {
			t.Fatalf("stream returned error: %v", chunk.BifrostError)
		}
		allChunks++

		// Serialize exactly as the HTTP handler does: sonic.Marshal → BifrostStreamChunk.MarshalJSON
		chunkJSON, err := sonic.Marshal(chunk)
		if err != nil {
			t.Fatalf("failed to marshal chunk %d: %v", allChunks, err)
		}
		chunkStr := string(chunkJSON)
		finalChunkJSON = chunkStr

		// Verify annotations in serialized JSON (not just struct fields)
		if strings.Contains(chunkStr, `"annotations"`) {
			jsonAnnotationChunks++

			// Parse the serialized JSON back and verify structure
			var parsed map[string]interface{}
			if err := sonic.UnmarshalString(chunkStr, &parsed); err != nil {
				t.Fatalf("failed to re-parse chunk JSON: %v", err)
			}
			choices, ok := parsed["choices"].([]interface{})
			if !ok || len(choices) == 0 {
				t.Fatal("serialized JSON has no choices array")
			}
			choice, ok := choices[0].(map[string]interface{})
			if !ok {
				t.Fatal("choices[0] is not an object")
			}
			delta, ok := choice["delta"].(map[string]interface{})
			if !ok {
				t.Fatal("choices[0].delta is not an object")
			}
			annotations, ok := delta["annotations"].([]interface{})
			if !ok || len(annotations) == 0 {
				t.Fatalf("choices[0].delta.annotations missing or empty in serialized JSON: delta=%v", delta)
			}
			// Verify annotation structure
			ann, ok := annotations[0].(map[string]interface{})
			if !ok {
				t.Fatal("annotation[0] is not an object")
			}
			if ann["type"] != "url_citation" {
				t.Errorf("expected annotation type %q, got %v", "url_citation", ann["type"])
			}
			urlCitation, ok := ann["url_citation"].(map[string]interface{})
			if !ok {
				t.Fatal("annotation[0].url_citation is not an object")
			}
			if urlCitation["url"] == nil || urlCitation["url"] == "" {
				t.Error("annotation[0].url_citation.url is missing or empty in serialized JSON")
			}
		}
	}

	// At least 3 chunks should have annotations:
	// chunk 1 (content + annotations), chunk 3 (annotation-only), final synthetic chunk
	if jsonAnnotationChunks < 3 {
		t.Errorf("expected >= 3 chunks with annotations in serialized JSON, got %d out of %d total", jsonAnnotationChunks, allChunks)
	}

	// Verify the final (synthetic) chunk has all 3 aggregated annotations
	if !strings.Contains(finalChunkJSON, `"annotations"`) {
		t.Fatal("final synthetic chunk JSON does not contain annotations")
	}
	var finalParsed map[string]interface{}
	if err := sonic.UnmarshalString(finalChunkJSON, &finalParsed); err != nil {
		t.Fatalf("failed to parse final chunk JSON: %v", err)
	}
	finalChoices := finalParsed["choices"].([]interface{})
	finalDelta := finalChoices[0].(map[string]interface{})["delta"].(map[string]interface{})
	finalAnnotations := finalDelta["annotations"].([]interface{})
	if len(finalAnnotations) != 3 {
		t.Errorf("expected 3 aggregated annotations on final chunk, got %d", len(finalAnnotations))
	}
}
