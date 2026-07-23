package schemas

// BifrostListInferenceProfilesRequest lists AWS Bedrock inference profiles.
// The fields mirror ListInferenceProfiles' query parameters.
type BifrostListInferenceProfilesRequest struct {
	Provider   ModelProvider `json:"provider"`
	MaxResults *int          `json:"max_results,omitempty"`
	NextToken  *string       `json:"next_token,omitempty"`
	Type       *string       `json:"type,omitempty"`
}

// BifrostGetInferenceProfileRequest retrieves one AWS Bedrock inference profile.
// InferenceProfileIdentifier is also the model identifier used for policy and
// key selection, because it is what callers submit to Bedrock runtime endpoints.
type BifrostGetInferenceProfileRequest struct {
	Provider                   ModelProvider `json:"provider"`
	InferenceProfileIdentifier string        `json:"inference_profile_identifier"`
}

// BifrostInferenceProfileModel is a model associated with an AWS Bedrock
// inference profile.
type BifrostInferenceProfileModel struct {
	ModelArn string `json:"modelArn"`
}

// BifrostInferenceProfileSummary preserves the AWS Bedrock inference profile
// representation. Time fields intentionally remain strings so the HTTP
// integration can return AWS-compatible payloads without translation.
type BifrostInferenceProfileSummary struct {
	InferenceProfileName string                         `json:"inferenceProfileName"`
	Description          string                         `json:"description,omitempty"`
	CreatedAt            string                         `json:"createdAt"`
	UpdatedAt            string                         `json:"updatedAt"`
	InferenceProfileArn  string                         `json:"inferenceProfileArn"`
	Models               []BifrostInferenceProfileModel `json:"models"`
	InferenceProfileID   string                         `json:"inferenceProfileId"`
	Status               string                         `json:"status"`
	Type                 string                         `json:"type"`
}

// BifrostListInferenceProfilesResponse is the AWS ListInferenceProfiles result.
type BifrostListInferenceProfilesResponse struct {
	InferenceProfileSummaries []BifrostInferenceProfileSummary `json:"inferenceProfileSummaries"`
	NextToken                 *string                          `json:"nextToken,omitempty"`
	ExtraFields               BifrostResponseExtraFields       `json:"extra_fields"`
}

// BifrostGetInferenceProfileResponse is the AWS GetInferenceProfile result.
type BifrostGetInferenceProfileResponse struct {
	BifrostInferenceProfileSummary
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
