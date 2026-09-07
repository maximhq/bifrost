package logging

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// TestMCPObservationSnapshot ensures async pending logs cannot alias mutable execution attribution.
func TestMCPObservationSnapshot(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()
	observation := &MCPObservation{DeviceID: "device", AppKey: "cursor", ServerLabel: "local", ToolName: "read", Decision: "deny"}
	SetMCPObservation(ctx, observation)
	var pending, final logstore.MCPToolLog
	applyMCPObservation(ctx, &pending)
	observation.Decision = "allow"
	applyMCPObservation(ctx, &final)
	if *pending.Decision != "deny" || *final.Decision != "allow" {
		t.Fatalf("pending=%s final=%s", *pending.Decision, *final.Decision)
	}
	if *final.App != "cursor" || *final.Source != "endpoint" || final.ToolName != "read" {
		t.Fatalf("incorrect attribution: %+v", final)
	}
}
