package handlers

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func perUserOAuthConfig() *schemas.MCPClientConfig {
	return &schemas.MCPClientConfig{
		ID:               "client-1",
		Name:             "notion",
		AuthType:         schemas.MCPAuthTypePerUserOauth,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: &schemas.SecretVar{Val: "https://mcp.notion.com/mcp"},
	}
}

// TestValidateAuthorizeMCPClientRequest covers the guards in
// validateAuthorizeMCPClientRequest. The two "requires"/"accepts"
// connection_string cases are the load-bearing ones: without an explicit
// client_id, OAuth discovery needs a server URL to probe, which comes from
// the existing client's connection_string, not the request body — and a
// config.json-seeded client with a connection_string but no client_id is the
// concrete case this endpoint exists for.
func TestValidateAuthorizeMCPClientRequest(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*schemas.MCPClientConfig)
		req     AuthorizeMCPClientRequest
		wantErr bool
	}{
		{
			name:    "rejects non-per_user_oauth client",
			mutate:  func(c *schemas.MCPClientConfig) { c.AuthType = schemas.MCPAuthTypeOauth },
			req:     AuthorizeMCPClientRequest{OauthConfig: &OAuthConfigRequest{}},
			wantErr: true,
		},
		{
			name:    "rejects disabled client",
			mutate:  func(c *schemas.MCPClientConfig) { c.Disabled = true },
			req:     AuthorizeMCPClientRequest{OauthConfig: &OAuthConfigRequest{}},
			wantErr: true,
		},
		{
			name:    "requires oauth_config in request body",
			req:     AuthorizeMCPClientRequest{OauthConfig: nil},
			wantErr: true,
		},
		{
			name:    "requires client_id or connection_string",
			mutate:  func(c *schemas.MCPClientConfig) { c.ConnectionString = nil },
			req:     AuthorizeMCPClientRequest{OauthConfig: &OAuthConfigRequest{}},
			wantErr: true,
		},
		{
			name:    "rejects empty client_id without connection_string",
			mutate:  func(c *schemas.MCPClientConfig) { c.ConnectionString = nil },
			req:     AuthorizeMCPClientRequest{OauthConfig: &OAuthConfigRequest{ClientID: &schemas.SecretVar{}}},
			wantErr: true,
		},
		{
			name:    "accepts explicit client_id without connection_string",
			mutate:  func(c *schemas.MCPClientConfig) { c.ConnectionString = nil },
			req:     AuthorizeMCPClientRequest{OauthConfig: &OAuthConfigRequest{ClientID: &schemas.SecretVar{Val: "abc"}}},
			wantErr: false,
		},
		{
			name:    "accepts connection_string discovery",
			req:     AuthorizeMCPClientRequest{OauthConfig: &OAuthConfigRequest{}},
			wantErr: false,
		},
		{
			name:    "accepts connection_string discovery with empty client_id",
			req:     AuthorizeMCPClientRequest{OauthConfig: &OAuthConfigRequest{ClientID: &schemas.SecretVar{}}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := perUserOAuthConfig()
			if tt.mutate != nil {
				tt.mutate(config)
			}
			err := validateAuthorizeMCPClientRequest(config, tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAuthorizeMCPClientRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
