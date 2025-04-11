package meta

type BedrockMetaConfig struct {
	SecretAccessKey   *string           `json:"secret_access_key,omitempty"`
	Region            *string           `json:"region,omitempty"`
	SessionToken      *string           `json:"session_token,omitempty"`
	ARN               *string           `json:"arn,omitempty"`
	InferenceProfiles map[string]string `json:"inference_profiles,omitempty"`
}

func (c *BedrockMetaConfig) GetSecretAccessKey() *string {
	return c.SecretAccessKey
}

func (c *BedrockMetaConfig) GetRegion() *string {
	return c.Region
}

func (c *BedrockMetaConfig) GetSessionToken() *string {
	return c.SessionToken
}

func (c *BedrockMetaConfig) GetARN() *string {
	return c.ARN
}

func (c *BedrockMetaConfig) GetInferenceProfiles() map[string]string {
	return c.InferenceProfiles
}
