package runware

import (
	"testing"
)

// Runware reports per-task cost (when includeCost is set); it is summed across results and surfaced
// as the provider-reported image cost so pricing uses it verbatim.
func TestToBifrostImageGenerationResponse_Cost(t *testing.T) {
	resp := &RunwareResponse{
		Data: []RunwareResult{
			{TaskUUID: "img-1", ImageURL: "https://x/1.jpg", Cost: 0.0006},
			{TaskUUID: "img-1", ImageURL: "https://x/2.jpg", Cost: 0.0004},
		},
	}

	out, err := ToBifrostImageGenerationResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Usage == nil || out.Usage.Cost == nil {
		t.Fatalf("expected provider-reported cost, got Usage=%+v", out.Usage)
	}
	if out.Usage.Cost.TotalCost != 0.001 {
		t.Fatalf("total cost = %v, want 0.001", out.Usage.Cost.TotalCost)
	}
}

// With no cost reported, Usage stays nil so datasheet pricing applies.
func TestToBifrostImageGenerationResponse_NoCost(t *testing.T) {
	resp := &RunwareResponse{Data: []RunwareResult{{TaskUUID: "img-2", ImageURL: "https://x/1.jpg"}}}
	out, err := ToBifrostImageGenerationResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Usage != nil {
		t.Fatalf("expected nil Usage when cost absent, got %+v", out.Usage)
	}
}
