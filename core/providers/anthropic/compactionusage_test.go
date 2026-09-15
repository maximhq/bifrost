package anthropic

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBillableAnthropicUsage_CompactionPlusMessage(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{
		InputTokens:  23000,
		OutputTokens: 1000,
		Iterations: []AnthropicUsage{
			{Type: schemas.Ptr(AnthropicUsageIterationTypeCompaction), InputTokens: 180000, OutputTokens: 3500},
			{Type: schemas.Ptr("message"), InputTokens: 23000, OutputTokens: 1000},
		},
	}
	got := billableAnthropicUsage(usage)
	if got.InputTokens != 203000 {
		t.Fatalf("InputTokens = %d, want 203000", got.InputTokens)
	}
	if got.OutputTokens != 4500 {
		t.Fatalf("OutputTokens = %d, want 4500", got.OutputTokens)
	}
	if len(got.Iterations) != 0 {
		t.Fatalf("expected synthetic usage to drop iterations, got %d", len(got.Iterations))
	}
}

func TestBillableAnthropicUsage_NoIterationsUnchanged(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{InputTokens: 42, OutputTokens: 7}
	got := billableAnthropicUsage(usage)
	if got != usage {
		t.Fatal("expected same pointer when iterations absent")
	}
}

func TestBillableAnthropicUsage_MessageOnlyIterations(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{
		InputTokens:  23000,
		OutputTokens: 1000,
		Iterations: []AnthropicUsage{
			{Type: schemas.Ptr("message"), InputTokens: 23000, OutputTokens: 1000},
		},
	}
	got := billableAnthropicUsage(usage)
	if got.InputTokens != 23000 || got.OutputTokens != 1000 {
		t.Fatalf("got input=%d output=%d, want 23000/1000", got.InputTokens, got.OutputTokens)
	}
}

func TestBillableAnthropicUsage_DoesNotMutateInput(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{
		InputTokens:  100,
		OutputTokens: 50,
		OutputTokensDetails: &AnthropicOutputTokensDetails{
			ThinkingTokens: 10,
		},
		ServerToolUse: &AnthropicServerToolUseUsage{
			WebSearchRequests: 1,
		},
		Iterations: []AnthropicUsage{
			{
				Type:         schemas.Ptr(AnthropicUsageIterationTypeCompaction),
				OutputTokens: 500,
				OutputTokensDetails: &AnthropicOutputTokensDetails{
					ThinkingTokens: 400,
				},
				ServerToolUse: &AnthropicServerToolUseUsage{
					WebSearchRequests: 5,
				},
			},
		},
	}
	origThinking := usage.OutputTokensDetails.ThinkingTokens
	origWebSearch := usage.ServerToolUse.WebSearchRequests

	got := billableAnthropicUsage(usage)

	if usage.OutputTokensDetails.ThinkingTokens != origThinking {
		t.Fatalf("mutated input ThinkingTokens: got %d want %d", usage.OutputTokensDetails.ThinkingTokens, origThinking)
	}
	if usage.ServerToolUse.WebSearchRequests != origWebSearch {
		t.Fatalf("mutated input WebSearchRequests: got %d want %d", usage.ServerToolUse.WebSearchRequests, origWebSearch)
	}
	if got.OutputTokensDetails == nil || got.OutputTokensDetails.ThinkingTokens != 400 {
		t.Fatalf("billable ThinkingTokens = %v, want 400", got.OutputTokensDetails)
	}
	if got.ServerToolUse == nil || got.ServerToolUse.WebSearchRequests != 5 {
		t.Fatalf("billable WebSearchRequests = %v, want 5", got.ServerToolUse)
	}
}

func TestBillableAnthropicUsage_FallbackIterationsStayTopLevel(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{
		InputTokens:  412,
		OutputTokens: 264,
		Iterations: []AnthropicUsage{
			{Type: schemas.Ptr("message"), Model: schemas.Ptr("claude-fable-5"), InputTokens: 535, OutputTokens: 0},
			{Type: schemas.Ptr(AnthropicUsageIterationTypeFallbackMessage), Model: schemas.Ptr("claude-opus-4-8"), InputTokens: 412, OutputTokens: 264},
		},
	}
	got := billableAnthropicUsage(usage)
	if got.InputTokens != 412 || got.OutputTokens != 264 {
		t.Fatalf("got input=%d output=%d, want top-level serving attempt 412/264", got.InputTokens, got.OutputTokens)
	}
}

