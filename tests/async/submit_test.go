package async

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSubmit_AllEndpoints_Returns202 verifies that every async endpoint immediately
// returns 202 Accepted with a well-formed job envelope.
// Runs once in global mode (no VK) and once with BIFROST_VK when set.
func TestSubmit_AllEndpoints_Returns202(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, ec := range endpointCases() {
				t.Run(ec.name, func(t *testing.T) {
					code, job, body := submitCase(t, ec, mode.headers)

					if code != http.StatusAccepted {
						t.Fatalf("expected 202, got %d: %s", code, body)
					}
					if job.ID == "" {
						t.Fatal("response missing id")
					}
					// UUID format: 8-4-4-4-12 hex groups separated by hyphens.
					parts := strings.Split(job.ID, "-")
					if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 ||
						len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
						t.Errorf("id %q does not look like a UUID", job.ID)
					}
					if job.Status != "pending" {
						t.Errorf("expected status=pending, got %q", job.Status)
					}
					if job.CreatedAt.IsZero() {
						t.Error("created_at is zero")
					}
					if time.Since(job.CreatedAt) > 30*time.Second {
						t.Errorf("created_at %v appears stale (>30s ago)", job.CreatedAt)
					}
					if job.CompletedAt != nil {
						t.Error("completed_at must be absent on a freshly submitted job")
					}
					if job.ExpiresAt != nil {
						t.Error("expires_at must be absent on a freshly submitted job")
					}
				})
			}
		})
	}
}

// TestSubmit_AllEndpoints_PollPathReturnsPending verifies that polling immediately
// after submission yields a non-terminal (pending/processing) or just-completed state
// with the correct HTTP status code for each.
// Runs in both global and VK modes.
func TestSubmit_AllEndpoints_PollPathReturnsPending(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			for _, ec := range endpointCases() {
				t.Run(ec.name, func(t *testing.T) {
					submitCode, submitted, body := submitCase(t, ec, mode.headers)
					if submitCode != http.StatusAccepted {
						t.Fatalf("expected submit 202, got %d: %s", submitCode, body)
					}
					if submitted.ID == "" {
						t.Fatalf("submit returned no job id: %s", body)
					}

					pollPath := jobPollPath(ec.pollBase, submitted.ID)
					code, polled, _ := pollOnce(t, pollPath, mode.headers)

					switch polled.Status {
					case "pending", "processing":
						if code != http.StatusAccepted {
							t.Errorf("expected 202 for status %q, got %d", polled.Status, code)
						}
					case "completed", "failed":
						if code != http.StatusOK {
							t.Errorf("expected 200 for terminal status %q, got %d", polled.Status, code)
						}
					default:
						t.Errorf("unexpected status %q (HTTP %d)", polled.Status, code)
					}
				})
			}
		})
	}
}
