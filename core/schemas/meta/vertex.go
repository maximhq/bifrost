// Package meta provides provider-specific configuration structures and schemas.
// This file contains the AWS Vertex-specific configuration implementation.

package meta

// VertexMetaConfig represents the Vertex-specific configuration.
// It contains Vertex-specific settings required for authentication and service access.
type VertexMetaConfig struct {
	ProjectID          string `json:"project_id,omitempty"`
	AuthCredentialPath string `json:"auth_credential_path,omitempty"`
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetSecretAccessKey() *string {
	return nil
}

// This is not used for Vertex.
func (c *VertexMetaConfig) GetRegion() *string {
	return nil
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

// GetAuthCredentialPath returns the path to the authentication credentials for the provider.
// This is the path to the authentication credentials for the google cloud api.
func (c *VertexMetaConfig) GetAuthCredentialPath() *string {
	return &c.AuthCredentialPath
}
