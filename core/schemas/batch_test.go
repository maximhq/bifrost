package schemas

import "testing"

func TestBatchRequestCountsIsZero(t *testing.T) {
	if !(BatchRequestCounts{}).IsZero() {
		t.Fatalf("expected empty counts to be zero")
	}
	if (BatchRequestCounts{Total: 1}).IsZero() {
		t.Fatalf("expected total count to be non-zero")
	}
	if (BatchRequestCounts{Pending: 1}).IsZero() {
		t.Fatalf("expected provider-specific pending count to be non-zero")
	}
}

func TestBatchResultItemFailed(t *testing.T) {
	tests := []struct {
		name string
		item BatchResultItem
		want bool
	}{
		{
			name: "explicit error",
			item: BatchResultItem{Error: &BatchResultError{Message: "failed"}},
			want: true,
		},
		{
			name: "http status error",
			item: BatchResultItem{Response: &BatchResultResponse{StatusCode: 500}},
			want: true,
		},
		{
			name: "anthropic succeeded",
			item: BatchResultItem{Result: &BatchResultData{Type: "succeeded"}},
			want: false,
		},
		{
			name: "anthropic errored",
			item: BatchResultItem{Result: &BatchResultData{Type: "errored"}},
			want: true,
		},
		{
			name: "openai success",
			item: BatchResultItem{Response: &BatchResultResponse{StatusCode: 200}},
			want: false,
		},
		{
			// Gemini's inline path and Bedrock emit a bare item (no response, no
			// error) when a record produced no output. That is not a success.
			name: "no response, result, or error",
			item: BatchResultItem{CustomID: "req-1"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.item.Failed(); got != tt.want {
				t.Fatalf("Failed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBatchRequestCountsFromResults(t *testing.T) {
	counts := BatchRequestCountsFromResults([]BatchResultItem{
		{Response: &BatchResultResponse{StatusCode: 200}},
		{Response: &BatchResultResponse{StatusCode: 429}},
		{Result: &BatchResultData{Type: "succeeded"}},
		{Result: &BatchResultData{Type: "expired"}},
	})

	if counts.Total != 4 || counts.Completed != 2 || counts.Failed != 2 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
}

func TestBatchUsagePreservesNativeCharge(t *testing.T) {
	for _, cost := range []string{"0", "0.123"} {
		raw := []byte(`{"id":"b","model":"m","usage":{"prompt_tokens":2,"total_tokens":2,"cost":` + cost + `,"is_byok":false,"cost_details":{"upstream_inference_cost":0.5}},"results":[{"id":"r","custom_id":"a","error":{"type":"bad_input","param":"input","error_type":"invalid_request","message":"bad"}}]}`)
		var response BifrostBatchRetrieveResponse
		if e := Unmarshal(raw, &response); e != nil {
			t.Fatal(e)
		}
		if response.Usage == nil || response.Usage.Cost == nil || response.Usage.IsBYOK == nil || *response.Usage.IsBYOK || response.Usage.CostDetails["upstream_inference_cost"] != 0.5 {
			t.Fatalf("usage %+v", response.Usage)
		}
		if len(response.Results) != 1 || response.Results[0].Error.Param != "input" || response.Results[0].ID != "r" {
			t.Fatalf("results %+v", response.Results)
		}
	}
}
