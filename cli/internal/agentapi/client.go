// Package agentapi provides typed access to user-scoped Enterprise agent APIs.
package agentapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/cli/internal/client"
)

// Client calls Enterprise agent endpoints through the shared authenticated transport.
type Client struct {
	transport *client.Client
}

// New creates a typed agent API client that inherits token refresh behavior.
func New(transport *client.Client) *Client {
	return &Client{transport: transport}
}

// ListVirtualKeys returns the active virtual keys assigned to the signed-in user.
func (c *Client) ListVirtualKeys(ctx context.Context) (VirtualKeyListResponse, error) {
	var result VirtualKeyListResponse
	if c == nil || c.transport == nil {
		return result, fmt.Errorf("agent API client is unavailable")
	}
	response, err := c.transport.Do(ctx, client.Request{Path: "/api/agent/virtual-keys", Auth: client.AuthAgent})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(response.Body, &result); err != nil {
		return result, fmt.Errorf("decode assigned virtual keys: %w", err)
	}
	return result, nil
}

// GetUsageSummary returns the signed-in user's budgets, rate limits, and rankings.
func (c *Client) GetUsageSummary(ctx context.Context) (UsageSummaryResponse, error) {
	var result UsageSummaryResponse
	if c == nil || c.transport == nil {
		return result, fmt.Errorf("agent API client is unavailable")
	}
	response, err := c.transport.Do(ctx, client.Request{Path: "/api/agent/usage-summary", Auth: client.AuthAgent})
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(response.Body, &result); err != nil {
		return result, fmt.Errorf("decode usage summary: %w", err)
	}
	return result, nil
}
