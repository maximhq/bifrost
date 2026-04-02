package vertex

import (
	"time"

	"github.com/maximhq/bifrost/core/providers/gemini"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
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
	return providerUtils.MarshalSorted(r.RequestBody)
}

// VertexRawRequestBody holds pre-serialized JSON bytes to preserve key ordering
// for LLM prompt caching. This avoids the map[string]interface{} round-trip that
// destroys key order.
type VertexRawRequestBody struct {
	RawBody     []byte                 `json:"-"`
	ExtraParams map[string]interface{} `json:"-"`
}

func (r *VertexRawRequestBody) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// MarshalJSON returns the pre-serialized JSON bytes directly, preserving key order.
func (r *VertexRawRequestBody) MarshalJSON() ([]byte, error) {
	return r.RawBody, nil
}

// VertexAdvancedVoiceOptions represents advanced voice options for TTS synthesis.
type VertexAdvancedVoiceOptions struct {
	LowLatencyJourneySynthesis bool `json:"lowLatencyJourneySynthesis,omitempty"`
}

// VertexMultimodalImageInput represents an image for the multimodalembedding@001 model.
// Exactly one of BytesBase64Encoded or GCSUri must be set.
type VertexMultimodalImageInput struct {
	BytesBase64Encoded *string `json:"bytesBase64Encoded,omitempty"` // Raw base64 string (no data URI prefix)
	GCSUri             *string `json:"gcsUri,omitempty"`             // gs://bucket/object
}

// VertexVideoSegmentConfig controls which portion of a video is embedded.
type VertexVideoSegmentConfig struct {
	StartOffsetSec *int `json:"startOffsetSec,omitempty"`
	EndOffsetSec   *int `json:"endOffsetSec,omitempty"`
	IntervalSec    *int `json:"intervalSec,omitempty"` // 4=Essential, 8=Standard, 16=Plus
}

// VertexMultimodalVideoInput represents a video for the multimodalembedding@001 model.
// Exactly one of BytesBase64Encoded or GCSUri must be set.
type VertexMultimodalVideoInput struct {
	BytesBase64Encoded *string                   `json:"bytesBase64Encoded,omitempty"` // Raw base64 string (no data URI prefix)
	GCSUri             *string                   `json:"gcsUri,omitempty"`             // gs://bucket/object
	VideoSegmentConfig *VertexVideoSegmentConfig `json:"videoSegmentConfig,omitempty"`
}

// VertexVideoEmbedding is one segment's embedding returned for a video input.
type VertexVideoEmbedding struct {
	StartOffsetSec int       `json:"startOffsetSec"`
	EndOffsetSec   int       `json:"endOffsetSec"`
	Embedding      []float64 `json:"embedding"`
}

// VertexEmbeddingInstance represents a single embedding instance in the request.
// For text embedding models (text-multilingual-embedding-*): populate Content, TaskType, Title.
// For the native multimodal model (multimodalembedding@001): populate Text, Image, and/or Video.
type VertexEmbeddingInstance struct {
	// Text embedding fields
	Content  string  `json:"content,omitempty"`   // Plain text for text-only embedding models
	TaskType *string `json:"task_type,omitempty"` // Downstream task hint (text models only)
	Title    *string `json:"title,omitempty"`     // Optional title (text models only)

	// Native multimodal embedding fields (multimodalembedding@001)
	Text  *string                     `json:"text,omitempty"`
	Image *VertexMultimodalImageInput `json:"image,omitempty"`
	Video *VertexMultimodalVideoInput `json:"video,omitempty"`
}

// VertexEmbeddingParameters represents the parameters for the embedding request.
// Dimension applies to multimodalembedding@001; OutputDimensionality to text models.
type VertexEmbeddingParameters struct {
	AutoTruncate         *bool `json:"autoTruncate,omitempty"`         // Truncate long inputs (text models)
	OutputDimensionality *int  `json:"outputDimensionality,omitempty"` // Output dimensions (text models)
	Dimension            *int  `json:"dimension,omitempty"`            // Output dimensions (multimodalembedding@001)
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

type VertexGeminiEmbeddingRequest struct {
	Content              *gemini.Content        `json:"content,omitempty"`
	DocumentOCR          *bool                  `json:"documentOcr,omitempty"`
	AudioTrackExtraction *bool                  `json:"audioTrackExtraction,omitempty"`
	TaskType             *string                `json:"taskType,omitempty"`
	Title                *string                `json:"title,omitempty"`
	OutputDimensionality *int                   `json:"outputDimensionality,omitempty"`
	AutoTruncate         *bool                  `json:"autoTruncate,omitempty"`
	ExtraParams          map[string]interface{} `json:"-"`
}

func (r *VertexGeminiEmbeddingRequest) GetExtraParams() map[string]interface{} {
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

// VertexEmbeddingPrediction represents a single prediction in the response.
// Text embedding models populate Embeddings.
// The native multimodal model (multimodalembedding@001) populates TextEmbedding,
// ImageEmbedding, and/or VideoEmbeddings — all in the same embedding space.
type VertexEmbeddingPrediction struct {
	// Text embedding model response
	Embeddings *VertexEmbeddingValues `json:"embeddings,omitempty"`

	// Native multimodal model response (multimodalembedding@001)
	TextEmbedding   []float64              `json:"textEmbedding,omitempty"`
	ImageEmbedding  []float64              `json:"imageEmbedding,omitempty"`
	VideoEmbeddings []VertexVideoEmbedding `json:"videoEmbeddings,omitempty"`
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

// ================================ Publisher Model Types ================================
// These types are for the publishers.models.list endpoint (Model Garden)

type VertexPublisherModel struct {
	Name                   string                       `json:"name"`
	VersionID              string                       `json:"versionId"`
	OpenSourceCategory     string                       `json:"openSourceCategory"`
	LaunchStage            string                       `json:"launchStage"`
	VersionState           string                       `json:"versionState"`
	PublisherModelTemplate string                       `json:"publisherModelTemplate"`
	SupportedActions       *VertexPublisherModelActions `json:"supportedActions"`
}

type VertexPublisherModelActions struct {
	OpenGenerationAIStudio   *VertexPublisherModelURI    `json:"openGenerationAiStudio"`
	OpenGenie                *VertexPublisherModelURI    `json:"openGenie"`
	OpenPromptTuningPipeline *VertexPublisherModelURI    `json:"openPromptTuningPipeline"`
	OpenNotebook             *VertexPublisherModelURI    `json:"openNotebook"`
	OpenFineTuningPipeline   *VertexPublisherModelURI    `json:"openFineTuningPipeline"`
	Deploy                   *VertexPublisherModelDeploy `json:"deploy"`
	OpenEvaluationPipeline   *VertexPublisherModelURI    `json:"openEvaluationPipeline"`
}

type VertexPublisherModelURI struct {
	URI string `json:"uri"`
}

type VertexPublisherModelDeploy struct {
	ModelDisplayName string `json:"modelDisplayName"`
	Title            string `json:"title"`
}

type VertexListPublisherModelsResponse struct {
	PublisherModels []VertexPublisherModel `json:"publisherModels"`
	NextPageToken   string                 `json:"nextPageToken"`
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
