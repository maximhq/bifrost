package anthropic

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Regression tests for #6153: a custom provider (base_provider_type: anthropic,
// e.g. MiniMax) is absent from ProviderFeatures, and the fail-open
// "!hasProvider || feature" gating forwarded Anthropic-API-only server tools
// (advisor) to it. The converted advisor stub carries a name but no
// input_schema, which strict upstreams reject with 400. Anthropic-only
// capabilities must fail closed for unknown providers, matching enumerated
// third-party providers like DeepSeek.

const customProvider = schemas.ModelProvider("minimax")

func toolsWithAdvisor() []schemas.ResponsesTool {
	name := "TaskList"
	return []schemas.ResponsesTool{
		{Type: schemas.ResponsesToolTypeAdvisor},
		{Type: schemas.ResponsesToolTypeFunction, Name: &name},
		{Type: schemas.ResponsesToolTypeWebSearch},
	}
}

func TestValidateResponsesToolsForProvider_CustomProviderDropsAdvisor(t *testing.T) {
	keep, dropped := ValidateResponsesToolsForProvider(toolsWithAdvisor(), customProvider)

	if len(dropped) != 1 || dropped[0] != string(schemas.ResponsesToolTypeAdvisor) {
		t.Fatalf("expected exactly the advisor tool dropped for unknown provider, got dropped=%v", dropped)
	}
	for _, tool := range keep {
		if tool.Type == schemas.ResponsesToolTypeAdvisor {
			t.Fatalf("advisor tool kept for unknown provider %q", customProvider)
		}
	}
	// Fail-open surface for custom providers is preserved: function and
	// non-Anthropic-only server tools still pass through.
	if len(keep) != 2 {
		t.Fatalf("expected function + web_search kept, got %d tools: %v", len(keep), keep)
	}
}

func TestValidateResponsesToolsForProvider_AnthropicKeepsAdvisor(t *testing.T) {
	keep, dropped := ValidateResponsesToolsForProvider(toolsWithAdvisor(), schemas.Anthropic)
	if len(dropped) != 0 {
		t.Fatalf("expected no tools dropped on Anthropic, got %v", dropped)
	}
	if len(keep) != 3 {
		t.Fatalf("expected all 3 tools kept on Anthropic, got %d", len(keep))
	}
}

func TestValidateResponsesToolsForProvider_DeepSeekDropsAdvisor(t *testing.T) {
	// DeepSeek is the enumerated third-party Anthropic-compatible provider;
	// unknown custom providers must get the same advisor treatment.
	_, dropped := ValidateResponsesToolsForProvider(toolsWithAdvisor(), schemas.DeepSeek)
	found := false
	for _, d := range dropped {
		if d == string(schemas.ResponsesToolTypeAdvisor) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected advisor dropped on DeepSeek, got dropped=%v", dropped)
	}
}

func TestValidateToolsForProvider_CustomProviderRejectsAdvisor(t *testing.T) {
	if err := ValidateToolsForProvider(toolsWithAdvisor(), customProvider); err == nil {
		t.Fatalf("expected error for advisor tool on unknown provider %q, got nil", customProvider)
	}
	if err := ValidateToolsForProvider(toolsWithAdvisor(), schemas.Anthropic); err != nil {
		t.Fatalf("expected advisor allowed on Anthropic, got %v", err)
	}
}

func TestValidateChatToolsForProvider_CustomProviderDropsAdvisor(t *testing.T) {
	tools := []schemas.ChatTool{
		{Type: schemas.ChatToolType(AnthropicToolTypeAdvisor20260301)},
		{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "TaskList"}},
	}

	keep, dropped := ValidateChatToolsForProvider(tools, customProvider)
	if len(dropped) != 1 || dropped[0] != string(AnthropicToolTypeAdvisor20260301) {
		t.Fatalf("expected advisor chat tool dropped for unknown provider, got dropped=%v", dropped)
	}
	if len(keep) != 1 || keep[0].Function == nil {
		t.Fatalf("expected only the function tool kept, got %v", keep)
	}

	keep, dropped = ValidateChatToolsForProvider(tools, schemas.Anthropic)
	if len(dropped) != 0 || len(keep) != 2 {
		t.Fatalf("expected advisor chat tool kept on Anthropic, got keep=%d dropped=%v", len(keep), dropped)
	}
}

// A directly constructed ChatTool can carry an Anthropic-only server tool Type
// alongside a populated Function — wire input is canonicalized by
// UnmarshalJSON, in-memory values are not. The Anthropic-only gate must win
// over the function/custom fast path.
func TestValidateChatToolsForProvider_CustomProviderDropsAdvisorWithFunctionSet(t *testing.T) {
	tools := []schemas.ChatTool{
		{Type: schemas.ChatToolType(AnthropicToolTypeAdvisor20260301), Function: &schemas.ChatToolFunction{Name: "smuggled"}},
		{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "TaskList"}},
	}

	keep, dropped := ValidateChatToolsForProvider(tools, customProvider)
	if len(dropped) != 1 || dropped[0] != string(AnthropicToolTypeAdvisor20260301) {
		t.Fatalf("expected hybrid advisor tool dropped for unknown provider, got dropped=%v", dropped)
	}
	if len(keep) != 1 || keep[0].Function == nil || keep[0].Function.Name != "TaskList" {
		t.Fatalf("expected only the plain function tool kept, got %v", keep)
	}
}

func TestAddMissingBetaHeaders_CustomProviderNoAdvisorHeader(t *testing.T) {
	advisorType := AnthropicToolTypeAdvisor20260301
	req := &AnthropicMessageRequest{
		Tools: []AnthropicTool{{Type: &advisorType, Name: string(AnthropicToolNameAdvisor)}},
	}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if err := AddMissingBetaHeadersToContext(ctx, req, customProvider); err != nil {
		t.Fatalf("AddMissingBetaHeadersToContext: %v", err)
	}
	for _, b := range MergeBetaHeaders(ctx, nil) {
		if strings.HasPrefix(b, AnthropicAdvisorBetaHeaderPrefix) {
			t.Fatalf("advisor beta header injected for unknown provider %q: %v", customProvider, b)
		}
	}

	ctx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if err := AddMissingBetaHeadersToContext(ctx, req, schemas.Anthropic); err != nil {
		t.Fatalf("AddMissingBetaHeadersToContext: %v", err)
	}
	found := false
	for _, b := range MergeBetaHeaders(ctx, nil) {
		if b == AnthropicAdvisorBetaHeader {
			found = true
		}
	}
	if !found {
		t.Fatalf("advisor beta header not injected for Anthropic")
	}
}
