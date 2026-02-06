package vertex

import (
	"time"

	"github.com/bytedance/sonic"
)

// Vertex AI Embedding API types

const (
	DefaultVertexAnthropicVersion = "vertex-2023-10-16"
)

// PhoneticEncoding represents the phonetic encoding of a phrase.
type PhoneticEncoding string

const (
	PhoneticEncodingUnspecified      PhoneticEncoding = "PHONETIC_ENCODING_UNSPECIFIED"
	PhoneticEncodingIPA              PhoneticEncoding = "PHONETIC_ENCODING_IPA"
	PhoneticEncodingXSAMPA           PhoneticEncoding = "PHONETIC_ENCODING_X_SAMPA"
	PhoneticEncodingJapaneseYomigana PhoneticEncoding = "PHONETIC_ENCODING_JAPANESE_YOMIGANA"
	PhoneticEncodingPinyin           PhoneticEncoding = "PHONETIC_ENCODING_PINYIN"
)

// CustomPronunciationParams represents pronunciation customization for a phrase.
type CustomPronunciationParams struct {
	Phrase           string           `json:"phrase,omitempty"`
	PhoneticEncoding PhoneticEncoding `json:"phoneticEncoding,omitempty"`
	Pronunciation    string           `json:"pronunciation,omitempty"`
}

// CustomPronunciations represents a collection of pronunciation customizations.
type CustomPronunciations struct {
	Pronunciations []CustomPronunciationParams `json:"pronunciations,omitempty"`
}

// Turn represents a multi-speaker turn.
type Turn struct {
	Speaker string `json:"speaker,omitempty"`
	Text    string `json:"text,omitempty"`
}

// MultiSpeakerMarkup represents a collection of turns for multi-speaker synthesis.
type MultiSpeakerMarkup struct {
	Turns []Turn `json:"turns,omitempty"`
}

// VertexSynthesisInput contains text input to be synthesized.
type VertexSynthesisInput struct {
	Text                 *string               `json:"text,omitempty"`
	Markup               *string               `json:"markup,omitempty"`
	SSML                 *string               `json:"ssml,omitempty"`
	MultiSpeakerMarkup   *MultiSpeakerMarkup   `json:"multiSpeakerMarkup,omitempty"`
	Prompt               *string               `json:"prompt,omitempty"`
	CustomPronunciations *CustomPronunciations `json:"customPronunciations,omitempty"`
}

// VertexVoiceSelectionParams represents voice selection parameters for TTS synthesis.
type VertexVoiceSelectionParams struct {
	LanguageCode string `json:"languageCode,omitempty"`
	Name         string `json:"name,omitempty"`
	SsmlGender   string `json:"ssmlGender,omitempty"`
}

// VertexAudioConfig represents audio configuration for TTS synthesis.
type VertexAudioConfig struct {
	AudioEncoding    string   `json:"audioEncoding,omitempty"`
	SpeakingRate     float64  `json:"speakingRate,omitempty"`
	Pitch            float64  `json:"pitch,omitempty"`
	VolumeGainDB     float64  `json:"volumeGainDb,omitempty"`
	SampleRateHertz  int      `json:"sampleRateHertz,omitempty"`
	EffectsProfileID []string `json:"effectsProfileId,omitempty"`
}

type VertexRequestBody struct {
	RequestBody map[string]interface{} `json:"-"`
	ExtraParams map[string]interface{} `json:"-"`
}

func (r *VertexRequestBody) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// MarshalJSON implements custom JSON marshalling for VertexRequestBody.
// It marshals the RequestBody field directly without wrapping.
func (r *VertexRequestBody) MarshalJSON() ([]byte, error) {
	return sonic.Marshal(r.RequestBody)
}

// VertexAdvancedVoiceOptions represents advanced voice options for TTS synthesis.
type VertexAdvancedVoiceOptions struct {
	LowLatencyJourneySynthesis bool `json:"lowLatencyJourneySynthesis,omitempty"`
}