func TestBillableAnthropicUsage_ReplicaCacheAndThinking(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{
		InputTokens:              2,
		OutputTokens:             74,
		CacheCreationInputTokens: 445,
		CacheReadInputTokens:     3146,
		CacheCreation: AnthropicUsageCacheCreation{
			Ephemeral5mInputTokens: 445,
		},
		OutputTokensDetails: &AnthropicOutputTokensDetails{ThinkingTokens: 50},
		Iterations: []AnthropicUsage{
			{
				Type:                     schemas.Ptr(AnthropicUsageIterationTypeCompaction),
				InputTokens:              106576,
				OutputTokens:             312,
				CacheCreationInputTokens: 3146,
				CacheCreation: AnthropicUsageCacheCreation{
					Ephemeral5mInputTokens: 3146,
				},
			},
			{
				Type:                     schemas.Ptr("message"),
				InputTokens:              2,
				OutputTokens:             74,
				CacheCreationInputTokens: 445,
				CacheReadInputTokens:     3146,
				CacheCreation: AnthropicUsageCacheCreation{
					Ephemeral5mInputTokens: 445,
				},
				OutputTokensDetails: &AnthropicOutputTokensDetails{ThinkingTokens: 50},
			},
		},
	}
	got := billableAnthropicUsage(usage)
	if got.InputTokens != 106578 {
		t.Fatalf("InputTokens = %d, want 106578", got.InputTokens)
	}
	if got.OutputTokens != 386 {
		t.Fatalf("OutputTokens = %d, want 386", got.OutputTokens)
	}
	if got.CacheCreationInputTokens != 3591 {
		t.Fatalf("CacheCreationInputTokens = %d, want 3591", got.CacheCreationInputTokens)
	}
	if got.CacheReadInputTokens != 3146 {
		t.Fatalf("CacheReadInputTokens = %d, want 3146", got.CacheReadInputTokens)
	}
	if got.CacheCreation.Ephemeral5mInputTokens != 3591 {
		t.Fatalf("CachedWriteTokens5m = %d, want 3591", got.CacheCreation.Ephemeral5mInputTokens)
	}
	if got.OutputTokensDetails == nil || got.OutputTokensDetails.ThinkingTokens != 50 {
		t.Fatalf("ThinkingTokens = %v, want 50", got.OutputTokensDetails)
	}
	if got.OutputTokensDetails.ThinkingTokens > got.OutputTokens {
		t.Fatal("invariant violated: ThinkingTokens > OutputTokens")
	}
}

func TestConvertAnthropicUsageToBifrostUsage_BillsCompactionIterations(t *testing.T) {
	t.Parallel()

	usage := &AnthropicUsage{
		InputTokens:  23000,
		OutputTokens: 1000,
		Iterations: []AnthropicUsage{
			{Type: schemas.Ptr(AnthropicUsageIterationTypeCompaction), InputTokens: 180000, OutputTokens: 3500},
			{Type: schemas.Ptr("message"), InputTokens: 23000, OutputTokens: 1000},
		},
	}
	got := ConvertAnthropicUsageToBifrostUsage(usage)
	if got == nil {
		t.Fatal("nil usage")
	}
	if got.OutputTokens != 4500 {
		t.Fatalf("OutputTokens = %d, want 4500", got.OutputTokens)
	}
	if got.InputTokens != 203000 {
		t.Fatalf("InputTokens = %d, want 203000", got.InputTokens)
	}
	if len(got.Iterations) != 2 {
		t.Fatalf("expected original iterations preserved, got %d", len(got.Iterations))
	}
}

func TestToBifrostChatResponse_BillsCompactionIterations(t *testing.T) {
	t.Parallel()

	resp := &AnthropicMessageResponse{
		Usage: &AnthropicUsage{
			InputTokens:  23000,
			OutputTokens: 1000,
			Iterations: []AnthropicUsage{
				{Type: schemas.Ptr(AnthropicUsageIterationTypeCompaction), InputTokens: 180000, OutputTokens: 3500},
				{Type: schemas.Ptr("message"), InputTokens: 23000, OutputTokens: 1000},
			},
		},
	}
	got := resp.ToBifrostChatResponse(nil)
	if got.Usage == nil {
		t.Fatal("nil usage")
	}
	if got.Usage.CompletionTokens != 4500 {
		t.Fatalf("CompletionTokens = %d, want 4500", got.Usage.CompletionTokens)
	}
	if got.Usage.PromptTokens != 203000 {
		t.Fatalf("PromptTokens = %d, want 203000", got.Usage.PromptTokens)
	}
}

