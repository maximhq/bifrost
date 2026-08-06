package munsit

import "testing"

func TestSpeechCostUSD(t *testing.T) {
	t.Parallel()

	// 15_000 chars × $1/15_000 = $1.00
	if got := speechCostUSD(15_000); got != 1.0 {
		t.Fatalf("speechCostUSD(15000)=%v, want 1.0", got)
	}

	// 1 char = 2 credits; 3M credits = $100 → $0.0000666...
	wantOne := 2.0 * 100.0 / 3_000_000.0
	if got := speechCostUSD(1); got != wantOne {
		t.Fatalf("speechCostUSD(1)=%v, want %v", got, wantOne)
	}

	if got := speechCostUSD(0); got != 0 {
		t.Fatalf("speechCostUSD(0)=%v, want 0", got)
	}
}

func TestEstimateRealtimeSpeechUsageFromRawRequest(t *testing.T) {
	t.Parallel()

	p := &MunsitProvider{}
	if got := p.EstimateRealtimeSpeechUsageFromRawRequest(""); got != nil {
		t.Fatalf("empty raw should return nil usage, got %#v", got)
	}

	raw := `{"type":"conversation.item.create","item":{"type":"message","role":"user","content":[{"type":"input_text","text":"مرحبا"}]}}`
	usage := p.EstimateRealtimeSpeechUsageFromRawRequest(raw)
	if usage == nil {
		t.Fatal("expected usage")
	}
	if usage.PromptTokens != 5 || usage.TotalTokens != 5 {
		t.Fatalf("tokens = %d/%d, want 5/5", usage.PromptTokens, usage.TotalTokens)
	}
	if usage.Cost == nil || usage.Cost.TotalCost != speechCostUSD(5) {
		t.Fatalf("cost = %#v, want TotalCost=%v", usage.Cost, speechCostUSD(5))
	}
}

func TestCountBillableCharsUsesRunes(t *testing.T) {
	t.Parallel()
	if got := countBillableChars("ab"); got != 2 {
		t.Fatalf("ascii chars = %d, want 2", got)
	}
	if got := countBillableChars("أه"); got != 2 {
		t.Fatalf("arabic chars = %d, want 2", got)
	}
}
