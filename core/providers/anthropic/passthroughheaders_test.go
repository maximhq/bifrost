package anthropic

import (
	"context"
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// clientHeaders is what Claude Code sends on the OAuth passthrough path: the caller's token
// (the upstream credential), their beta flags, client telemetry, Bifrost's own virtual key,
// and headers an ingress proxy injected.
func clientHeaders() map[string][]string {
	return map[string][]string{
		"authorization":     {"Bearer sk-ant-oat01-caller-token"},
		"anthropic-beta":    {"interleaved-thinking-2025-05-14"},
		"anthropic-version": {"2023-06-01"},
		"content-type":      {"text/plain"},
		"x-app":             {"cli"},
		"x-stainless-lang":  {"js"},
		"x-bf-vk":           {"sk-bf-must-never-leave-the-gateway"},
		"x-forwarded-for":   {"10.30.10.147"},
		"connection":        {"keep-alive"},
	}
}

// TestSetPassthroughHeaders_ForwardsCallerCredentialButNotInternals covers the Anthropic side:
// the caller's OAuth token must reach Anthropic, while Bifrost-internal and hop-by-hop headers
// must not. anthropic-beta is skipped here because MergeBetaHeaders owns the final value.
func TestSetPassthroughHeaders_ForwardsCallerCredentialButNotInternals(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, clientHeaders())

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	providerUtils.SetPassthroughHeaders(ctx, req, schemas.Anthropic, []string{AnthropicBetaHeader})

	forwarded := map[string]string{
		"authorization":     "Bearer sk-ant-oat01-caller-token",
		"anthropic-version": "2023-06-01",
		"x-app":             "cli",
		"x-stainless-lang":  "js",
	}
	for name, want := range forwarded {
		if got := string(req.Header.Peek(name)); got != want {
			t.Errorf("%q should reach Anthropic: got %q, want %q", name, got, want)
		}
	}
	// x-bf-* is Bifrost's own credential; connection is hop-by-hop.
	for _, name := range []string{"x-bf-vk", "connection"} {
		if got := string(req.Header.Peek(name)); got != "" {
			t.Errorf("%q must not be forwarded, got %q", name, got)
		}
	}
	// Owned by the beta pipeline, not by raw passthrough.
	if got := string(req.Header.Peek(AnthropicBetaHeader)); got != "" {
		t.Errorf("anthropic-beta must be left to MergeBetaHeaders, got %q", got)
	}
	// Bifrost owns the body, so it owns Content-Type — a caller value must never override it,
	// regardless of where callers invoke this relative to their own SetContentType.
	if got := string(req.Header.Peek("Content-Type")); got != "" {
		t.Errorf("caller Content-Type must not be forwarded, got %q", got)
	}
}

// TestSetPassthroughHeaders_NoOpWithoutKey — providers other than Anthropic never populate the
// key, so nothing is forwarded. This is the structural guarantee that replaced the old
// clear-the-context-afterwards teardown.
func TestSetPassthroughHeaders_NoOpWithoutKey(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, clientHeaders()) // wrong key on purpose

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	providerUtils.SetPassthroughHeaders(ctx, req, schemas.Anthropic, nil)

	req.Header.VisitAll(func(k, v []byte) {
		t.Errorf("nothing should be forwarded without the passthrough key, got %q: %q", k, v)
	})
}

// TestMergeBetaHeaders_ReadsPassthroughKey is how Bedrock/Vertex/Azure/Mantle still receive the
// caller's beta flags now that they no longer see the raw header set at all.
func TestMergeBetaHeaders_ReadsPassthroughKey(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, clientHeaders())

	got := MergeBetaHeaders(ctx, nil)
	if len(got) != 1 || got[0] != "interleaved-thinking-2025-05-14" {
		t.Fatalf("caller beta flags lost: %v", got)
	}
}

// TestMergeBetaHeaders_DedupesAcrossBothKeys — a value present in both context keys must be
// emitted once.
func TestMergeBetaHeaders_DedupesAcrossBothKeys(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyExtraHeaders, map[string][]string{
		"anthropic-beta": {"interleaved-thinking-2025-05-14"},
	})
	ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, map[string][]string{
		"anthropic-beta": {"interleaved-thinking-2025-05-14,context-management-2025-06-27"},
	})

	got := MergeBetaHeaders(ctx, nil)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped betas, got %v", got)
	}
}

// TestAnthropicPassthroughHeaders_IsReserved — plugins must not be able to inject into the
// passthrough set while restricted writes are blocked (which is how the plugin pipeline runs).
func TestAnthropicPassthroughHeaders_IsReserved(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.BlockRestrictedWrites()
	ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, clientHeaders())
	if v := ctx.Value(schemas.BifrostContextKeyPassthroughHeaders); v != nil {
		t.Errorf("a plugin was able to set the passthrough headers: %v", v)
	}
}

// countPrefixed counts entries in a merged beta list belonging to one beta family.
func countPrefixed(betas []string, prefix string) int {
	n := 0
	for _, b := range betas {
		if strings.HasPrefix(b, prefix) {
			n++
		}
	}
	return n
}

