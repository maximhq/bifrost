// Package meta provides provider-specific configuration structures and schemas.
// This file contains the AWS Bedrock-specific configuration implementation.

package meta

// BedrockMetaConfig represents the AWS Bedrock-specific configuration.
// It contains AWS-specific settings required for authentication and service access.
type BedrockMetaConfig struct {
	SecretAccessKey   string            `json:"secret_access_key,omitempty"`  // AWS secret access key for authentication
	Region            *string           `json:"region,omitempty"`             // AWS region for service access
	SessionToken      *string           `json:"session_token,omitempty"`      // AWS session token for temporary credentials
	ARN               *string           `json:"arn,omitempty"`                // Amazon Resource Name for resource identification
	InferenceProfiles map[string]string `json:"inference_profiles,omitempty"` // Mapping of model identifiers to inference profiles
}

// GetSecretAccessKey returns the AWS secret access key.
// This is used for AWS API authentication.
func (c *BedrockMetaConfig) GetSecretAccessKey() *string {
	return &c.SecretAccessKey
}

// GetRegion returns the AWS region.
// This specifies which AWS region the service should be accessed from.
func (c *BedrockMetaConfig) GetRegion() *string {
	return c.Region
}

// GetSessionToken returns the AWS session token.
// This is used for temporary credentials in AWS authentication.
func (c *BedrockMetaConfig) GetSessionToken() *string {
	return c.SessionToken
}

// GetARN returns the Amazon Resource Name.
// This uniquely identifies AWS resources.
func (c *BedrockMetaConfig) GetARN() *string {
	return c.ARN
}

// GetInferenceProfiles returns the inference profiles mapping.
// This maps model identifiers to their corresponding inference profiles.
func (c *BedrockMetaConfig) GetInferenceProfiles() map[string]string {
	return c.InferenceProfiles
}

// This is not used for Bedrock.
func (c *BedrockMetaConfig) GetEndpoint() *string {
	return nil
}

// This is not used for Bedrock.
func (c *BedrockMetaConfig) GetDeployments() map[string]string {
	return nil
}

// This is not used for Bedrock.
func (c *BedrockMetaConfig) GetAPIVersion() *string {
	return nil
}

// This is not used for Bedrock.
func (c *BedrockMetaConfig) GetProjectID() *string {
	return nil
}

// This is not used for Bedrock.
func (c *BedrockMetaConfig) GetAuthCredentialPath() *string {
	return nil
}
