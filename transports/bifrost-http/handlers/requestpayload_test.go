package handlers

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/valyala/fasthttp"
)

type loginDecodeConfigStore struct {
	configstore.ConfigStore
}

func TestSessionLoginInvalidPayloadDoesNotExposeDecoderDetails(t *testing.T) {
	h := &SessionHandler{configStore: &loginDecodeConfigStore{}}
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"username":1234,"password":"Suresh"}`)

	h.login(ctx)

	body := string(ctx.Response.Body())
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", ctx.Response.StatusCode(), fasthttp.StatusBadRequest, body)
	}
	if !strings.Contains(body, "Invalid request payload") {
		t.Fatalf("body = %s, want generic invalid payload message", body)
	}
	if strings.Contains(body, "cannot unmarshal") || strings.Contains(body, "username") || strings.Contains(body, "Go struct field") {
		t.Fatalf("body exposes decoder internals: %s", body)
	}
}

func TestPrepareRequestInvalidPayloadDoesNotExposeDecoderDetails(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"model":1234,"prompt":"hello"}`)

	_, _, err := prepareRequest[TextRequest](ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid payload")
	}
	msg := err.Error()
	if msg != "Invalid request payload" {
		t.Fatalf("error = %q, want generic invalid payload message", msg)
	}
	if strings.Contains(msg, "cannot unmarshal") || strings.Contains(msg, "model") || strings.Contains(msg, "Go struct field") {
		t.Fatalf("error exposes decoder internals: %s", msg)
	}
}

// TestExtractExtraParamsPreservesNumbers pins that extra parameters keep the
// caller's numeric literal: integers above 2^53 (seeds, ids) must not round
// through float64, and decimals must not be re-rendered.
func TestExtractExtraParamsPreservesNumbers(t *testing.T) {
	body := []byte(`{"model":"openai/gpt-4o","custom_seed":9007199254740993,"ratio":0.10,` +
		`"nested":{"id":18446744073709551615,"list":[1,2.50]}}`)

	extras, err := extractExtraParams(body, chatParamsKnownFields)
	if err != nil {
		t.Fatalf("extractExtraParams: %v", err)
	}
	if got, want := extras["custom_seed"], json.Number("9007199254740993"); got != want {
		t.Fatalf("custom_seed = %#v, want %#v", got, want)
	}
	if got, want := extras["ratio"], json.Number("0.10"); got != want {
		t.Fatalf("ratio = %#v, want %#v", got, want)
	}

	wire, err := providerUtils.MergeExtraParamsIntoJSON([]byte(`{"model":"gpt-4o"}`), extras)
	if err != nil {
		t.Fatalf("MergeExtraParamsIntoJSON: %v", err)
	}
	for _, want := range []string{`"custom_seed":9007199254740993`, `"ratio":0.10`, `"id":18446744073709551615`, `"list":[1,2.50]`} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("wire body %s does not contain %s", wire, want)
		}
	}
}

// TestChatParamsKnownFieldsCoverChatParameters keeps chatParamsKnownFields in
// step with schemas.ChatParameters. A typed field missing from the list is
// decoded twice: once typed, and once as a lossy ExtraParams copy that
// shadows the typed value on the wire when extra params pass through.
func TestChatParamsKnownFieldsCoverChatParameters(t *testing.T) {
	fields := reflect.TypeFor[schemas.ChatParameters]()
	for i := range fields.NumField() {
		name, _, _ := strings.Cut(fields.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		if !chatParamsKnownFields[name] {
			t.Errorf("chat parameter %q is missing from chatParamsKnownFields", name)
		}
	}
	// Input-only spellings accepted by ChatParameters.UnmarshalJSON.
	for _, name := range []string{"reasoning_effort", "reasoning_max_tokens", "reasoning_display"} {
		if !chatParamsKnownFields[name] {
			t.Errorf("chat parameter alias %q is missing from chatParamsKnownFields", name)
		}
	}
}

// TestPrepareChatCompletionRequestExactNumbers pins the provider wire for a
// chat request: the typed seed keeps its exact value and is not shadowed by
// an ExtraParams copy, and an unknown integer passes through verbatim.
func TestPrepareChatCompletionRequestExactNumbers(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetBodyString(`{"model":"openai/gpt-4o","messages":[{"role":"user","content":"hi"}],` +
		`"seed":9007199254740993,"top_p":0.10,"custom_id":9007199254740993}`)

	_, req, err := prepareChatCompletionRequest(ctx, nil)
	if err != nil {
		t.Fatalf("prepareChatCompletionRequest: %v", err)
	}
	if req.Params.Seed == nil || *req.Params.Seed != 9007199254740993 {
		t.Fatalf("seed = %v, want 9007199254740993", req.Params.Seed)
	}
	for _, typed := range []string{"seed", "top_p"} {
		if _, shadowed := req.Params.ExtraParams[typed]; shadowed {
			t.Fatalf("typed field %q duplicated into ExtraParams", typed)
		}
	}

	bctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	bctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	wire, bifrostErr := providerUtils.CheckContextAndGetRequestBody(bctx, req, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openai.ToOpenAIChatRequest(bctx, req), nil
	})
	if bifrostErr != nil {
		t.Fatalf("CheckContextAndGetRequestBody: %v", bifrostErr.Error)
	}
	for _, want := range []string{`"seed":9007199254740993`, `"custom_id":9007199254740993`} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("wire body %s does not contain %s", wire, want)
		}
	}
}
