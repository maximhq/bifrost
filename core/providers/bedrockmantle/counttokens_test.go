package bedrockmantle

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// TestMantleAnthropicCountTokensURL pins the URL to the one AWS documents at
// https://docs.aws.amazon.com/bedrock/latest/userguide/count-tokens.html#count-tokens-mantle
// ("The bedrock-mantle endpoint exposes Anthropic's count_tokens API at
// /anthropic/v1/messages/count_tokens").
func TestMantleAnthropicCountTokensURL(t *testing.T) {
	tests := []struct {
		region string
		want   string
	}{
		{region: "us-east-1", want: "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages/count_tokens"},
		{region: "eu-north-1", want: "https://bedrock-mantle.eu-north-1.api.aws/anthropic/v1/messages/count_tokens"},
		{region: "us-gov-east-1", want: "https://bedrock-mantle.us-gov-east-1.api.aws/anthropic/v1/messages/count_tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			got := mantleAnthropicCountTokensURL(nil, tt.region)
			if got != tt.want {
				t.Fatalf("mantleAnthropicCountTokensURL(%q) = %q, want %q", tt.region, got, tt.want)
			}
			// count_tokens must stay a suffix of the messages URL, so a future change to the
			// messages host/path can't silently desync the two surfaces.
			if want := mantleAnthropicURL(nil, tt.region) + "/count_tokens"; got != want {
				t.Fatalf("count-tokens URL %q is not the messages URL + /count_tokens (%q)", got, want)
			}
		})
	}
}

// TestCountTokensRegionResolution verifies the count-tokens URL honours the same region
// precedence as the other mantle surfaces (model prefix > alias > key config > default).
func TestCountTokensRegionResolution(t *testing.T) {
	provider := &BedrockMantleProvider{}

	tests := []struct {
		name  string
		key   schemas.Key
		model string
		want  string
	}{
		{name: "default region", model: "anthropic.claude-opus-4-8", want: defaultMantleRegion},
		{
			name:  "key config region",
			key:   schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{Region: schemas.NewSecretVar("eu-west-3")}},
			model: "anthropic.claude-opus-4-8",
			want:  "eu-west-3",
		},
		{
			name:  "model prefix wins over key config",
			key:   schemas.Key{BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{Region: schemas.NewSecretVar("eu-west-3")}},
			model: "ap-southeast-2/anthropic.claude-opus-4-8",
			want:  "ap-southeast-2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()

			got := mantleAnthropicCountTokensURL(nil, provider.resolveRegion(ctx, tt.key, tt.model))
			want := "https://bedrock-mantle." + tt.want + ".api.aws/anthropic/v1/messages/count_tokens"
			if got != want {
				t.Fatalf("count-tokens URL = %q, want %q", got, want)
			}
		})
	}
}

// TestCountTokensRejectsNonAnthropicModels covers the AWS-documented constraint that
// "/anthropic/v1/messages is Claude-specific; non-Anthropic models on bedrock-mantle return
// The model 'X' does not support the '/anthropic/v1/messages' API". Bifrost fails these
// locally instead of spending a round trip on a guaranteed upstream rejection.
func TestCountTokensRejectsNonAnthropicModels(t *testing.T) {
	provider := &BedrockMantleProvider{}

	for _, model := range []string{"openai.gpt-oss-120b", "openai.gpt-5.2", "google.gemma-4-27b"} {
		t.Run(model, func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			defer ctx.Cancel()

			resp, bifrostErr := provider.CountTokens(ctx, schemas.Key{}, &schemas.BifrostResponsesRequest{
				Provider: schemas.BedrockMantle,
				Model:    model,
			})
			if resp != nil {
				t.Fatalf("expected nil response for non-Anthropic model %q, got %+v", model, resp)
			}
			if bifrostErr == nil || bifrostErr.Error == nil {
				t.Fatalf("expected an unsupported-operation error for non-Anthropic model %q", model)
			}
			if bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
				t.Fatalf("expected code unsupported_operation, got %+v", bifrostErr.Error.Code)
			}
			if bifrostErr.ExtraFields.RequestType != schemas.CountTokensRequest {
				t.Fatalf("expected request type %q, got %q", schemas.CountTokensRequest, bifrostErr.ExtraFields.RequestType)
			}
		})
	}
}

