// Package meta provides provider-specific configuration structures and schemas.
// This file contains the AWS Vertex-specific configuration implementation.

package meta

// VertexMetaConfig represents the Vertex-specific configuration.
// It contains Vertex-specific settings required for authentication and service access.
type VertexMetaConfig struct {
	ProjectID       string `json:"project_id,omitempty"`
	Region          string `json:"region,omitempty"`
	AuthCredentials string `json:"auth_credentials,omitempty"`
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetSecretAccessKey() *string {
	return nil
}

// GetRegion returns the Vertex region.
// This is the region for the Vertex project.
func (c *VertexMetaConfig) GetRegion() *string {
	return &c.Region
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetSessionToken() *string {
	return nil
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetARN() *string {
	return nil
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetInferenceProfiles() map[string]string {
	return nil
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetEndpoint() *string {
	return nil
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetDeployments() map[string]string {
	return nil
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetAPIVersion() *string {
	return nil
}

// GetProjectID returns the Vertex project ID.
// This is the project ID for the Vertex project.
func (c *VertexMetaConfig) GetProjectID() *string {
	return &c.ProjectID
}

// GetAuthCredentials returns the authentication credentials for the provider.
// This is the authentication credentials for the google cloud api.
func (c *VertexMetaConfig) GetAuthCredentials() *string {
	return &c.AuthCredentials
}
