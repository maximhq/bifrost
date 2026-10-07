package anthropic

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
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
	if len(usage.Iterations) != 2 {
		t.Fatalf("Iterations = %d, want 2 (compaction + message)", len(usage.Iterations))
	}
	if it := usage.Iterations[0]; it.Type == nil || *it.Type != AnthropicUsageIterationTypeCompaction || it.OutputTokens != 312 {
		t.Fatalf("Iterations[0] = %+v, want compaction with 312 output tokens", it)
	}
	if it := usage.Iterations[1]; it.Type == nil || *it.Type != "message" || it.OutputTokens != 74 {
		t.Fatalf("Iterations[1] = %+v, want message with 74 output tokens", it)
	}

	// A later usage event without iterations must not erase the breakdown.
	accumulateAnthropicResponsesUsage(usage, billed, &AnthropicUsage{OutputTokens: 74})
	if len(usage.Iterations) != 2 {
		t.Fatalf("Iterations after iteration-less event = %d, want 2", len(usage.Iterations))
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

// A cache READ turn must surface cached_read_tokens, and therefore cached_tokens on
// the wire. This is the streaming shape behind harness rows 63.4/63.6 (#6180), which
// assert `usage.input_tokens_details.cached_tokens > 0` after a paired [write] warmed
// the prefix.
//
// The distinction the test pins is the one that made those rows hard to read when they
// failed: a cache WRITE and a cache READ both mean "caching engaged", but only a read
// may appear as cached_tokens — MarshalJSON aliases cached_tokens to CachedReadTokens
// alone, deliberately, so OpenAI-spec consumers never price a write as a read
// (schemas/responses.go). So a stream reporting cache_creation_input_tokens and nothing
// else must marshal cached_tokens 0, and that zero is a faithful report of an upstream
// miss rather than a mapping bug. Nothing covered either half before; the only signal
// was a live harness row, which cannot separate "Bifrost dropped the read" from "the
// provider never had the prefix".
func TestAccumulateAnthropicResponsesUsage_CacheReadSurfacesCachedTokens(t *testing.T) {
	t.Parallel()

	marshalDetails := func(t *testing.T, u *schemas.ResponsesResponseUsage) string {
		t.Helper()
		if u.InputTokensDetails == nil {
			t.Fatalf("InputTokensDetails must be populated, got %+v", u)
		}
		data, err := sonic.Marshal(u.InputTokensDetails)
		if err != nil {
			t.Fatalf("marshal input_tokens_details: %v", err)
		}
		return string(data)
	}

	// Turn 1, the [write]: Anthropic bills the cold prefix as cache creation. Usage
	// arrives on message_start, which is the only frame carrying it for a read turn.
	t.Run("write turn reports no cached_tokens", func(t *testing.T) {
		usage := &schemas.ResponsesResponseUsage{}
		billed := &schemas.BifrostLLMUsage{}
		accumulateAnthropicResponsesUsage(usage, billed, &AnthropicUsage{
			InputTokens:              14,
			CacheCreationInputTokens: 4759,
			CacheCreation:            AnthropicUsageCacheCreation{Ephemeral5mInputTokens: 4759},
		})
		accumulateAnthropicResponsesUsage(usage, billed, &AnthropicUsage{OutputTokens: 5})

		if got := usage.InputTokensDetails.CachedWriteTokens; got != 4759 {
			t.Fatalf("CachedWriteTokens = %d, want 4759", got)
		}
		if got := usage.InputTokensDetails.CachedReadTokens; got != 0 {
			t.Fatalf("CachedReadTokens = %d, want 0 - a write is not a read", got)
		}
		raw := marshalDetails(t, usage)
		if got := gjson.Get(raw, "cached_tokens").Int(); got != 0 {
			t.Errorf("cached_tokens = %d, want 0 on a write-only turn: %s", got, raw)
		}
		if got := gjson.Get(raw, "cache_write_tokens").Int(); got != 4759 {
			t.Errorf("cache_write_tokens = %d, want 4759: %s", got, raw)
		}
	})

	// Turn 2, the [read]: byte-identical request against a warm prefix. This is what
	// 63.4/63.6 assert, and what a run that drops the paired [write] can never produce.
	t.Run("read turn surfaces cached_tokens", func(t *testing.T) {
		usage := &schemas.ResponsesResponseUsage{}
		billed := &schemas.BifrostLLMUsage{}
		accumulateAnthropicResponsesUsage(usage, billed, &AnthropicUsage{
			InputTokens:          14,
			CacheReadInputTokens: 4759,
		})
		accumulateAnthropicResponsesUsage(usage, billed, &AnthropicUsage{OutputTokens: 5})

		if got := usage.InputTokensDetails.CachedReadTokens; got != 4759 {
			t.Fatalf("CachedReadTokens = %d, want 4759", got)
		}
		if got := usage.InputTokensDetails.CachedWriteTokens; got != 0 {
			t.Fatalf("CachedWriteTokens = %d, want 0 on a pure read turn", got)
		}
		raw := marshalDetails(t, usage)
		if got := gjson.Get(raw, "cached_tokens").Int(); got != 4759 {
			t.Errorf("cached_tokens = %d, want 4759 - this is the field harness 63.4/63.6 assert on: %s", got, raw)
		}

		// Billing folds the cached tokens into the top-level counters exactly once, so a
		// read turn is billed for the whole prefix it read, not just the 14 fresh tokens.
		normalizeCachedUsage(billed)
		if billed.PromptTokensDetails == nil || billed.PromptTokensDetails.CachedReadTokens != 4759 {
			t.Fatalf("billed CachedReadTokens = %+v, want 4759", billed.PromptTokensDetails)
		}
		if billed.PromptTokens != 4773 {
			t.Errorf("billed PromptTokens = %d, want 4773 (14 fresh + 4759 read)", billed.PromptTokens)
		}
	})
}
