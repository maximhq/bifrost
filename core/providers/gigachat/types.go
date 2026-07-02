// Package gigachat implements the GigaChat LLM provider.
package gigachat

import (
	"bytes"
	"encoding/json"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// # AUTH TYPES

// GigaChatTokenResponse is returned by the GigaChat OAuth endpoint.
type GigaChatTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   int64  `json:"expires_at"`
}

// GigaChatPasswordTokenResponse is returned by the SDK-backed password auth endpoint.
type GigaChatPasswordTokenResponse struct {
	Token     string `json:"tok"`
	ExpiresAt int64  `json:"exp"`
}

// # CHAT TYPES

// GigaChatChatRequest is the v1 chat completions request body.
type GigaChatChatRequest struct {
	Model           string                 `json:"model"`
	Messages        []GigaChatChatMessage  `json:"messages"`
	Temperature     *float64               `json:"temperature,omitempty"`
	TopP            *float64               `json:"top_p,omitempty"`
	MaxTokens       *int                   `json:"max_tokens,omitempty"`
	N               *int                   `json:"n,omitempty"`
	Stop            []string               `json:"stop,omitempty"`
	Stream          *bool                  `json:"stream,omitempty"`
	ReasoningEffort *string                `json:"reasoning_effort,omitempty"`
	ResponseFormat  interface{}            `json:"response_format,omitempty"`
	FunctionCall    interface{}            `json:"function_call,omitempty"`
	Functions       []GigaChatFunction     `json:"functions,omitempty"`
	ExtraParams     map[string]interface{} `json:"-"`
}

// GetExtraParams returns provider-specific passthrough fields.
func (request *GigaChatChatRequest) GetExtraParams() map[string]interface{} {
	if request == nil || request.ExtraParams == nil {
		return make(map[string]interface{}, 0)
	}
	return request.ExtraParams
}

// GigaChatChatMessage is a GigaChat v1 chat message.
type GigaChatChatMessage struct {
	Role             string                      `json:"role,omitempty"`
	Content          *schemas.ChatMessageContent `json:"content,omitempty"`
	Attachments      []string                    `json:"attachments,omitempty"`
	Name             *string                     `json:"name,omitempty"`
	Reasoning        *string                     `json:"reasoning_content,omitempty"`
	FunctionCall     *GigaChatFunctionCall       `json:"function_call,omitempty"`
	FunctionsStateID *string                     `json:"functions_state_id,omitempty"`
}

