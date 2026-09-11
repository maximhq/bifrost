package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestMCPErrorTypeClassification asserts that mcpErrorType maps failed MCP ops onto the
// low-cardinality error.type values used by the mcp.client.operation.duration metric,
// including the sentinel-wrapped wire errors and the _OTHER catch-all.
func TestMCPErrorTypeClassification(t *testing.T) {
	tests := []struct {
		name string
		err  *schemas.BifrostError
		want string
	}{
		{
			name: "nil error",
			err:  nil,
			want: "_OTHER",
		},
		{
			name: "auth required takes precedence",
			err: &schemas.BifrostError{
				Error:       &schemas.ErrorField{Message: "auth", Error: ErrMCPToolTimeout},
				ExtraFields: schemas.BifrostErrorExtraFields{MCPAuthRequired: &schemas.MCPAuthRequiredError{}},
			},
			want: "auth_required",
		},
		{
			name: "timeout sentinel through fmt wrap",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "MCP tool call timed out after 30s: search",
					Error:   fmt.Errorf("MCP tool call timed out after 30s: search: %w", ErrMCPToolTimeout),
				},
			},
			want: "timeout",
		},
		{
			name: "tool_error sentinel through fmt wrap",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{
					Message: "MCP tool call failed for search: boom",
					Error:   fmt.Errorf("MCP tool call failed for search: boom: %w", ErrMCPToolCallFailed),
				},
			},
			want: "tool_error",
		},
		{
			name: "unclassified error falls back to _OTHER",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{Message: "tool execution returned nil result", Error: fmt.Errorf("nil result")},
			},
			want: "_OTHER",
		},
		{
			name: "error present but no wrapped chain",
			err: &schemas.BifrostError{
				Error: &schemas.ErrorField{Message: "plugin denied"},
			},
			want: "_OTHER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mcpErrorType(tt.err); got != tt.want {
				t.Errorf("mcpErrorType() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestProbeRetryConfig_RetriesTimeoutsButNotPermanentFailures pins the
// classification the periodic connection checker's own ping / list_tools calls
// run under. isTransientError's blanket "if something times out, retrying
// won't help" is written for a dial, where a timeout usually means the
// endpoint is wrong or unreachable. A heartbeat over an already-established
// connection is the opposite case: ConnectionCheckTimeout is 5 seconds, so one
// slow response from a busy upstream would otherwise exhaust none of the
// probe's retry budget and mark the client Unstable on the first attempt.
//
// Everything the shared classifier calls permanent for a good reason (a 422
// dead session, a 401, a 404) must still fail fast: retrying those over the
// same connection cannot succeed, and the reconnect is what repairs them.
func TestProbeRetryConfig_RetriesTimeoutsButNotPermanentFailures(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantAttempts int
	}{
		{
			name:         "probe timeout is retried",
			err:          fmt.Errorf("ping failed: context deadline exceeded"),
			wantAttempts: ProbeRetryConfig.MaxRetries + 1,
		},
		{
			name:         "bare timeout wording is retried",
			err:          fmt.Errorf("list_tools failed: i/o timeout"),
			wantAttempts: ProbeRetryConfig.MaxRetries + 1,
		},
		{
			// Every phrase the shared classifier calls a timeout gets the same
			// treatment here, not just the two most common ones: they are one
			// class ("did not complete in time"), and a probe retries all of
			// them.
			name:         "endpoint wait is retried like any other timeout",
			err:          fmt.Errorf("list_tools failed: waiting for endpoint"),
			wantAttempts: ProbeRetryConfig.MaxRetries + 1,
		},
		{
			name:         "dead session is permanent",
			err:          fmt.Errorf("request failed with status 422: Unexpected message, expect initialize request"),
			wantAttempts: 1,
		},
		{
			name:         "terminated session is permanent",
			err:          fmt.Errorf("session terminated (404). need to re-initialize"),
			wantAttempts: 1,
		},
		{
			name:         "auth rejection is permanent",
			err:          fmt.Errorf("ping failed: 401 unauthorized"),
			wantAttempts: 1,
		},
		{
			name:         "auth rejection wins over timeout wording",
			err:          fmt.Errorf("ping failed: 403 forbidden: timeout"),
			wantAttempts: 1,
		},
		{
			// A dead protocol exchange that happens to mention a timeout is
			// still dead: no upstream observed so far words it this way, but
			// nothing stops one, and retrying cannot make a 422 succeed.
			name:         "status 422 wins over timeout wording",
			err:          fmt.Errorf("request failed with status 422: timeout"),
			wantAttempts: 1,
		},
		{
			// Same rule generalized past HTTP: every permanent signal outranks
			// a co-occurring timeout, not just the auth and status ones.
			name:         "command failure wins over timeout wording",
			err:          fmt.Errorf("command failed: timeout starting server"),
			wantAttempts: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			err := ExecuteWithRetry(context.Background(), func() error {
				attempts++
				return tc.err
			}, ProbeRetryConfig, &MockLogger{})
			require.Error(t, err)
			require.Equal(t, tc.wantAttempts, attempts)
		})
	}
}
