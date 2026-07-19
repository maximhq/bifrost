package vertex

import (
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
)

// Vertex AI Embedding API types

const (
	// VertexServiceTierHeader is the HTTP header used to request priority or flex processing on the global endpoint.
	VertexServiceTierHeader = "X-Vertex-AI-LLM-Shared-Request-Type"
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

// ================================ Batch Prediction API Types ================================

// VertexGcsSource is the GCS input source for a batch prediction job.
type VertexGcsSource struct {
	Uris []string `json:"uris"`
}

// VertexBigQuerySource is the BigQuery input source for a batch prediction job.
type VertexBigQuerySource struct {
	InputUri string `json:"inputUri"`
}

// VertexBatchInputConfig is the input configuration for a batch prediction job.
type VertexBatchInputConfig struct {
	InstancesFormat string                `json:"instancesFormat"`
	GcsSource       *VertexGcsSource      `json:"gcsSource,omitempty"`
	BigquerySource  *VertexBigQuerySource `json:"bigquerySource,omitempty"`
}

// VertexBatchInstanceConfig controls how input instances are converted to prediction instances.
type VertexBatchInstanceConfig struct {
	InstanceType   string   `json:"instanceType,omitempty"`
	KeyField       string   `json:"keyField,omitempty"`
	IncludedFields []string `json:"includedFields,omitempty"`
	ExcludedFields []string `json:"excludedFields,omitempty"`
}

// VertexGcsDestination is the GCS output destination for a batch prediction job.
type VertexGcsDestination struct {
	OutputUriPrefix string `json:"outputUriPrefix"`
}

// VertexBigQueryDestination is the BigQuery output destination for a batch prediction job.
type VertexBigQueryDestination struct {
	OutputUri string `json:"outputUri"`
}

// VertexBatchOutputConfig is the output configuration for a batch prediction job.
type VertexBatchOutputConfig struct {
	PredictionsFormat   string                     `json:"predictionsFormat"`
	GcsDestination      *VertexGcsDestination      `json:"gcsDestination,omitempty"`
	BigqueryDestination *VertexBigQueryDestination `json:"bigqueryDestination,omitempty"`
}

// VertexBatchOutputInfo describes where a finished job wrote its output.
type VertexBatchOutputInfo struct {
	GcsOutputDirectory    string `json:"gcsOutputDirectory,omitempty"`
	BigqueryOutputDataset string `json:"bigqueryOutputDataset,omitempty"`
	BigqueryOutputTable   string `json:"bigqueryOutputTable,omitempty"`
}

// VertexBatchCompletionStats tracks per-request completion counts of a job.
type VertexBatchCompletionStats struct {
	SuccessfulCount              string `json:"successfulCount"`                        // int64 serialised as string
	FailedCount                  string `json:"failedCount"`                            // int64 serialised as string
	IncompleteCount              string `json:"incompleteCount"`                        // int64 serialised as string
	SuccessfulForecastPointCount string `json:"successfulForecastPointCount,omitempty"` // int64 serialised as string
}

// VertexResourcesConsumed reports resources consumed by a batch prediction job.
type VertexResourcesConsumed struct {
	ReplicaHours float64 `json:"replicaHours,omitempty"`
}

// VertexManualBatchTuningParameters configures batch behaviour (only with dedicatedResources).
type VertexManualBatchTuningParameters struct {
	BatchSize int `json:"batchSize,omitempty"`
}

// VertexReservationAffinity configures the reservation a MachineSpec draws resources from.
type VertexReservationAffinity struct {
	ReservationAffinityType string   `json:"reservationAffinityType,omitempty"`
	Key                     string   `json:"key,omitempty"`
	Values                  []string `json:"values,omitempty"`
}

// VertexMachineSpec is the compute machine configuration for dedicated resources.
type VertexMachineSpec struct {
	MachineType         string                     `json:"machineType,omitempty"`
	AcceleratorType     string                     `json:"acceleratorType,omitempty"`
	AcceleratorCount    int                        `json:"acceleratorCount,omitempty"`
	TpuTopology         string                     `json:"tpuTopology,omitempty"`
	ReservationAffinity *VertexReservationAffinity `json:"reservationAffinity,omitempty"`
}

// VertexBatchDedicatedResources is the dedicated compute config used during batch prediction.
type VertexBatchDedicatedResources struct {
	MachineSpec          *VertexMachineSpec `json:"machineSpec,omitempty"`
	StartingReplicaCount int                `json:"startingReplicaCount,omitempty"`
	MaxReplicaCount      int                `json:"maxReplicaCount,omitempty"`
}

// VertexEncryptionSpec is the customer-managed encryption key configuration.
type VertexEncryptionSpec struct {
	KmsKeyName string `json:"kmsKeyName,omitempty"`
}

// VertexPredictSchemata describes the instance/parameter/prediction schemas of a model.
type VertexPredictSchemata struct {
	InstanceSchemaUri   string `json:"instanceSchemaUri,omitempty"`
	ParametersSchemaUri string `json:"parametersSchemaUri,omitempty"`
	PredictionSchemaUri string `json:"predictionSchemaUri,omitempty"`
}

// VertexUnmanagedContainerModel describes a model used without registry upload.
type VertexUnmanagedContainerModel struct {
	ArtifactUri     string                 `json:"artifactUri,omitempty"`
	PredictSchemata *VertexPredictSchemata `json:"predictSchemata,omitempty"`
	// ContainerSpec (ModelContainerSpec) is deeply nested; kept generic for passthrough.
	ContainerSpec map[string]interface{} `json:"containerSpec,omitempty"`
}

// VertexBatchJobError mirrors google.rpc.Status. Used for the job's terminal error as well
// as partialFailures and modelMonitoringStatus. The details array holds google.protobuf.Any
// entries with no fixed schema, so it is kept generic.
type VertexBatchJobError struct {
	Code    int                      `json:"code"`
	Message string                   `json:"message"`
	Details []map[string]interface{} `json:"details,omitempty"`
}

// VertexBatchPredictionJob is the BatchPredictionJob resource returned by the Vertex AI API.
// Fields Bifrost interprets are typed; the deeply-nested explanation/monitoring config trees
// (rarely used for Gemini batch) are kept generic for lossless passthrough.
type VertexBatchPredictionJob struct {
	Name                        string                             `json:"name,omitempty"`
	DisplayName                 string                             `json:"displayName"`
	Model                       string                             `json:"model,omitempty"`
	ModelVersionID              string                             `json:"modelVersionId,omitempty"`
	UnmanagedContainerModel     *VertexUnmanagedContainerModel     `json:"unmanagedContainerModel,omitempty"`
	InputConfig                 VertexBatchInputConfig             `json:"inputConfig"`
	InstanceConfig              *VertexBatchInstanceConfig         `json:"instanceConfig,omitempty"`
	ModelParameters             interface{}                        `json:"modelParameters,omitempty"`
	OutputConfig                VertexBatchOutputConfig            `json:"outputConfig"`
	DedicatedResources          *VertexBatchDedicatedResources     `json:"dedicatedResources,omitempty"`
	ServiceAccount              string                             `json:"serviceAccount,omitempty"`
	ManualBatchTuningParameters *VertexManualBatchTuningParameters `json:"manualBatchTuningParameters,omitempty"`
	GenerateExplanation         bool                               `json:"generateExplanation,omitempty"`
	// ExplanationSpec is deeply nested; kept generic for passthrough.
	ExplanationSpec   map[string]interface{}      `json:"explanationSpec,omitempty"`
	OutputInfo        *VertexBatchOutputInfo      `json:"outputInfo,omitempty"`
	State             string                      `json:"state,omitempty"`
	Error             *VertexBatchJobError        `json:"error,omitempty"`
	PartialFailures   []VertexBatchJobError       `json:"partialFailures,omitempty"`
	ResourcesConsumed *VertexResourcesConsumed    `json:"resourcesConsumed,omitempty"`
	CompletionStats   *VertexBatchCompletionStats `json:"completionStats,omitempty"`
	CreateTime        string                      `json:"createTime,omitempty"` // RFC3339
	StartTime         string                      `json:"startTime,omitempty"`  // RFC3339
	EndTime           string                      `json:"endTime,omitempty"`    // RFC3339
	UpdateTime        string                      `json:"updateTime,omitempty"` // RFC3339
	Labels            map[string]string           `json:"labels,omitempty"`
	EncryptionSpec    *VertexEncryptionSpec       `json:"encryptionSpec,omitempty"`
	// ModelMonitoringConfig / ModelMonitoringStatsAnomalies are deeply nested; kept generic.
	ModelMonitoringConfig         map[string]interface{}   `json:"modelMonitoringConfig,omitempty"`
	ModelMonitoringStatsAnomalies []map[string]interface{} `json:"modelMonitoringStatsAnomalies,omitempty"`
	ModelMonitoringStatus         *VertexBatchJobError     `json:"modelMonitoringStatus,omitempty"`
	DisableContainerLogging       bool                     `json:"disableContainerLogging,omitempty"`
	SatisfiesPzs                  bool                     `json:"satisfiesPzs,omitempty"`
	SatisfiesPzi                  bool                     `json:"satisfiesPzi,omitempty"`
}

// VertexBatchCreateRequest is the request body for creating a BatchPredictionJob. Only the
// fields Bifrost maps directly are typed; any other Vertex-native field (modelParameters,
// labels, modelVersionId, encryptionSpec, instanceConfig, ...) is passed through ExtraParams
// and merged into the body by CheckContextAndGetRequestBody.
type VertexBatchCreateRequest struct {
	DisplayName  string                  `json:"displayName"`
	Model        string                  `json:"model"`
	InputConfig  VertexBatchInputConfig  `json:"inputConfig"`
	OutputConfig VertexBatchOutputConfig `json:"outputConfig"`

	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams implements the providerUtils.RequestBodyWithExtraParams interface.
func (r *VertexBatchCreateRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
}

// VertexBatchJobListResponse is the batchPredictionJobs.list response envelope.
type VertexBatchJobListResponse struct {
	BatchPredictionJobs []VertexBatchPredictionJob `json:"batchPredictionJobs"`
	NextPageToken       string                     `json:"nextPageToken"`
}

// VertexBatchOutputLine is one line of a predictions-*.jsonl batch output file.
// The original request is echoed back; the Bifrost custom_id round-trips via the native
// top-level "custom_id" (Anthropic/Claude jobs) or the request labels (Gemini jobs).
type VertexBatchOutputLine struct {
	CustomID string `json:"custom_id,omitempty"` // native custom_id echoed by Anthropic-on-Vertex batch
	Status   string `json:"status,omitempty"`    // error string for failed records, empty on success
	Request  struct {
		Labels map[string]string `json:"labels"`
	} `json:"request"`
	Response map[string]interface{} `json:"response,omitempty"`
}

// ================================ GCS File API Types ================================

// gcsObjectMetadata represents GCS object metadata as returned by the JSON API.
type gcsObjectMetadata struct {
	Name        string            `json:"name"`
	Bucket      string            `json:"bucket"`
	Size        string            `json:"size"` // int64 serialised as string by GCS
	ContentType string            `json:"contentType"`
	TimeCreated string            `json:"timeCreated"` // RFC3339
	Updated     string            `json:"updated"`     // RFC3339
	Metadata    map[string]string `json:"metadata"`
}

// gcsObjectListResponse is the GCS object list response envelope.
type gcsObjectListResponse struct {
	NextPageToken string              `json:"nextPageToken"`
	Items         []gcsObjectMetadata `json:"items"`
}

// gcsErrorBody is the GCS API error response envelope.
type gcsErrorBody struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
