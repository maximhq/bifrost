package async

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestLifecycle_AllEndpoints_ReachesTerminalState submits a job for every supported
// endpoint and polls until it reaches completed or failed, then validates the
// terminal response shape. Passes for either outcome — the test asserts the async
// mechanism itself, not model availability.
// Runs in both global and VK modes.
func TestLifecycle_AllEndpoints_ReachesTerminalState(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, ec := range endpointCases() {
				t.Run(ec.name, func(t *testing.T) {
					_, submitted, body := submitCase(t, ec, mode.headers)
					if submitted.ID == "" {
						t.Fatalf("submit returned no job id: %s", body)
					}

					pollPath := jobPollPath(ec.pollBase, submitted.ID)
					code, job := pollUntilTerminal(t, pollPath, mode.headers)

					if code != http.StatusOK {
						t.Errorf("expected 200 for terminal job, got %d", code)
					}
					if job.ID != submitted.ID {
						t.Errorf("polled id %q does not match submitted id %q", job.ID, submitted.ID)
					}
					if job.CompletedAt == nil {
						t.Error("completed_at must be set on a terminal job")
					}
					if job.ExpiresAt == nil {
						t.Error("expires_at must be set on a terminal job")
					}
					if job.CompletedAt != nil && job.ExpiresAt != nil && !job.ExpiresAt.After(*job.CompletedAt) {
						t.Error("expires_at must be after completed_at")
					}

					switch job.Status {
					case "completed":
						if len(job.Result) == 0 || string(job.Result) == "null" {
							t.Error("completed job must have a non-null result")
						}
					case "failed":
						if len(job.Error) == 0 || string(job.Error) == "null" {
							t.Error("failed job must have a non-null error")
						}
						if job.StatusCode == 0 {
							t.Error("failed job must carry a non-zero status_code")
						}
					}
				})
			}
		})
	}
}

// TestLifecycle_Poll_NonExistentJob_Returns404 confirms that polling a random job ID
// returns 404 regardless of VK mode (job lookup fails before VK check).
// Uses chat_completions as a representative endpoint — all endpoints share the same
// RetrieveJob() path, so repeating across all 11 adds no coverage.
func TestLifecycle_Poll_NonExistentJob_Returns404(t *testing.T) {
	const fakeID = "00000000-0000-0000-0000-000000000000"
	ec := chatCompletionCase()
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			pollPath := jobPollPath(ec.pollBase, fakeID)
			code, _, _ := pollOnce(t, pollPath, mode.headers)
			if code != http.StatusNotFound {
				t.Errorf("expected 404 for non-existent job, got %d", code)
			}
		})
	}
}

// TestLifecycle_CompletedJobResultShape checks that completed jobs carry the expected
// top-level fields in their result JSON.  If a job fails (e.g., no live API key), the
// shape check is skipped for that case — the test asserts structure, not model availability.
func TestLifecycle_CompletedJobResultShape(t *testing.T) {
	type shapeCheck struct {
		name  string
		check func(t *testing.T, result json.RawMessage)
	}

	shapeChecks := map[string]shapeCheck{
		"chat_completions": {
			"choices[]",
			func(t *testing.T, result json.RawMessage) {
				var r struct {
					Choices []json.RawMessage `json:"choices"`
				}
				if err := json.Unmarshal(result, &r); err != nil {
					t.Fatalf("unmarshal choices: %v", err)
				}
				if len(r.Choices) == 0 {
					t.Error("completed chat job must have at least one choice")
				}
			},
		},
		"embeddings": {
			"data[]",
			func(t *testing.T, result json.RawMessage) {
				var r struct {
					Data []json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(result, &r); err != nil {
					t.Fatalf("unmarshal data: %v", err)
				}
				if len(r.Data) == 0 {
					t.Error("completed embeddings job must have at least one data entry")
				}
			},
		},
		"rerank": {
			"results[]",
			func(t *testing.T, result json.RawMessage) {
				var r struct {
					Results []json.RawMessage `json:"results"`
				}
				if err := json.Unmarshal(result, &r); err != nil {
					t.Fatalf("unmarshal results: %v", err)
				}
				if len(r.Results) == 0 {
					t.Error("completed rerank job must have at least one result")
				}
			},
		},
	}

	for _, ec := range endpointCases() {
		sc, ok := shapeChecks[ec.name]
		if !ok {
			continue
		}
		t.Run(ec.name+"/"+sc.name, func(t *testing.T) {
			_, submitted, body := submitCase(t, ec, nil)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}
			pollPath := jobPollPath(ec.pollBase, submitted.ID)
			_, job := pollUntilTerminal(t, pollPath, nil)
			if job.Status != "completed" {
				t.Skipf("job status=%q (not completed) — shape check skipped", job.Status)
			}
			sc.check(t, job.Result)
		})
	}
}

// TestLifecycle_Poll_WrongEndpointType_Returns404 submits a job on one endpoint and
// polls it via a different endpoint's path, expecting 404 (type mismatch).
func TestLifecycle_Poll_WrongEndpointType_Returns404(t *testing.T) {
	cases := endpointCases()
	if len(cases) < 2 {
		t.Skip("need at least two endpoint cases")
	}

	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			// Submit on cases[0], poll via cases[1]'s poll base.
			submitter := cases[0]
			wrongBase := cases[1].pollBase

			_, submitted, body := submitCase(t, submitter, mode.headers)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}

			pollPath := jobPollPath(wrongBase, submitted.ID)
			code, _, _ := pollOnce(t, pollPath, mode.headers)
			if code != http.StatusNotFound {
				t.Errorf("expected 404 when polling with wrong endpoint type, got %d", code)
			}
		})
	}
}
