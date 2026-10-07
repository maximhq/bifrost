package schemas

import "testing"

func TestMergeBifrostLLMUsage(t *testing.T) {
	base := &BifrostLLMUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		PromptTokensDetails: &ChatPromptTokensDetails{
			TextTokens:        3,
			AudioTokens:       2,
			ImageTokens:       1,
			CachedReadTokens:  4,
			CachedWriteTokens: 6,
			CachedWriteTokenDetails: &ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 2,
				CachedWriteTokens1h: 4,
			},
		},
		CompletionTokensDetails: &ChatCompletionTokensDetails{
			TextTokens:               1,
			AcceptedPredictionTokens: 2,
			AudioTokens:              3,
			CitationTokens:           new(4),
			NumSearchQueries:         new(5),
			ReasoningTokens:          6,
			ImageTokens:              new(7),
			RejectedPredictionTokens: 8,
		},
		ToolUsage: &ToolUsage{WebSearch: &WebSearchToolUsage{NumRequests: 2}},
		Cost: &BifrostCost{
			InputCost:             1,
			InputCostDetails:      &InputCostDetails{TextCost: 1},
			OutputCost:            2,
			OutputCostDetails:     &OutputCostDetails{TextCost: 2},
			AdditionalCost:        3,
			AdditionalCostDetails: &AdditionalCostDetails{GuardrailCost: 3},
			TotalCost:             6,
		},
	}
	add := &BifrostLLMUsage{
		PromptTokens:     20,
		CompletionTokens: 7,
		TotalTokens:      27,
		PromptTokensDetails: &ChatPromptTokensDetails{
			TextTokens:        5,
			AudioTokens:       4,
			ImageTokens:       3,
			CachedReadTokens:  2,
			CachedWriteTokens: 1,
			CachedWriteTokenDetails: &ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 8,
				CachedWriteTokens1h: 9,
			},
		},
		CompletionTokensDetails: &ChatCompletionTokensDetails{
			TextTokens:               8,
			AcceptedPredictionTokens: 9,
			AudioTokens:              10,
			CitationTokens:           new(11),
			NumSearchQueries:         new(12),
			ReasoningTokens:          13,
			ImageTokens:              new(14),
			RejectedPredictionTokens: 15,
		},
		ToolUsage: &ToolUsage{WebSearch: &WebSearchToolUsage{NumRequests: 3}},
		Cost: &BifrostCost{
			InputCost:             10,
			InputCostDetails:      &InputCostDetails{TextCost: 10},
			OutputCost:            20,
			OutputCostDetails:     &OutputCostDetails{TextCost: 20},
			AdditionalCost:        30,
			AdditionalCostDetails: &AdditionalCostDetails{GuardrailCost: 30},
			TotalCost:             60,
		},
	}

	merged := MergeBifrostLLMUsage(base, add)
	if merged.PromptTokens != 30 || merged.CompletionTokens != 12 || merged.TotalTokens != 42 {
		t.Fatalf("unexpected token totals: %+v", merged)
	}
	if got := merged.ToolUsage.WebSearch.NumRequests; got != 5 {
		t.Fatalf("tool_usage web search = %d, want 5", got)
	}
	if merged.ToolUsage == base.ToolUsage || merged.ToolUsage == add.ToolUsage {
		t.Fatal("merged tool_usage aliases an input")
	}
	if merged.PromptTokensDetails.TextTokens != 8 ||
		merged.PromptTokensDetails.AudioTokens != 6 ||
		merged.PromptTokensDetails.ImageTokens != 4 ||
		merged.PromptTokensDetails.CachedReadTokens != 6 ||
		merged.PromptTokensDetails.CachedWriteTokens != 7 {
		t.Fatalf("unexpected prompt details: %+v", merged.PromptTokensDetails)
	}
	if merged.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m != 10 ||
		merged.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens1h != 13 {
		t.Fatalf("unexpected cache write details: %+v", merged.PromptTokensDetails.CachedWriteTokenDetails)
	}
	if merged.CompletionTokensDetails.TextTokens != 9 ||
		merged.CompletionTokensDetails.AcceptedPredictionTokens != 11 ||
		merged.CompletionTokensDetails.AudioTokens != 13 ||
		*merged.CompletionTokensDetails.CitationTokens != 15 ||
		*merged.CompletionTokensDetails.NumSearchQueries != 17 ||
		merged.CompletionTokensDetails.ReasoningTokens != 19 ||
		*merged.CompletionTokensDetails.ImageTokens != 21 ||
		merged.CompletionTokensDetails.RejectedPredictionTokens != 23 {
		t.Fatalf("unexpected completion details: %+v", merged.CompletionTokensDetails)
	}
	if merged.Cost.InputCost != 11 ||
		merged.Cost.OutputCost != 22 ||
		merged.Cost.AdditionalCost != 33 ||
		merged.Cost.TotalCost != 66 ||
		merged.Cost.InputCostDetails.TextCost != 11 ||
		merged.Cost.OutputCostDetails.TextCost != 22 ||
		merged.Cost.AdditionalCostDetails.GuardrailCost != 33 {
		t.Fatalf("unexpected cost: %+v", merged.Cost)
	}
}

func TestMergeBifrostLLMUsageNilInputs(t *testing.T) {
	usage := &BifrostLLMUsage{PromptTokens: 3, TotalTokens: 3}

	if got := MergeBifrostLLMUsage(nil, nil); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
	if got := MergeBifrostLLMUsage(usage, nil); got != usage {
		t.Fatalf("expected base usage returned for nil add")
	}
	if got := MergeBifrostLLMUsage(nil, usage); got != usage {
		t.Fatalf("expected added usage returned for nil base")
	}
}

func TestToolUsageRoundTripsResponsesAndChatUsage(t *testing.T) {
	ru := &ResponsesResponseUsage{TotalTokens: 1, ToolUsage: &ToolUsage{WebSearch: &WebSearchToolUsage{NumRequests: 4}}}
	cu := ru.ToBifrostLLMUsage()
	if got := cu.ToolUsage.WebSearch.NumRequests; got != 4 {
		t.Fatalf("chat tool_usage = %d, want 4", got)
	}
	if got := cu.ToResponsesResponseUsage().ToolUsage.WebSearch.NumRequests; got != 4 {
		t.Fatalf("responses tool_usage = %d, want 4", got)
	}
	if cp := cu.DeepCopy(); cp.ToolUsage == cu.ToolUsage || cp.ToolUsage.WebSearch.NumRequests != 4 {
		t.Fatal("DeepCopy must own tool_usage")
	}
	var parsed ResponsesResponseUsage
	if err := Unmarshal([]byte(`{"total_tokens":1,"tool_usage":{"web_search":{"num_requests":1}}}`), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ToolUsage.WebSearch.NumRequests != 1 {
		t.Fatal("tool_usage.web_search.num_requests not decoded")
	}
}

func TestToolUsageAddHandlesNilOperands(t *testing.T) {
	var none *ToolUsage
	if none.Add(nil) != nil {
		t.Fatal("nil + nil must stay nil")
	}
	one := &ToolUsage{WebSearch: &WebSearchToolUsage{NumRequests: 2}}
	for _, sum := range []*ToolUsage{none.Add(one), one.Add(nil)} {
		if sum == one || sum.WebSearch == one.WebSearch || sum.WebSearch.NumRequests != 2 {
			t.Fatalf("sum with nil must be an owned copy of the other side, got %+v", sum)
		}
	}
	if got := (&ToolUsage{}).Add(one).WebSearch.NumRequests; got != 2 {
		t.Fatalf("empty + one = %d, want 2", got)
	}
}
