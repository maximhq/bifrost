package handlers

import (
	"bytes"
	"mime/multipart"
	"testing"

	"github.com/valyala/fasthttp"
)

func newChatRequestCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/chat/completions")
	ctx.Request.SetBody([]byte(body))
	return ctx
}

func TestPrepareChatCompletionRequest_AcceptsObjectFallbacksWithParams(t *testing.T) {
	ctx := newChatRequestCtx(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": "hello"}],
		"fallbacks": [
			{
				"provider": "bedrock",
				"model": "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
				"params": {
					"reasoning_effort": "high",
					"thinking_budget": 2048
				}
			}
		]
	}`)

	_, bifrostReq, err := prepareChatCompletionRequest(ctx)
	if err != nil {
		t.Fatalf("expected object-form fallbacks to parse successfully, got error: %v", err)
	}
	if bifrostReq == nil {
		t.Fatal("expected bifrost request, got nil")
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
	if bifrostReq.Fallbacks[0].Model != "us.anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Fatalf("unexpected fallback model: %s", bifrostReq.Fallbacks[0].Model)
	}
	if bifrostReq.Fallbacks[0].Params["reasoning_effort"] != "high" {
		t.Fatalf("expected fallback reasoning_effort=high, got %#v", bifrostReq.Fallbacks[0].Params["reasoning_effort"])
	}
	if bifrostReq.Fallbacks[0].Params["thinking_budget"] != float64(2048) {
		t.Fatalf("expected fallback thinking_budget=2048, got %#v", bifrostReq.Fallbacks[0].Params["thinking_budget"])
	}
}

func TestPrepareChatCompletionRequest_StringFallbacksRemainSupported(t *testing.T) {
	ctx := newChatRequestCtx(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": "hello"}],
		"fallbacks": ["bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0"]
	}`)

	_, bifrostReq, err := prepareChatCompletionRequest(ctx)
	if err != nil {
		t.Fatalf("expected string-form fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
}

func TestPrepareChatCompletionRequest_AcceptsMultipleFallbacks(t *testing.T) {
	ctx := newChatRequestCtx(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": "hello"}],
		"fallbacks": [
			{"provider": "bedrock", "model": "us.anthropic.claude-3-5-sonnet-20241022-v2:0", "params": {"reasoning_effort": "high"}},
			{"provider": "openai", "model": "gpt-4.1-mini"}
		]
	}`)

	_, bifrostReq, err := prepareChatCompletionRequest(ctx)
	if err != nil {
		t.Fatalf("expected multiple fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 2 {
		t.Fatalf("expected 2 fallbacks, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Params["reasoning_effort"] != "high" {
		t.Fatalf("expected first fallback params to be preserved, got %#v", bifrostReq.Fallbacks[0].Params)
	}
	if bifrostReq.Fallbacks[1].Params != nil {
		t.Fatalf("expected second fallback without params, got %#v", bifrostReq.Fallbacks[1].Params)
	}
}

func TestPrepareChatCompletionRequest_LenientlyIgnoresInvalidFallbackObject(t *testing.T) {
	// Backward-compatibility contract: prior to per-fallback params support,
	// malformed fallback entries were silently dropped. Lenient validation
	// keeps that behaviour so existing clients aren't suddenly broken.
	ctx := newChatRequestCtx(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": "hello"}],
		"fallbacks": [
			{"provider": "bedrock"}
		]
	}`)

	_, bifrostReq, err := prepareChatCompletionRequest(ctx)
	if err != nil {
		t.Fatalf("expected lenient mode to accept request and drop the invalid fallback, got error: %v", err)
	}
	if bifrostReq == nil {
		t.Fatal("expected bifrost request, got nil")
	}
	if len(bifrostReq.Fallbacks) != 0 {
		t.Fatalf("expected invalid fallback to be dropped, got %d fallbacks", len(bifrostReq.Fallbacks))
	}
}