// UnmarshalJSON accepts both GigaChat's reasoning_content field and legacy
// reasoning-shaped payloads while preserving reasoning_content for outbound JSON.
func (message *GigaChatChatMessage) UnmarshalJSON(data []byte) error {
	type Alias GigaChatChatMessage
	var aux struct {
		Alias
		LegacyReasoning *string `json:"reasoning,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*message = GigaChatChatMessage(aux.Alias)
	if message.Reasoning == nil && aux.LegacyReasoning != nil {
		message.Reasoning = aux.LegacyReasoning
	}
	return nil
}

// GigaChatFunctionCall is the legacy GigaChat function-call shape.
type GigaChatFunctionCall struct {
	Name      string          `json:"name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// GigaChatFunctionCallChoice forces a specific GigaChat function call.
type GigaChatFunctionCallChoice struct {
	Name string `json:"name"`
}

// GigaChatFunction describes a client-defined function for GigaChat function calling.
type GigaChatFunction struct {
	Name             string                          `json:"name"`
	Description      *string                         `json:"description,omitempty"`
	Parameters       *schemas.ToolFunctionParameters `json:"parameters,omitempty"`
	FewShotExamples  []map[string]interface{}        `json:"few_shot_examples,omitempty"`
	ReturnParameters map[string]interface{}          `json:"return_parameters,omitempty"`
}

// GigaChatChatResponse is the v1 chat completions response body.
type GigaChatChatResponse struct {
	ID                string                 `json:"id,omitempty"`
	Choices           []GigaChatChatChoice   `json:"choices,omitempty"`
	Created           int                    `json:"created,omitempty"`
	Model             string                 `json:"model,omitempty"`
	Object            string                 `json:"object,omitempty"`
	SystemFingerprint string                 `json:"system_fingerprint,omitempty"`
	Usage             *GigaChatChatUsage     `json:"usage,omitempty"`
	ExtraParams       map[string]interface{} `json:"-"`
}

// GigaChatChatChoice is a single v1 chat completion choice.
type GigaChatChatChoice struct {
	Index        int                      `json:"index"`
	Message      *GigaChatChatMessage     `json:"message,omitempty"`
	FinishReason *string                  `json:"finish_reason,omitempty"`
	LogProbs     *schemas.BifrostLogProbs `json:"logprobs,omitempty"`
}

// GigaChatChatStreamResponse is a v1 chat completions SSE chunk.
type GigaChatChatStreamResponse struct {
	ID                string                     `json:"id,omitempty"`
	Choices           []GigaChatChatStreamChoice `json:"choices,omitempty"`
	Created           int                        `json:"created,omitempty"`
	Model             string                     `json:"model,omitempty"`
	Object            string                     `json:"object,omitempty"`
	SystemFingerprint string                     `json:"system_fingerprint,omitempty"`
	Usage             *GigaChatChatUsage         `json:"usage,omitempty"`
	ExtraParams       map[string]interface{}     `json:"-"`
}

// GigaChatChatStreamChoice is a single streaming choice.
type GigaChatChatStreamChoice struct {
	Index        int                      `json:"index"`
	Delta        *GigaChatChatStreamDelta `json:"delta,omitempty"`
	FinishReason *string                  `json:"finish_reason,omitempty"`
	LogProbs     *schemas.BifrostLogProbs `json:"logprobs,omitempty"`
}

// GigaChatChatStreamDelta is the partial assistant message in an SSE chunk.
type GigaChatChatStreamDelta struct {
	Role             *string               `json:"role,omitempty"`
	Content          *string               `json:"content,omitempty"`
	Reasoning        *string               `json:"reasoning_content,omitempty"`
	FunctionCall     *GigaChatFunctionCall `json:"function_call,omitempty"`
	FunctionsStateID *string               `json:"functions_state_id,omitempty"`
}

// UnmarshalJSON accepts both reasoning_content and reasoning stream fields.
func (delta *GigaChatChatStreamDelta) UnmarshalJSON(data []byte) error {
	type Alias GigaChatChatStreamDelta
	var aux struct {
		Alias
		LegacyReasoning *string `json:"reasoning,omitempty"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*delta = GigaChatChatStreamDelta(aux.Alias)
	if delta.Reasoning == nil && aux.LegacyReasoning != nil {
		delta.Reasoning = aux.LegacyReasoning
	}
	return nil
}

// GigaChatChatUsage is token usage returned by GigaChat chat completions.
type GigaChatChatUsage struct {
	PromptTokens          int                   `json:"prompt_tokens,omitempty"`
	CompletionTokens      int                   `json:"completion_tokens,omitempty"`
	TotalTokens           int                   `json:"total_tokens,omitempty"`
	PrecachedPromptTokens int                   `json:"precached_prompt_tokens,omitempty"`
	InputTokens           int                   `json:"input_tokens,omitempty"`
	OutputTokens          int                   `json:"output_tokens,omitempty"`
	InputTokensDetails    *GigaChatTokenDetails `json:"input_tokens_details,omitempty"`
}

// GigaChatTokenDetails is the token breakdown shape used by GigaChat v2.
type GigaChatTokenDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CachedReadTokens int `json:"cached_read_tokens,omitempty"`
}

// # MODELS TYPES

// GigaChatListModelsResponse is the v1 models list response body.
type GigaChatListModelsResponse struct {
	Object string          `json:"object"`
	Data   []GigaChatModel `json:"data"`
}

// GigaChatModel is a single model descriptor returned by GigaChat.
type GigaChatModel struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	Type    string `json:"type,omitempty"`
}

// # FILES TYPES

// GigaChatUploadedFile is a file metadata object returned by GigaChat.
type GigaChatUploadedFile struct {
	ID           string  `json:"id"`
	Object       string  `json:"object,omitempty"`
	Bytes        int64   `json:"bytes"`
	CreatedAt    int64   `json:"created_at"`
	Filename     string  `json:"filename"`
	Purpose      string  `json:"purpose"`
	AccessPolicy *string `json:"access_policy,omitempty"`
}

// GigaChatUploadedFiles is a list wrapper for GigaChat file metadata.
type GigaChatUploadedFiles struct {
	Data []GigaChatUploadedFile `json:"data"`
}

// GigaChatDeletedFile is returned by GigaChat after deleting a file.
type GigaChatDeletedFile struct {
	ID      string `json:"id"`
	Deleted bool   `json:"deleted"`
}

// GigaChatFileContent contains base64-encoded file content.
type GigaChatFileContent struct {
	Content string `json:"content"`
}

// # BATCH TYPES

// GigaChatBatchMethod selects the target operation for GigaChat batch execution.
type GigaChatBatchMethod string

const (
	GigaChatBatchMethodChatCompletions GigaChatBatchMethod = "chat_completions"
	GigaChatBatchMethodResponses       GigaChatBatchMethod = "responses"
	GigaChatBatchMethodEmbedder        GigaChatBatchMethod = "embedder"
)

// GigaChatBatchStatus is the lifecycle state returned by GigaChat batch APIs.
type GigaChatBatchStatus string

const (
	GigaChatBatchStatusCreated    GigaChatBatchStatus = "created"
	GigaChatBatchStatusInProgress GigaChatBatchStatus = "in_progress"
	GigaChatBatchStatusCompleted  GigaChatBatchStatus = "completed"
)

// GigaChatBatchRequestCounts tracks processed rows in a GigaChat batch job.
type GigaChatBatchRequestCounts struct {
	Total     int `json:"total,omitempty"`
	Completed int `json:"completed,omitempty"`
	Failed    int `json:"failed,omitempty"`
}

// GigaChatBatch is a batch metadata object returned by GigaChat.
type GigaChatBatch struct {
	ID               string                      `json:"id"`
	Object           string                      `json:"object,omitempty"`
	Method           GigaChatBatchMethod         `json:"method,omitempty"`
	Status           GigaChatBatchStatus         `json:"status,omitempty"`
	RequestCounts    *GigaChatBatchRequestCounts `json:"request_counts,omitempty"`
	InputFileID      *string                     `json:"input_file_id,omitempty"`
	OutputFileID     *string                     `json:"output_file_id,omitempty"`
	ResultFileID     *string                     `json:"result_file_id,omitempty"`
	ErrorFileID      *string                     `json:"error_file_id,omitempty"`
	CompletionWindow string                      `json:"completion_window,omitempty"`
	CreatedAt        int64                       `json:"created_at,omitempty"`
	UpdatedAt        *int64                      `json:"updated_at,omitempty"`
	CompletedAt      *int64                      `json:"completed_at,omitempty"`
}

// GigaChatBatches is a list wrapper for GigaChat batch metadata.
type GigaChatBatches struct {
	Data []GigaChatBatch `json:"data"`
}

// UnmarshalJSON accepts both the wrapper shape returned by some GigaChat
// environments and the root list shape documented for empty task lists.
func (batches *GigaChatBatches) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var items []GigaChatBatch
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return err
		}
		batches.Data = items
		return nil
	}

	type Alias GigaChatBatches
	var aux Alias
	if err := json.Unmarshal(trimmed, &aux); err != nil {
		return err
	}
	*batches = GigaChatBatches(aux)
	return nil
}

// GigaChatBatchInputRow is a single JSONL row accepted by GigaChat batches.
type GigaChatBatchInputRow struct {
	ID      string          `json:"id"`
	Request json.RawMessage `json:"request"`
}

// GigaChatBatchResultRow is a single JSONL row returned by GigaChat batches.
type GigaChatBatchResultRow struct {
	ID       string                       `json:"id,omitempty"`
	CustomID string                       `json:"custom_id,omitempty"`
	Response *schemas.BatchResultResponse `json:"response,omitempty"`
	Result   *schemas.BatchResultData     `json:"result,omitempty"`
	Error    *schemas.BatchResultError    `json:"error,omitempty"`
}

// # EMBEDDING TYPES

// GigaChatEmbeddingRequest is the v1 embeddings request body.
type GigaChatEmbeddingRequest struct {
	Model string                  `json:"model"`
	Input *schemas.EmbeddingInput `json:"input"`

	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams returns provider-specific passthrough fields.
func (request *GigaChatEmbeddingRequest) GetExtraParams() map[string]interface{} {
	if request == nil || request.ExtraParams == nil {
		return make(map[string]interface{}, 0)
	}
	return request.ExtraParams
}

// GigaChatEmbeddingResponse is the v1 embeddings response body.
type GigaChatEmbeddingResponse struct {
	Object string                  `json:"object"`
	Data   []GigaChatEmbeddingData `json:"data"`
	Model  string                  `json:"model"`
	Usage  *GigaChatEmbeddingUsage `json:"usage,omitempty"`
}

// GigaChatEmbeddingData is a single embedding vector returned by GigaChat.
type GigaChatEmbeddingData struct {
	Object    string                  `json:"object,omitempty"`
	Embedding []float64               `json:"embedding"`
	Index     int                     `json:"index"`
	Usage     *GigaChatEmbeddingUsage `json:"usage,omitempty"`
}

// GigaChatEmbeddingUsage describes token usage for embedding generation.
type GigaChatEmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// # COUNT TOKENS TYPES

// GigaChatCountTokensRequest is the v1 /tokens/count request body.
type GigaChatCountTokensRequest struct {
	Model       string                 `json:"model"`
	Input       []string               `json:"input"`
	ExtraParams map[string]interface{} `json:"-"`
}

// GetExtraParams returns provider-specific passthrough fields.
func (request *GigaChatCountTokensRequest) GetExtraParams() map[string]interface{} {
	if request == nil || request.ExtraParams == nil {
		return make(map[string]interface{}, 0)
	}
	return request.ExtraParams
}

// GigaChatCountTokensResponse accepts the documented SDK/API response shapes for
// /tokens/count: a root array, a {data:[...]} wrapper, or an aggregate object.
type GigaChatCountTokensResponse struct {
	Object     string                    `json:"object,omitempty"`
	Model      string                    `json:"model,omitempty"`
	Data       []GigaChatCountTokensItem `json:"data,omitempty"`
	Tokens     *int                      `json:"tokens,omitempty"`
	Characters *int                      `json:"characters,omitempty"`
	Items      []GigaChatCountTokensItem `json:"-"`
}

func (response *GigaChatCountTokensResponse) UnmarshalJSON(data []byte) error {
	var items []GigaChatCountTokensItem
	if err := json.Unmarshal(data, &items); err == nil {
		response.Items = items
		response.Data = items
		return nil
	}

	type Alias GigaChatCountTokensResponse
	var object Alias
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}

	*response = GigaChatCountTokensResponse(object)
	if len(response.Data) > 0 {
		response.Items = response.Data
		return nil
	}
	if response.Tokens != nil {
		item := GigaChatCountTokensItem{Tokens: *response.Tokens}
		if response.Characters != nil {
			item.Characters = *response.Characters
		}
		response.Items = []GigaChatCountTokensItem{item}
	}
	return nil
}

// GigaChatCountTokensItem is one token count result for one input string.
type GigaChatCountTokensItem struct {
	Tokens     int `json:"tokens"`
	Characters int `json:"characters,omitempty"`
}

// # RESPONSES TYPES

// GigaChatResponsesRequest is the v2 chat completions request body used for Bifrost Responses.
type GigaChatResponsesRequest struct {
	Model         string                         `json:"model,omitempty"`
	Messages      []GigaChatResponsesMessage     `json:"messages"`
	AssistantID   *string                        `json:"assistant_id,omitempty"`
	ToolsStateID  *string                        `json:"tools_state_id,omitempty"`
	ModelOptions  *GigaChatResponsesModelOptions `json:"model_options,omitempty"`
	FilterConfig  map[string]interface{}         `json:"filter_config,omitempty"`
	Storage       interface{}                    `json:"storage,omitempty"`
	RankerOptions map[string]interface{}         `json:"ranker_options,omitempty"`
	ToolConfig    *GigaChatResponsesToolConfig   `json:"tool_config,omitempty"`
	Tools         []GigaChatResponsesTool        `json:"tools,omitempty"`
	UserInfo      map[string]interface{}         `json:"user_info,omitempty"`
	Stream        *bool                          `json:"stream,omitempty"`
	DisableFilter *bool                          `json:"disable_filter,omitempty"`
	Flags         []string                       `json:"flags,omitempty"`
	ExtraParams   map[string]interface{}         `json:"-"`
}

// GigaChatResponsesStorage configures v2 thread storage.
type GigaChatResponsesStorage struct {
	Limit    *int                   `json:"limit,omitempty"`
	ThreadID *string                `json:"thread_id,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// GetExtraParams returns provider-specific passthrough fields.
func (request *GigaChatResponsesRequest) GetExtraParams() map[string]interface{} {
	if request == nil || request.ExtraParams == nil {
		return make(map[string]interface{}, 0)
	}
	return request.ExtraParams
}

// GigaChatResponsesModelOptions contains v2 generation controls.
type GigaChatResponsesModelOptions struct {
	Preset              *string                          `json:"preset,omitempty"`
	Temperature         *float64                         `json:"temperature,omitempty"`
	TopP                *float64                         `json:"top_p,omitempty"`
	MaxTokens           *int                             `json:"max_tokens,omitempty"`
	RepetitionPenalty   *float64                         `json:"repetition_penalty,omitempty"`
	UpdateInterval      *float64                         `json:"update_interval,omitempty"`
	UnnormalizedHistory *bool                            `json:"unnormalized_history,omitempty"`
	TopLogProbs         *int                             `json:"top_logprobs,omitempty"`
	Reasoning           *GigaChatResponsesReasoning      `json:"reasoning,omitempty"`
	ResponseFormat      *GigaChatResponsesResponseFormat `json:"response_format,omitempty"`
	ExtraParams         map[string]interface{}           `json:"-"`
}

// GigaChatResponsesReasoning contains GigaChat v2 reasoning controls.
type GigaChatResponsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// GigaChatResponsesResponseFormat contains GigaChat v2 structured output controls.
type GigaChatResponsesResponseFormat struct {
	Type   string      `json:"type"`
	Schema interface{} `json:"schema,omitempty"`
	Strict *bool       `json:"strict,omitempty"`
	Regex  *string     `json:"regex,omitempty"`
}

// GigaChatResponsesMessage is a v2 chat message.
type GigaChatResponsesMessage struct {
	Role         string                         `json:"role,omitempty"`
	MessageID    *string                        `json:"message_id,omitempty"`
	Content      []GigaChatResponsesContentPart `json:"content,omitempty"`
	ToolsStateID *string                        `json:"tools_state_id,omitempty"`
	ToolStateID  *string                        `json:"tool_state_id,omitempty"`
	FunctionCall *GigaChatResponsesFunctionCall `json:"function_call,omitempty"`
	FinishReason *string                        `json:"finish_reason,omitempty"`
	ExtraParams  map[string]interface{}         `json:"-"`
}

// GigaChatResponsesContentPart is a v2 multipart message content item.
type GigaChatResponsesContentPart struct {
	Text           *string                          `json:"text,omitempty"`
	Files          []GigaChatResponsesContentFile   `json:"files,omitempty"`
	FunctionCall   *GigaChatResponsesFunctionCall   `json:"function_call,omitempty"`
	FunctionResult *GigaChatResponsesFunctionResult `json:"function_result,omitempty"`
	InlineData     map[string]interface{}           `json:"inline_data,omitempty"`
}

// GigaChatResponsesContentFile is a v2 file reference.
type GigaChatResponsesContentFile struct {
	ID     string  `json:"id"`
	Target *string `json:"target,omitempty"`
	MIME   *string `json:"mime,omitempty"`
}

// GigaChatResponsesFunctionCall is a v2 function call content item.
type GigaChatResponsesFunctionCall struct {
	Name      string      `json:"name"`
	Arguments interface{} `json:"arguments"`
}

// GigaChatResponsesFunctionResult is a v2 function result content item.
type GigaChatResponsesFunctionResult struct {
	Name   string      `json:"name"`
	Result interface{} `json:"result"`
}

// GigaChatResponsesToolConfig controls v2 tool invocation policy.
type GigaChatResponsesToolConfig struct {
	Mode         string  `json:"mode,omitempty"`
	ToolName     *string `json:"tool_name,omitempty"`
	FunctionName *string `json:"function_name,omitempty"`
}

// GigaChatResponsesTool is a v2 tool definition.
type GigaChatResponsesTool struct {
	CodeInterpreter      map[string]interface{}          `json:"code_interpreter,omitempty"`
	ImageGenerate        map[string]interface{}          `json:"image_generate,omitempty"`
	WebSearch            *GigaChatResponsesWebSearchTool `json:"web_search,omitempty"`
	URLContentExtraction map[string]interface{}          `json:"url_content_extraction,omitempty"`
	Model3DGenerate      map[string]interface{}          `json:"model_3d_generate,omitempty"`
	Functions            *GigaChatResponsesFunctionsTool `json:"functions,omitempty"`
}

func (tool GigaChatResponsesTool) MarshalJSON() ([]byte, error) {
	fields := schemas.NewOrderedMap()
	if tool.CodeInterpreter != nil {
		fields.Set("code_interpreter", tool.CodeInterpreter)
	}
	if tool.ImageGenerate != nil {
		fields.Set("image_generate", tool.ImageGenerate)
	}
	if tool.WebSearch != nil {
		fields.Set("web_search", tool.WebSearch)
	}
	if tool.URLContentExtraction != nil {
		fields.Set("url_content_extraction", tool.URLContentExtraction)
	}
	if tool.Model3DGenerate != nil {
		fields.Set("model_3d_generate", tool.Model3DGenerate)
	}
	if tool.Functions != nil {
		fields.Set("functions", tool.Functions)
	}
	if fields.Len() == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(fields)
}

// GigaChatResponsesWebSearchTool configures GigaChat v2 web search.
type GigaChatResponsesWebSearchTool struct {
	Type    *string  `json:"type,omitempty"`
	Indexes []string `json:"indexes,omitempty"`
	Flags   []string `json:"flags,omitempty"`
}

// GigaChatResponsesFunctionsTool wraps client-defined function specifications.
type GigaChatResponsesFunctionsTool struct {
	Specifications []GigaChatResponsesFunctionSpecification `json:"specifications,omitempty"`
}

// GigaChatResponsesFunctionSpecification describes a client-defined function.
type GigaChatResponsesFunctionSpecification struct {
	Name             string                          `json:"name"`
	Description      *string                         `json:"description,omitempty"`
	Parameters       *schemas.ToolFunctionParameters `json:"parameters"`
	FewShotExamples  []map[string]interface{}        `json:"few_shot_examples,omitempty"`
	ReturnParameters map[string]interface{}          `json:"return_parameters,omitempty"`
}

// GigaChatResponsesResponse is the v2 chat completions response body used for Bifrost Responses.
type GigaChatResponsesResponse struct {
	ID                string                     `json:"id,omitempty"`
	Event             *string                    `json:"event,omitempty"`
	Object            string                     `json:"object,omitempty"`
	Created           int                        `json:"created,omitempty"`
	CreatedAt         int                        `json:"created_at,omitempty"`
	Model             string                     `json:"model,omitempty"`
	Messages          []GigaChatResponsesMessage `json:"messages,omitempty"`
	Choices           []GigaChatResponsesChoice  `json:"choices,omitempty"`
	FinishReason      *string                    `json:"finish_reason,omitempty"`
	Usage             *GigaChatChatUsage         `json:"usage,omitempty"`
	ThreadID          *string                    `json:"thread_id,omitempty"`
	MessageID         *string                    `json:"message_id,omitempty"`
	ToolsStateID      *string                    `json:"tools_state_id,omitempty"`
	ToolExecution     interface{}                `json:"tool_execution,omitempty"`
	AdditionalData    interface{}                `json:"additional_data,omitempty"`
	SystemFingerprint string                     `json:"system_fingerprint,omitempty"`
	ExtraParams       map[string]interface{}     `json:"-"`
}

// GigaChatResponsesChoice is a single v2 completion choice.
type GigaChatResponsesChoice struct {
	Index        int                       `json:"index"`
	Message      *GigaChatResponsesMessage `json:"message,omitempty"`
	Delta        *GigaChatChatStreamDelta  `json:"delta,omitempty"`
	FinishReason *string                   `json:"finish_reason,omitempty"`
	LogProbs     *schemas.BifrostLogProbs  `json:"logprobs,omitempty"`
}

// # ERROR TYPES

// GigaChatErrorResponse is the common REST API error shape used by GigaChat.
type GigaChatErrorResponse struct {
	Status           *int            `json:"status,omitempty"`
	Code             json.RawMessage `json:"code,omitempty"`
	Message          string          `json:"message,omitempty"`
	Error            string          `json:"error,omitempty"`
	ErrorDescription string          `json:"error_description,omitempty"`
}
