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
			CitationTokens:           intPtr(4),
			NumSearchQueries:         intPtr(5),
			ReasoningTokens:          6,
			ImageTokens:              intPtr(7),
			RejectedPredictionTokens: 8,
		},
		Cost: &BifrostCost{
			InputTokensCost:     1,
			OutputTokensCost:    2,
			ReasoningTokensCost: 3,
			CitationTokensCost:  4,
			SearchQueriesCost:   5,
			RequestCost:         6,
			TotalCost:           21,
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
			AcceptedPredictionTokens: 7,
			AudioTokens:              6,
			CitationTokens:           intPtr(5),
			NumSearchQueries:         intPtr(4),
			ReasoningTokens:          3,
			ImageTokens:              intPtr(2),
			RejectedPredictionTokens: 1,
		},
		Cost: &BifrostCost{
			InputTokensCost:     10,
			OutputTokensCost:    20,
			ReasoningTokensCost: 30,
			CitationTokensCost:  40,
			SearchQueriesCost:   50,
			RequestCost:         60,
			TotalCost:           210,
		},
	}

	merged := MergeBifrostLLMUsage(base, add)
	if merged.PromptTokens != 30 || merged.CompletionTokens != 12 || merged.TotalTokens != 42 {
		t.Fatalf("unexpected token totals: %+v", merged)
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
		merged.CompletionTokensDetails.AcceptedPredictionTokens != 9 ||
		merged.CompletionTokensDetails.AudioTokens != 9 ||
		*merged.CompletionTokensDetails.CitationTokens != 9 ||
		*merged.CompletionTokensDetails.NumSearchQueries != 9 ||
		merged.CompletionTokensDetails.ReasoningTokens != 9 ||
		*merged.CompletionTokensDetails.ImageTokens != 9 ||
		merged.CompletionTokensDetails.RejectedPredictionTokens != 9 {
		t.Fatalf("unexpected completion details: %+v", merged.CompletionTokensDetails)
	}
	if merged.Cost.InputTokensCost != 11 ||
		merged.Cost.OutputTokensCost != 22 ||
		merged.Cost.ReasoningTokensCost != 33 ||
		merged.Cost.CitationTokensCost != 44 ||
		merged.Cost.SearchQueriesCost != 55 ||
		merged.Cost.RequestCost != 66 ||
		merged.Cost.TotalCost != 231 {
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

func intPtr(value int) *int {
	return &value
}