func TestPrepareChatCompletionRequest_DropsOnlyInvalidEntriesInMixedBatch(t *testing.T) {
	// Mixed batches must keep valid entries while silently dropping malformed
	// ones — anything else would be a hidden DoS vector for clients that
	// occasionally typo a fallback model.
	ctx := newChatRequestCtx(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": "hello"}],
		"fallbacks": [
			{"provider": "bedrock", "model": "us.anthropic.claude-3-5-sonnet-20241022-v2:0"},
			{"provider": "bedrock"},
			{"provider": "openai", "model": "gpt-4.1-mini", "params": {"reasoning_effort": "low"}}
		]
	}`)

	_, bifrostReq, err := prepareChatCompletionRequest(ctx)
	if err != nil {
		t.Fatalf("expected lenient mode to accept mixed batch, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 2 {
		t.Fatalf("expected 2 valid fallbacks (invalid dropped), got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Model != "us.anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Fatalf("unexpected first fallback: %+v", bifrostReq.Fallbacks[0])
	}
	if bifrostReq.Fallbacks[1].Provider != "openai" {
		t.Fatalf("unexpected second fallback provider: %s", bifrostReq.Fallbacks[1].Provider)
	}
	if bifrostReq.Fallbacks[1].Params["reasoning_effort"] != "low" {
		t.Fatalf("expected params on second fallback to be preserved, got %#v", bifrostReq.Fallbacks[1].Params)
	}
}

func TestPrepareChatCompletionRequest_EmptyFallbackArrayIsAccepted(t *testing.T) {
	ctx := newChatRequestCtx(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": "hello"}],
		"fallbacks": []
	}`)

	_, bifrostReq, err := prepareChatCompletionRequest(ctx)
	if err != nil {
		t.Fatalf("expected empty fallback array to be accepted, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 0 {
		t.Fatalf("expected zero fallbacks, got %d", len(bifrostReq.Fallbacks))
	}
}

