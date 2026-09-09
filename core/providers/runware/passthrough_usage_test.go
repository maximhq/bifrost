package runware

import "testing"

func TestExtractRunwarePassthroughUsage(t *testing.T) {
	t.Run("single task cost", func(t *testing.T) {
		body := []byte(`{"data":[{"taskType":"3dInference","taskUUID":"x","cost":0.5}]}`)
		u := ExtractRunwarePassthroughUsage(body)
		if u == nil || u.Cost == nil {
			t.Fatalf("expected cost, got %+v", u)
		}
		if u.Cost.TotalCost != 0.5 {
			t.Fatalf("cost = %v, want 0.5", u.Cost.TotalCost)
		}
	})

	t.Run("sums across tasks", func(t *testing.T) {
		body := []byte(`{"data":[{"cost":0.001},{"cost":0.0019}]}`)
		u := ExtractRunwarePassthroughUsage(body)
		if u == nil || u.Cost == nil {
			t.Fatalf("expected cost, got %+v", u)
		}
		if u.Cost.TotalCost != 0.0029 {
			t.Fatalf("cost = %v, want 0.0029", u.Cost.TotalCost)
		}
	})

	t.Run("no cost field returns nil", func(t *testing.T) {
		body := []byte(`{"data":[{"taskType":"upscale","taskUUID":"x"}]}`)
		if u := ExtractRunwarePassthroughUsage(body); u != nil {
			t.Fatalf("expected nil, got %+v", u)
		}
	})

	t.Run("error/queued response returns nil", func(t *testing.T) {
		body := []byte(`{"data":[{"taskType":"upscale","taskUUID":"x"}],"errors":[]}`)
		if u := ExtractRunwarePassthroughUsage(body); u != nil {
			t.Fatalf("expected nil, got %+v", u)
		}
		queued := []byte(`{"data":[{"taskType":"3dInference","taskUUID":"x"}]}`)
		if u := ExtractRunwarePassthroughUsage(queued); u != nil {
			t.Fatalf("expected nil for queued (no cost), got %+v", u)
		}
	})
}
