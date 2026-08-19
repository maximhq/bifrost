package async

import (
	"net/http"
	"testing"
	"time"
)

// TTL tests use chat_completions as a representative endpoint and run in both
// global and VK modes. They verify that expires_at is set correctly relative to
// completed_at based on the TTL value in effect.

// TestTTL_DefaultApplied verifies that when no TTL header is sent, expires_at is
// approximately 3600s (one hour) after completed_at.
func TestTTL_DefaultApplied(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			ec := chatCompletionCase()
			_, submitted, body := submitCase(t, ec, mode.headers)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}
			pollPath := jobPollPath(ec.pollBase, submitted.ID)
			_, job := pollUntilTerminal(t, pollPath, mode.headers)
			assertTTL(t, job, 3600, 60)
		})
	}
}

// TestTTL_CustomHeaderApplied verifies that x-bf-async-job-result-ttl overrides the
// default and expires_at is roughly TTL seconds after completed_at.
func TestTTL_CustomHeaderApplied(t *testing.T) {
	const customTTL = 120
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			ec := chatCompletionCase()
			headers := withTTLHeader(mode.headers, customTTL)
			_, submitted, body := submitCase(t, ec, headers)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}
			pollPath := jobPollPath(ec.pollBase, submitted.ID)
			// Poll must use the mode headers, not the TTL headers (TTL is submit-only).
			_, job := pollUntilTerminal(t, pollPath, mode.headers)
			assertTTL(t, job, customTTL, 30)
		})
	}
}

// TestTTL_InvalidHeader_FallsBackToDefault verifies that a non-numeric TTL header
// is ignored and the server falls back to the default 3600s TTL.
func TestTTL_InvalidHeader_FallsBackToDefault(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			ec := chatCompletionCase()
			headers := withRawHeader(mode.headers, "x-bf-async-job-result-ttl", "not-a-number")
			_, submitted, body := submitCase(t, ec, headers)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}
			pollPath := jobPollPath(ec.pollBase, submitted.ID)
			_, job := pollUntilTerminal(t, pollPath, mode.headers)
			assertTTL(t, job, 3600, 60)
		})
	}
}

// TestTTL_ZeroHeader_FallsBackToDefault verifies that TTL=0 is treated as invalid
// (per SubmitJob: if resultTTL <= 0 use default) and falls back to 3600s.
func TestTTL_ZeroHeader_FallsBackToDefault(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			ec := chatCompletionCase()
			headers := withTTLHeader(mode.headers, 0)
			_, submitted, body := submitCase(t, ec, headers)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}
			pollPath := jobPollPath(ec.pollBase, submitted.ID)
			_, job := pollUntilTerminal(t, pollPath, mode.headers)
			assertTTL(t, job, 3600, 60)
		})
	}
}

// TestTTL_NegativeHeader_FallsBackToDefault verifies that a negative TTL value
// falls back to the default 3600s.
func TestTTL_NegativeHeader_FallsBackToDefault(t *testing.T) {
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			ec := chatCompletionCase()
			headers := withTTLHeader(mode.headers, -1)
			_, submitted, body := submitCase(t, ec, headers)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}
			pollPath := jobPollPath(ec.pollBase, submitted.ID)
			_, job := pollUntilTerminal(t, pollPath, mode.headers)
			assertTTL(t, job, 3600, 60)
		})
	}
}

// TestTTL_ExpiredJob_Returns404 submits a job with a very short TTL, waits for
// completion, then waits for the TTL to elapse and confirms polling returns 404.
// Verifies FindAsyncJobByID filters on expires_at > NOW().
func TestTTL_ExpiredJob_Returns404(t *testing.T) {
	const shortTTL = 10 // seconds — must be larger than BIFROST_POLL_INTERVAL
	for _, mode := range testModes() {
		t.Run(mode.name, func(t *testing.T) {
			ec := chatCompletionCase()
			headers := withTTLHeader(mode.headers, shortTTL)
			_, submitted, body := submitCase(t, ec, headers)
			if submitted.ID == "" {
				t.Fatalf("submit returned no job id: %s", body)
			}

			pollPath := jobPollPath(ec.pollBase, submitted.ID)
			pollUntilTerminal(t, pollPath, mode.headers)

			// Poll until 404 (TTL expired) with a generous deadline to avoid flakiness.
			deadline := time.Now().Add(time.Duration(shortTTL+10) * time.Second)
			for {
				code, _, _ := pollOnce(t, pollPath, mode.headers)
				if code == http.StatusNotFound {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("expected 404 after TTL expiry, last code=%d", code)
				}
				time.Sleep(250 * time.Millisecond)
			}
		})
	}
}

// assertTTL checks that expires_at ≈ completed_at + wantTTLSeconds within toleranceSeconds.
func assertTTL(t *testing.T, job AsyncJobResponse, wantTTLSeconds, toleranceSeconds int) {
	t.Helper()
	if job.CompletedAt == nil {
		t.Fatal("completed_at is nil, cannot verify TTL")
	}
	if job.ExpiresAt == nil {
		t.Fatal("expires_at is nil, cannot verify TTL")
	}
	actual := job.ExpiresAt.Sub(*job.CompletedAt)
	want := time.Duration(wantTTLSeconds) * time.Second
	tolerance := time.Duration(toleranceSeconds) * time.Second
	diff := actual - want
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		t.Errorf("TTL mismatch: expires_at - completed_at = %v, want %v ± %v",
			actual, want, tolerance)
	}
}