func TestAccumulateAnthropicResponsesUsage_StreamReplica(t *testing.T) {
	t.Parallel()

	usage := &schemas.ResponsesResponseUsage{}
	billed := &schemas.BifrostLLMUsage{}

	start := &AnthropicUsage{
		InputTokens:              106576,
		OutputTokens:             5,
		CacheCreationInputTokens: 3146,
		CacheCreation: AnthropicUsageCacheCreation{
			Ephemeral5mInputTokens: 3146,
		},
	}
	accumulateAnthropicResponsesUsage(usage, billed, start)

	delta := &AnthropicUsage{
		InputTokens:              2,
		OutputTokens:             74,
		CacheCreationInputTokens: 445,
		CacheReadInputTokens:     3146,
		CacheCreation: AnthropicUsageCacheCreation{
			Ephemeral5mInputTokens: 445,
		},
		OutputTokensDetails: &AnthropicOutputTokensDetails{ThinkingTokens: 50},
		Iterations: []AnthropicUsage{
			{
				Type:                     schemas.Ptr(AnthropicUsageIterationTypeCompaction),
				InputTokens:              106576,
				OutputTokens:             312,
				CacheCreationInputTokens: 3146,
				CacheCreation: AnthropicUsageCacheCreation{
					Ephemeral5mInputTokens: 3146,
				},
			},
			{
				Type:                     schemas.Ptr("message"),
				InputTokens:              2,
				OutputTokens:             74,
				CacheCreationInputTokens: 445,
				CacheReadInputTokens:     3146,
				CacheCreation: AnthropicUsageCacheCreation{
					Ephemeral5mInputTokens: 445,
				},
				OutputTokensDetails: &AnthropicOutputTokensDetails{ThinkingTokens: 50},
			},
		},
	}
	accumulateAnthropicResponsesUsage(usage, billed, delta)

	if usage.OutputTokens != 386 {
		t.Fatalf("response OutputTokens = %d, want 386", usage.OutputTokens)
	}
	if usage.InputTokens != 106578 {
		t.Fatalf("response InputTokens = %d, want 106578", usage.InputTokens)
	}
	if usage.InputTokensDetails == nil || usage.InputTokensDetails.CachedWriteTokens != 3591 {
		t.Fatalf("CachedWriteTokens = %v, want 3591", usage.InputTokensDetails)
	}
	if usage.InputTokensDetails.CachedReadTokens != 3146 {
		t.Fatalf("CachedReadTokens = %d, want 3146", usage.InputTokensDetails.CachedReadTokens)
	}
	if usage.OutputTokensDetails == nil || usage.OutputTokensDetails.ReasoningTokens != 50 {
		t.Fatalf("ReasoningTokens = %v, want 50", usage.OutputTokensDetails)
	}

	normalizeCachedUsage(billed)
	if billed.CompletionTokens != 386 {
		t.Fatalf("billed CompletionTokens = %d, want 386", billed.CompletionTokens)
	}
	if billed.PromptTokens != 113315 {
		t.Fatalf("billed PromptTokens = %d, want 113315 (106578+3591+3146)", billed.PromptTokens)
	}
}

func TestPassthroughStream_CompactionIterations(t *testing.T) {
	t.Parallel()

	var acc AnthropicPassthroughStreamUsage
	acc.ObserveEvent([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":106576,"output_tokens":5,"cache_creation_input_tokens":3146,"cache_creation":{"ephemeral_5m_input_tokens":3146}}}}`))
	got := acc.ObserveEvent([]byte(`{"type":"message_delta","usage":{"input_tokens":2,"output_tokens":74,"cache_creation_input_tokens":445,"cache_read_input_tokens":3146,"cache_creation":{"ephemeral_5m_input_tokens":445},"output_tokens_details":{"thinking_tokens":50},"iterations":[{"type":"compaction","input_tokens":106576,"output_tokens":312,"cache_creation_input_tokens":3146,"cache_creation":{"ephemeral_5m_input_tokens":3146}},{"type":"message","input_tokens":2,"output_tokens":74,"cache_creation_input_tokens":445,"cache_read_input_tokens":3146,"cache_creation":{"ephemeral_5m_input_tokens":445},"output_tokens_details":{"thinking_tokens":50}}]}}`))

	if got == nil || got.LLMUsage == nil {
		t.Fatal("expected passthrough usage")
	}
	if got.LLMUsage.CompletionTokens != 386 {
		t.Fatalf("CompletionTokens = %d, want 386", got.LLMUsage.CompletionTokens)
	}
	if got.LLMUsage.PromptTokensDetails == nil || got.LLMUsage.PromptTokensDetails.CachedWriteTokens != 3591 {
		t.Fatalf("CachedWriteTokens = %v, want 3591", got.LLMUsage.PromptTokensDetails)
	}
	if got.LLMUsage.PromptTokensDetails.CachedReadTokens != 3146 {
		t.Fatalf("CachedReadTokens = %d, want 3146", got.LLMUsage.PromptTokensDetails.CachedReadTokens)
	}
	uncached := got.LLMUsage.PromptTokens - got.LLMUsage.PromptTokensDetails.CachedReadTokens - got.LLMUsage.PromptTokensDetails.CachedWriteTokens
	if uncached != 106578 {
		t.Fatalf("uncached prompt = %d, want 106578", uncached)
	}
}
