package mcpcatalog

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
)

// MCPCatalog answers what an MCP tool call costs. It keeps nothing of its own: each MCP client
// record in the live store carries its tools' prices, so a client written, renamed or removed is
// priced as stored the moment the store holds it.
type MCPCatalog struct {
	clients *livestore.Collection[registry.MCPClient]
}

// PricingEntry represents a single MCP server's tool call pricing information
type PricingEntry struct {
	Server           string  `json:"server"`
	ToolName         string  `json:"tool_name"`
	CostPerExecution float64 `json:"cost_per_execution"`
}

// Init initializes the MCP catalog over the MCP client records in live.
func Init(ctx context.Context, live *livestore.Store, logger schemas.Logger) (*MCPCatalog, error) {
	if live == nil {
		return nil, fmt.Errorf("MCP catalog: %w", livestore.ErrNoLiveStore)
	}
	logger.Info("initializing MCP catalog...")
	return &MCPCatalog{clients: live.Collections.MCPClients}, nil
}

// GetPricingData returns the pricing for toolName on the MCP client named server.
func (mc *MCPCatalog) GetPricingData(server string, toolName string) (PricingEntry, bool) {
	client, ok := mc.clients.GetBy(registry.MCPClientByName, server)
	if !ok {
		return PricingEntry{}, false
	}
	cost, ok := client.Config.ToolPricing[toolName]
	if !ok {
		return PricingEntry{}, false
	}
	return PricingEntry{Server: server, ToolName: toolName, CostPerExecution: cost}, true
}
