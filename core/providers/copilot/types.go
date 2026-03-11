// Package copilot implements the GitHub Copilot provider and its utility functions.
package copilot

// CopilotEndpoints contains the API endpoints returned by the Copilot token exchange.
type CopilotEndpoints struct {
	API string `json:"api"`
}

// CopilotTokenResponse represents the response from the Copilot token exchange endpoint.
type CopilotTokenResponse struct {
	Token     string           `json:"token"`
	ExpiresAt int64            `json:"expires_at"`
	Endpoints CopilotEndpoints `json:"endpoints"`
}