func TestPrepareChatCompletionRequest_DuplicateFallbacksArePreserved(t *testing.T) {
	// Duplicate entries are a legitimate use case (different params per attempt)
	// and must not be deduplicated by the validation layer — fallback ordering
	// and multiplicity are the caller's contract.
	ctx := newChatRequestCtx(`{
		"model": "openai/gpt-4o-mini",
		"messages": [{"role": "user", "content": "hello"}],
		"fallbacks": [
			{"provider": "openai", "model": "gpt-4.1-mini", "params": {"temperature": 0.1}},
			{"provider": "openai", "model": "gpt-4.1-mini", "params": {"temperature": 0.9}}
		]
	}`)

	_, bifrostReq, err := prepareChatCompletionRequest(ctx)
	if err != nil {
		t.Fatalf("expected duplicates to be accepted, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 2 {
		t.Fatalf("expected duplicates preserved, got %d fallbacks", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Params["temperature"] == bifrostReq.Fallbacks[1].Params["temperature"] {
		t.Fatalf("duplicates were merged: %#v", bifrostReq.Fallbacks)
	}
}

// invalidFallbackEntryError is intentionally still referenced here to keep the
// constant usage expectation in the test surface — even though we no longer
// expect validation to surface it on the happy path. If a future refactor
// removes the constant, this reference will catch it at compile time.
var _ = invalidFallbackEntryError

func buildMultipartImageRequestCtx(t *testing.T, uri string, fields map[string]string) *fasthttp.RequestCtx {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("failed to write field %s: %v", key, err)
		}
	}

	fileWriter, err := writer.CreateFormFile("image", "image.png")
	if err != nil {
		t.Fatalf("failed to create image form file: %v", err)
	}
	if _, err := fileWriter.Write([]byte("fake-image")); err != nil {
		t.Fatalf("failed to write image form file: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI(uri)
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

func TestPrepareImageEditRequest_AcceptsObjectFallbacks(t *testing.T) {
	ctx := buildMultipartImageRequestCtx(t, "/v1/images/edits", map[string]string{
		"model":     "openai/gpt-image-1",
		"prompt":    "edit this",
		"fallbacks": `[{"provider":"bedrock","model":"us.anthropic.claude-3-5-sonnet-20241022-v2:0","params":{"reasoning_effort":"high","thinking_budget":2048}}]`,
	})

	_, bifrostReq, err := prepareImageEditRequest(ctx)
	if err != nil {
		t.Fatalf("expected multipart object-form fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
	if bifrostReq.Fallbacks[0].Params["reasoning_effort"] != "high" {
		t.Fatalf("expected fallback reasoning_effort=high, got %#v", bifrostReq.Fallbacks[0].Params["reasoning_effort"])
	}
	if bifrostReq.Fallbacks[0].Params["thinking_budget"] != float64(2048) {
		t.Fatalf("expected fallback thinking_budget=2048, got %#v", bifrostReq.Fallbacks[0].Params["thinking_budget"])
	}
}

func TestPrepareImageEditRequest_StringFallbacksRemainSupported(t *testing.T) {
	ctx := buildMultipartImageRequestCtx(t, "/v1/images/edits", map[string]string{
		"model":     "openai/gpt-image-1",
		"prompt":    "edit this",
		"fallbacks": "bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0",
	})

	_, bifrostReq, err := prepareImageEditRequest(ctx)
	if err != nil {
		t.Fatalf("expected multipart string-form fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
}

func TestPrepareImageVariationRequest_AcceptsObjectFallbacks(t *testing.T) {
	ctx := buildMultipartImageRequestCtx(t, "/v1/images/variations", map[string]string{
		"model":     "openai/gpt-image-1",
		"fallbacks": `[{"provider":"bedrock","model":"us.anthropic.claude-3-5-sonnet-20241022-v2:0","params":{"reasoning_effort":"high","thinking_budget":2048}}]`,
	})

	bifrostReq, err := prepareImageVariationRequest(ctx)
	if err != nil {
		t.Fatalf("expected multipart object-form fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
	if bifrostReq.Fallbacks[0].Params["reasoning_effort"] != "high" {
		t.Fatalf("expected fallback reasoning_effort=high, got %#v", bifrostReq.Fallbacks[0].Params["reasoning_effort"])
	}
	if bifrostReq.Fallbacks[0].Params["thinking_budget"] != float64(2048) {
		t.Fatalf("expected fallback thinking_budget=2048, got %#v", bifrostReq.Fallbacks[0].Params["thinking_budget"])
	}
}

func TestPrepareImageVariationRequest_StringFallbacksRemainSupported(t *testing.T) {
	ctx := buildMultipartImageRequestCtx(t, "/v1/images/variations", map[string]string{
		"model":     "openai/gpt-image-1",
		"fallbacks": "bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0",
	})

	bifrostReq, err := prepareImageVariationRequest(ctx)
	if err != nil {
		t.Fatalf("expected multipart string-form fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
}

func TestPrepareImageEditRequest_LenientlyIgnoresInvalidJSONFallbackObject(t *testing.T) {
	ctx := buildMultipartImageRequestCtx(t, "/v1/images/edits", map[string]string{
		"model":     "openai/gpt-image-1",
		"prompt":    "edit this",
		"fallbacks": `[{"model":"bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0"}]`,
	})

	_, bifrostReq, err := prepareImageEditRequest(ctx)
	if err != nil {
		t.Fatalf("expected lenient mode to accept multipart with invalid fallback, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 0 {
		t.Fatalf("expected invalid multipart fallback to be dropped, got %d", len(bifrostReq.Fallbacks))
	}
}

func buildMultipartTranscriptionRequestCtx(t *testing.T, fields map[string]string) *fasthttp.RequestCtx {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("failed to write field %s: %v", key, err)
		}
	}

	fileWriter, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		t.Fatalf("failed to create file form part: %v", err)
	}
	if _, err := fileWriter.Write([]byte("fake-audio")); err != nil {
		t.Fatalf("failed to write file form part: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetRequestURI("/v1/audio/transcriptions")
	ctx.Request.Header.SetContentType(writer.FormDataContentType())
	ctx.Request.SetBody(body.Bytes())
	return ctx
}

func TestPrepareTranscriptionRequest_AcceptsObjectFallbacks(t *testing.T) {
	ctx := buildMultipartTranscriptionRequestCtx(t, map[string]string{
		"model":     "openai/gpt-4o-transcribe",
		"fallbacks": `[{"provider":"bedrock","model":"us.anthropic.claude-3-5-sonnet-20241022-v2:0","params":{"reasoning_effort":"high","thinking_budget":2048}}]`,
	})

	bifrostReq, _, err := prepareTranscriptionRequest(ctx)
	if err != nil {
		t.Fatalf("expected multipart object-form transcription fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
	if bifrostReq.Fallbacks[0].Model != "us.anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Fatalf("unexpected fallback model: %s", bifrostReq.Fallbacks[0].Model)
	}
	if bifrostReq.Fallbacks[0].Params["reasoning_effort"] != "high" {
		t.Fatalf("expected fallback reasoning_effort=high, got %#v", bifrostReq.Fallbacks[0].Params["reasoning_effort"])
	}
	if bifrostReq.Fallbacks[0].Params["thinking_budget"] != float64(2048) {
		t.Fatalf("expected fallback thinking_budget=2048, got %#v", bifrostReq.Fallbacks[0].Params["thinking_budget"])
	}
}

func TestPrepareTranscriptionRequest_StringFallbacksRemainSupported(t *testing.T) {
	ctx := buildMultipartTranscriptionRequestCtx(t, map[string]string{
		"model":     "openai/gpt-4o-transcribe",
		"fallbacks": "bedrock/us.anthropic.claude-3-5-sonnet-20241022-v2:0",
	})

	bifrostReq, _, err := prepareTranscriptionRequest(ctx)
	if err != nil {
		t.Fatalf("expected multipart string-form transcription fallbacks to parse successfully, got error: %v", err)
	}
	if len(bifrostReq.Fallbacks) != 1 {
		t.Fatalf("expected 1 fallback, got %d", len(bifrostReq.Fallbacks))
	}
	if bifrostReq.Fallbacks[0].Provider != "bedrock" {
		t.Fatalf("expected fallback provider bedrock, got %s", bifrostReq.Fallbacks[0].Provider)
	}
}
