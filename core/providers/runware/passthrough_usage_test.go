package runware

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

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

// TestBuildPassthroughURLStripsVersionAtSegmentBoundary pins the /v1 collapse: the base URL
// already ends in /v1, so a leading /v1 segment is dropped, but only as a whole segment. A path
// such as /v1beta/x shares the prefix without being that segment and must be forwarded intact
// rather than turned into the unrooted beta/x and refused.
func TestBuildPassthroughURLStripsVersionAtSegmentBoundary(t *testing.T) {
	provider := &RunwareProvider{networkConfig: schemas.NetworkConfig{BaseURL: "https://api.runware.ai/v1"}}
	cases := map[string]string{
		"":              "https://api.runware.ai/v1",
		"/v1":           "https://api.runware.ai/v1",
		"/v1/":          "https://api.runware.ai/v1/",
		"/v1/tasks":     "https://api.runware.ai/v1/tasks",
		"/tasks":        "https://api.runware.ai/v1/tasks",
		"/v1beta/tasks": "https://api.runware.ai/v1/v1beta/tasks",
		"/v10/tasks":    "https://api.runware.ai/v1/v10/tasks",
	}
	for path, want := range cases {
		got, err := provider.buildPassthroughURL(&schemas.BifrostPassthroughRequest{Path: path})
		if err != nil {
			t.Fatalf("buildPassthroughURL(%q): unexpected error %v", path, err)
		}
		if got != want {
			t.Fatalf("buildPassthroughURL(%q) = %q, want %q", path, got, want)
		}
	}
}