// TestAddMissingBetaHeaders_HonoursCallerVersionFromPassthroughKey covers the interaction
// between auto-injection and the OAuth passthrough split. The caller pins an older MCP beta;
// Bifrost's default is the current one. The "passthrough wins" guard has to see the caller's
// value — which now lives in the passthrough key, not ExtraHeaders — or it injects its own
// version and MergeBetaHeaders (exact-token dedup, not prefix) forwards BOTH versions of the
// same family, which Anthropic rejects.
func TestAddMissingBetaHeaders_HonoursCallerVersionFromPassthroughKey(t *testing.T) {
	const callerMCPBeta = "mcp-client-2025-04-04" // deliberately older than AnthropicMCPClientBetaHeader

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, map[string][]string{
		"anthropic-beta": {callerMCPBeta},
	})

	req := &AnthropicMessageRequest{MCPServers: []AnthropicMCPServerV2{{}}}
	if err := AddMissingBetaHeadersToContext(ctx, req, ""); err != nil {
		t.Fatalf("AddMissingBetaHeadersToContext: %v", err)
	}

	merged := MergeBetaHeaders(ctx, nil)
	if n := countPrefixed(merged, "mcp-client-"); n != 1 {
		t.Fatalf("expected exactly 1 mcp-client beta, got %d: %v", n, merged)
	}
	if merged[0] != callerMCPBeta {
		t.Errorf("caller's pinned version should win: got %q, want %q", merged[0], callerMCPBeta)
	}
}

// TestAddMissingBetaHeaders_StillInjectsWhenCallerSentNone — the guard must not over-trigger:
// with no caller beta at all, auto-injection still has to happen.
func TestAddMissingBetaHeaders_StillInjectsWhenCallerSentNone(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	req := &AnthropicMessageRequest{MCPServers: []AnthropicMCPServerV2{{}}}
	if err := AddMissingBetaHeadersToContext(ctx, req, ""); err != nil {
		t.Fatalf("AddMissingBetaHeadersToContext: %v", err)
	}

	merged := MergeBetaHeaders(ctx, nil)
	if countPrefixed(merged, "mcp-client-") != 1 || merged[0] != AnthropicMCPClientBetaHeader {
		t.Fatalf("expected auto-injected %q, got %v", AnthropicMCPClientBetaHeader, merged)
	}
}

// TestSetPassthroughHeaders_ContentTypeSurvivesEitherOrdering reproduces the two call
// orderings that exist in this file. completeRequest sets the content type AFTER
// SetPassthroughHeaders; both streaming handlers set it BEFORE. Rather than depend on call
// order, SetPassthroughHeaders drops the caller's content-type entirely — Bifrost owns the
// body it sends, so it owns the header. This test pins that, so a future call site added in
// either order cannot resurrect the bug.
func TestSetPassthroughHeaders_ContentTypeSurvivesEitherOrdering(t *testing.T) {
	hostile := map[string][]string{
		"content-type": {"text/plain"}, // a caller trying to override the JSON body's type
		"x-app":        {"cli"},
	}

	t.Run("SetContentType before (streaming handlers)", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, hostile)

		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		req.Header.SetContentType("application/json")
		providerUtils.SetPassthroughHeaders(ctx, req, schemas.Anthropic, []string{AnthropicBetaHeader})

		if got := string(req.Header.ContentType()); got != "application/json" {
			t.Errorf("caller overrode content type: got %q, want application/json", got)
		}
		if got := string(req.Header.Peek("x-app")); got != "cli" {
			t.Errorf("other caller headers should still forward, got %q", got)
		}
	})

	t.Run("SetContentType after (completeRequest)", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, hostile)

		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		providerUtils.SetPassthroughHeaders(ctx, req, schemas.Anthropic, []string{AnthropicBetaHeader})
		req.Header.SetContentType("application/json")

		if got := string(req.Header.ContentType()); got != "application/json" {
			t.Errorf("content type wrong: got %q, want application/json", got)
		}
	})
}

// TestSetPassthroughHeaders_OnlyAnthropicReceivesThem is the enforcement test for the
// invariant. HandleAnthropicChatCompletionStreaming / HandleAnthropicResponsesStream /
// completeRequest are shared by every Anthropic-wire-format provider, so without a gate the
// caller's OAuth credential and client metadata would be forwarded to Azure, Vertex, Bedrock
// Mantle, vLLM, SGL, DeepSeek and Fireworks too.
func TestSetPassthroughHeaders_OnlyAnthropicReceivesThem(t *testing.T) {
	sharers := []schemas.ModelProvider{
		schemas.Azure, schemas.Vertex, schemas.BedrockMantle, schemas.Bedrock,
		schemas.VLLM, schemas.SGL, schemas.DeepSeek, schemas.Fireworks,
	}

	for _, provider := range sharers {
		t.Run(string(provider), func(t *testing.T) {
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, clientHeaders())

			req := fasthttp.AcquireRequest()
			defer fasthttp.ReleaseRequest(req)
			providerUtils.SetPassthroughHeaders(ctx, req, provider, nil)

			req.Header.VisitAll(func(k, v []byte) {
				t.Errorf("%s must not receive caller passthrough headers, got %q: %q", provider, k, v)
			})
		})
	}

	t.Run("anthropic still receives them", func(t *testing.T) {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyPassthroughHeaders, clientHeaders())

		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		providerUtils.SetPassthroughHeaders(ctx, req, schemas.Anthropic, nil)

		if got := string(req.Header.Peek("authorization")); got != "Bearer sk-ant-oat01-caller-token" {
			t.Errorf("Anthropic must still receive the caller's OAuth token, got %q", got)
		}
	})
}
