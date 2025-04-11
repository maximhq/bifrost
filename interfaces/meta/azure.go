package meta

type AzureMetaConfig struct {
	Endpoint    string            `json:"endpoint"`
	Deployments map[string]string `json:"deployments,omitempty"`
	APIVersion  *string           `json:"api_version,omitempty"`
}

func (c *AzureMetaConfig) GetSecretAccessKey() *string {
	return nil
}

func (c *AzureMetaConfig) GetRegion() *string {
	return nil
}

func (c *AzureMetaConfig) GetSessionToken() *string {
	return nil
}

func (c *AzureMetaConfig) GetARN() *string {
	return nil
}

func (c *AzureMetaConfig) GetInferenceProfiles() map[string]string {
	return nil
}

func (c *AzureMetaConfig) GetEndpoint() *string {
	return &c.Endpoint
}

func (c *AzureMetaConfig) GetDeployments() map[string]string {
	return c.Deployments
}

func (c *AzureMetaConfig) GetAPIVersion() *string {
	return c.APIVersion
}
