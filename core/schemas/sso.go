package schemas

// GoogleSSOConfig holds configuration for Google OAuth SSO login.
type GoogleSSOConfig struct {
	ClientID       *EnvVar  `json:"client_id"`
	ClientSecret   *EnvVar  `json:"client_secret"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// SAMLConfig holds configuration for SAML SSO login.
type SAMLConfig struct {
	EntityID       string `json:"entity_id"`
	MetadataURL    string `json:"metadata_url,omitempty"`
	IdPSSOURL      string `json:"idp_sso_url,omitempty"`
	IdPCertificate string `json:"idp_certificate,omitempty"`
	IdPEntityID    string `json:"idp_entity_id,omitempty"`
	SignRequests   bool   `json:"sign_requests"`
	ForceAuthn     bool   `json:"force_authn"`
	NameIDFormat   string `json:"name_id_format,omitempty"`
}