// VertexEmbeddingInstance represents a single embedding instance in the request
type VertexEmbeddingInstance struct {
	Content  string  `json:"content"`             // The text to generate embeddings for
	TaskType *string `json:"task_type,omitempty"` // Intended downstream application (optional)
	Title    *string `json:"title,omitempty"`     // Used to help the model produce better embeddings (optional)
}

// VertexEmbeddingParameters represents the parameters for the embedding request
type VertexEmbeddingParameters struct {
	AutoTruncate         *bool `json:"autoTruncate,omitempty"`         // When true, input text will be truncated (defaults to true)
	OutputDimensionality *int  `json:"outputDimensionality,omitempty"` // Output embedding size (optional)
}

// VertexEmbeddingRequest represents the complete embedding request to Vertex AI
type VertexEmbeddingRequest struct {
	Instances   []VertexEmbeddingInstance  `json:"instances"`            // List of embedding instances
	Parameters  *VertexEmbeddingParameters `json:"parameters,omitempty"` // Optional parameters
	ExtraParams map[string]interface{}     `json:"-"`                    // Optional: Extra parameters
}

func (r *VertexEmbeddingRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// VertexEmbeddingStatistics represents statistics computed from the input text
type VertexEmbeddingStatistics struct {
	Truncated  bool `json:"truncated"`   // Whether the input text was truncated
	TokenCount int  `json:"token_count"` // Number of tokens in the input text
}

// VertexEmbeddingValues represents the embedding result
type VertexEmbeddingValues struct {
	Values     []float64                  `json:"values"`     // The embedding vector (list of floats)
	Statistics *VertexEmbeddingStatistics `json:"statistics"` // Statistics about the input text
}

// VertexEmbeddingPrediction represents a single prediction in the response
type VertexEmbeddingPrediction struct {
	Embeddings *VertexEmbeddingValues `json:"embeddings"` // The embedding result
}

// VertexEmbeddingResponse represents the complete embedding response from Vertex AI
type VertexEmbeddingResponse struct {
	Predictions []VertexEmbeddingPrediction `json:"predictions"` // List of embedding predictions
}

// ================================ Model Types ================================

const MaxPageSize = 100

type VertexModel struct {
	Name              string                `json:"name"`
	VersionId         string                `json:"versionId"`
	VersionAliases    []string              `json:"versionAliases"`
	VersionCreateTime time.Time             `json:"versionCreateTime"`
	DisplayName       string                `json:"displayName"`
	Description       string                `json:"description"`
	DeployedModels    []VertexDeployedModel `json:"deployedModels"`
	Labels            VertexModelLabels     `json:"labels"`
}

type VertexListModelsResponse struct {
	Models        []VertexModel `json:"models"`
	NextPageToken string        `json:"nextPageToken"`
}

type VertexDeployedModel struct {
	CheckpointID string `json:"checkpointId"`
	DeploymentID string `json:"deploymentId"`
	Endpoint     string `json:"endpoint"`
}

type VertexModelLabels struct {
	GoogleVertexLLMTuningBaseModelId string `json:"google-vertex-llm-tuning-base-model-id"`
	GoogleVertexLLMTuningJobId       string `json:"google-vertex-llm-tuning-job-id"`
	TuneType                         string `json:"tune-type"`
}

// ==================== ERROR TYPES ====================
// VertexValidationError represents validation errors
// returned by the Vertex Mistral endpoint
type VertexValidationError struct {
	Detail []struct {
		Input any    `json:"input"` // can be number, object, or array
		Loc   []any  `json:"loc"`   // location of the error (can contain strings and numeric indices)
		Msg   string `json:"msg"`   // error message
		Type  string `json:"type"`  // error type (e.g., "extra_forbidden", "missing")
	} `json:"detail"`
}

// VertexCountTokensResponse models the response payload for Vertex's Gemini-style countTokens.
// Vertex uses camelCase unlike other request json body.
type VertexCountTokensResponse struct {
	TotalTokens             int32 `json:"totalTokens,omitempty"`
	CachedContentTokenCount int32 `json:"cachedContentTokenCount,omitempty"`
}
