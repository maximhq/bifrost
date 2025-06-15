// Package meta provides provider-specific configuration structures and schemas.
// This file contains the Azure-specific configuration implementation.

package meta

// AzureMetaConfig represents the Azure-specific configuration.
// It contains Azure-specific settings required for service access and deployment management.
type AzureMetaConfig struct {
	Endpoint    string            `json:"endpoint"`              // Azure service endpoint URL
	Deployments map[string]string `json:"deployments,omitempty"` // Mapping of model names to deployment names
	APIVersion  *string           `json:"api_version,omitempty"` // Azure API version to use; defaults to "2024-02-01"
}

// GetEndpoint returns the Azure service endpoint.
// This specifies the base URL for Azure API requests.
func (c *AzureMetaConfig) GetEndpoint() *string {
	return &c.Endpoint
}

// GetDeployments returns the deployment configurations.
// This maps model names to their corresponding Azure deployment names.
// Eg. "gpt-4o": "your-deployment-name-for-gpt-4o"
func (c *AzureMetaConfig) GetDeployments() map[string]string {
	return c.Deployments
}

// GetAPIVersion returns the Azure API version.
// This specifies which version of the Azure API to use.
func (c *AzureMetaConfig) GetAPIVersion() *string {
	return c.APIVersion
}

// These are not used for Azure.
func (c *AzureMetaConfig) GetARN() *string                         { return nil }
func (c *AzureMetaConfig) GetAuthCredentials() *string             { return nil }
func (c *AzureMetaConfig) GetInferenceProfiles() map[string]string { return nil }
func (c *AzureMetaConfig) GetProjectID() *string                   { return nil }
func (c *AzureMetaConfig) GetRegion() *string                      { return nil }
func (c *AzureMetaConfig) GetSecretAccessKey() *string             { return nil }
func (c *AzureMetaConfig) GetSessionToken() *string                { return nil }
