package logging

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// MCPObservation attributes endpoint inspections without trusting payload-supplied governance identity.
type MCPObservation struct {
	DeviceID    string
	AppKey      string
	ServerLabel string
	ToolName    string
	Decision    string
}

// mcpObservationKey keeps endpoint attribution separate from gateway request headers.
const mcpObservationKey schemas.BifrostContextKey = "bifrost-edge-mcp-observation"

// SetMCPObservation attaches bounded endpoint attribution for the normal MCP logging hooks.
func SetMCPObservation(ctx *schemas.BifrostContext, observation *MCPObservation) {
	ctx.SetValue(mcpObservationKey, observation)
}

// applyMCPObservation attributes inspected calls, including pre-hook short-circuit fallback entries.
func applyMCPObservation(ctx *schemas.BifrostContext, entry *logstore.MCPToolLog) {
	observation, ok := ctx.Value(mcpObservationKey).(*MCPObservation)
	if !ok || observation == nil {
		return
	}
	// Copy attribution before asynchronous logging retains the entry.
	snapshot := *observation
	observation = &snapshot
	source := "endpoint"
	entry.Source = &source
	entry.DeviceID = &observation.DeviceID
	entry.AppKey = &observation.AppKey
	entry.App = &observation.AppKey
	entry.Decision = &observation.Decision
	entry.ToolName = observation.ToolName
	entry.ServerLabel = observation.ServerLabel
	if entry.MetadataParsed == nil {
		entry.MetadataParsed = map[string]interface{}{}
	}
	entry.MetadataParsed["inspection"] = "enforced"
}
