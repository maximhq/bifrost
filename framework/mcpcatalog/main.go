package mcpcatalog

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
)

type MCPCatalog struct {
	mu          sync.RWMutex
	pricingData MCPPricingData
	logger      schemas.Logger
}

// PricingEntry represents a single MCP server's tool call pricing information
type PricingEntry struct {
	Server           string  `json:"server"`
	ToolName         string  `json:"tool_name"`
	CostPerExecution float64 `json:"cost_per_execution"`
}

type MCPPricingData map[string]PricingEntry // Map of [{server_label}/{tool_name}] -> PricingEntry

type Config struct {
	PricingData MCPPricingData
}

// Init initializes the MCP catalog
func Init(ctx context.Context, config *Config, logger schemas.Logger) (*MCPCatalog, error) {
	logger.Info("initializing MCP catalog...")

	pricingData := MCPPricingData{}

	if config != nil && config.PricingData != nil {
		// Defensively copy the pricing map to prevent external mutations
		pricingData = make(MCPPricingData, len(config.PricingData))
		maps.Copy(pricingData, config.PricingData)
	}

	return &MCPCatalog{
		logger:      logger,
		pricingData: pricingData,
	}, nil
}

// GetAllPricingData returns all the pricing data
func (mc *MCPCatalog) GetAllPricingData() MCPPricingData {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	// Create a defensive copy to prevent callers from mutating shared state
	copy := make(MCPPricingData, len(mc.pricingData))
	maps.Copy(copy, mc.pricingData)
	return copy
}

// GetPricingData returns the pricing data for the given server and tool name
func (mc *MCPCatalog) GetPricingData(server string, toolName string) (PricingEntry, bool) {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	pricing, ok := mc.pricingData[fmt.Sprintf("%s/%s", server, toolName)]
	return pricing, ok
}

// UpdatePricingData updates the pricing data for the given server and tool name
func (mc *MCPCatalog) UpdatePricingData(server string, toolName string, costPerExecution float64) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.pricingData[fmt.Sprintf("%s/%s", server, toolName)] = PricingEntry{
		Server:           server,
		ToolName:         toolName,
		CostPerExecution: costPerExecution,
	}
}

// DeletePricingData deletes the pricing data for the given server and tool name
func (mc *MCPCatalog) DeletePricingData(server string, toolName string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	delete(mc.pricingData, fmt.Sprintf("%s/%s", server, toolName))
}

// Cleanup cleans up the MCP catalog
func (mc *MCPCatalog) Cleanup() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.pricingData = nil
}
