package handlers

import "testing"

// TestValidateGlobalToolSyncIntervalMinutes pins the client-config
// mcp_tool_sync_interval bounds: 0 (built-in default) and ordinary positive
// minute counts up to the minutes->Duration overflow edge are accepted;
// negatives and anything past the edge are rejected.
func TestValidateGlobalToolSyncIntervalMinutes(t *testing.T) {
	for _, minutes := range []int{0, 1, 10, 1440, int(maxToolSyncIntervalMinutes)} {
		if err := validateGlobalToolSyncIntervalMinutes(minutes); err != nil {
			t.Errorf("mcp_tool_sync_interval=%d minutes should be accepted, got %v", minutes, err)
		}
	}
	for _, minutes := range []int{-1, -10, int(maxToolSyncIntervalMinutes) + 1} {
		if err := validateGlobalToolSyncIntervalMinutes(minutes); err == nil {
			t.Errorf("mcp_tool_sync_interval=%d minutes should be rejected", minutes)
		}
	}
}