// TestCountTokensRequestBody verifies the body Bifrost puts on the wire matches the Anthropic
// count_tokens shape AWS documents: model / messages retained, and the fields the endpoint
// rejects removed. AWS notes the endpoint "validates the request body using the same schema as
// the corresponding inference endpoint", so leftover inference-only fields are a hard 400.
func TestCountTokensRequestBody(t *testing.T) {
	buildBody := func(t *testing.T, model, bareModel, rawBody string) []byte {
		t.Helper()
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		defer ctx.Cancel()
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

		body, err := anthropic.BuildAnthropicResponsesRequestBody(ctx, &schemas.BifrostResponsesRequest{
			Provider:       schemas.BedrockMantle,
			Model:          model,
			RawRequestBody: []byte(rawBody),
		}, anthropic.AnthropicRequestBuildConfig{
			Provider:      schemas.BedrockMantle,
			Model:         bareModel,
			IsCountTokens: true,
			ValidateTools: true,
			// CountTokens passes the first three fields; IsStreaming and ExcludeFields are what
			// HandleAnthropicCountTokensRequest injects before it reaches the builder, so they
			// are set here too or this test would not exercise the production shape.
			IsStreaming:   false,
			ExcludeFields: []string{"max_tokens", "temperature"},
		})
		if err != nil {
			t.Fatalf("unexpected error building count-tokens body: %v", err)
		}
		return body
	}

	t.Run("strips fields the count_tokens schema rejects", func(t *testing.T) {
		body := buildBody(t, "anthropic.claude-opus-4-8", "anthropic.claude-opus-4-8",
			`{"model":"anthropic.claude-opus-4-8","max_tokens":1024,"temperature":0.7,"fallback_credit_token":"tok","messages":[{"role":"user","content":"How many tokens is this prompt?"}]}`)

		for _, field := range []string{"max_tokens", "temperature", "fallback_credit_token"} {
			if providerUtils.JSONFieldExists(body, field) {
				t.Errorf("expected %q to be stripped from the count-tokens body", field)
			}
		}
		for _, field := range []string{"model", "messages"} {
			if !providerUtils.JSONFieldExists(body, field) {
				t.Errorf("expected %q to be retained in the count-tokens body", field)
			}
		}
	})

	t.Run("sends the bare model when the request carries a region prefix", func(t *testing.T) {
		// CountTokens splits the region off the model the same way the messages surface does:
		// the region addresses the host, the bare id goes in the body.
		region, bareModel := parseBedrockRegionAndModel("eu-north-1/anthropic.claude-opus-4-8")
		if region != "eu-north-1" || bareModel != "anthropic.claude-opus-4-8" {
			t.Fatalf("parseBedrockRegionAndModel = (%q, %q), want (eu-north-1, anthropic.claude-opus-4-8)", region, bareModel)
		}

		body := buildBody(t, "eu-north-1/anthropic.claude-opus-4-8", bareModel,
			`{"model":"eu-north-1/anthropic.claude-opus-4-8","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)

		if got := providerUtils.GetJSONField(body, "model").String(); got != bareModel {
			t.Fatalf("body model = %q, want %q", got, bareModel)
		}
		if strings.Contains(string(body), "eu-north-1/") {
			t.Fatalf("region prefix leaked into the count-tokens body: %s", body)
		}
	})
}

// TestCountTokensSignsTheBodyItSends pins the invariant SigV4 depends on: the bytes handed to
// the signer are the bytes fasthttp puts on the wire. AWS signs a SHA-256 of the payload into
// x-amz-content-sha256 and returns 403 SignatureDoesNotMatch when the arriving body differs, so
// in large payload mode the passthrough stream must be materialized before signing rather than
// signing the converted body. req.Body() draining a body stream is the fasthttp behaviour that
// makes this work; this test fails if a fasthttp upgrade changes it.
func TestCountTokensSignsTheBodyItSends(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	defer ctx.Cancel()

	payload := `{"model":"bedrock_mantle/anthropic.claude-opus-4-8","messages":[{"role":"user","content":"how many tokens?"}]}`
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMode, true)
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadReader, strings.NewReader(payload))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentLength, len(payload))
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadContentType, "application/json")
	ctx.SetValue(schemas.BifrostContextKeyLargePayloadMetadata, &schemas.LargePayloadMetadata{
		Model: "bedrock_mantle/anthropic.claude-opus-4-8",
	})

	req := &fasthttp.Request{}
	if !providerUtils.ApplyLargePayloadRequestBodyWithModelNormalization(ctx, req, schemas.BedrockMantle) {
		t.Fatal("expected large payload passthrough to apply the streaming body")
	}

	// This is the exact expression CountTokens hands to the signer.
	signedBody := req.Body()
	if len(signedBody) == 0 {
		t.Fatal("expected req.Body() to materialize the passthrough stream, got an empty body")
	}
	if !bytes.Equal(signedBody, req.Body()) {
		t.Fatal("req.Body() is not stable across calls, so the signed body can differ from the sent body")
	}
	if strings.Contains(string(signedBody), "bedrock_mantle/") {
		t.Fatalf("provider prefix leaked into the signed body: %s", signedBody)
	}
	if got := providerUtils.GetJSONField(signedBody, "model").String(); got != "anthropic.claude-opus-4-8" {
		t.Fatalf("signed body model = %q, want %q", got, "anthropic.claude-opus-4-8")
	}
}
