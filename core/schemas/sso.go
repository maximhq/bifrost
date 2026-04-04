package schemas

// GoogleSSOConfig holds configuration for Google OAuth SSO login.
type GoogleSSOConfig struct {
	ClientID     *EnvVar `json:"client_id"`
	ClientSecret *EnvVar `json:"client_secret"`
	RedirectURI  string  `json:"redirect_uri,omitempty"`
}

// SAMLConfig holds configuration for SAML-based SSO login.
type SAMLConfig struct {
	IDPURL      string `json:"idp_url"`
	Certificate string `json:"certificate,omitempty"`
	EntityID    string `json:"entity_id,omitempty"`
}
