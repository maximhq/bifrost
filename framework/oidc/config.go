package oidc

import "fmt"

// OIDCConfig holds the OIDC configuration parsed from config.json's "oidc" section.
type OIDCConfig struct {
	IssuerURL    string   `json:"issuer_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes"`
	OrgClaim     string   `json:"org_claim"`
	GroupsClaim  string   `json:"groups_claim"`
}

// Validate checks required fields and sets defaults for optional fields.
func (c *OIDCConfig) Validate() error {
	if c.IssuerURL == "" {
		return fmt.Errorf("oidc: issuer_url is required")
	}
	if c.ClientID == "" {
		return fmt.Errorf("oidc: client_id is required")
	}
	if c.OrgClaim == "" {
		c.OrgClaim = "organization_id"
	}
	if c.GroupsClaim == "" {
		c.GroupsClaim = "groups"
	}
	if len(c.Scopes) == 0 {
		c.Scopes = []string{"openid", "profile", "email"}
	}
	return nil
}
