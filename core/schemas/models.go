package schemas

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
)

// DefaultPageSize is the default page size for listing models
const DefaultPageSize = 1000

// MaxPaginationRequests is the maximum number of pagination requests to make
const MaxPaginationRequests = 20

// Structure to collect results from goroutines
type ListModelsByKeyResult struct {
	Response *BifrostListModelsResponse
	Err      *BifrostError
	KeyID    string
}

// KeyStatus represents the status of model listing for a specific key
type KeyStatus struct {
	KeyID    string        `json:"key_id"`   // Empty for keyless providers
	Status   KeyStatusType `json:"status"`   // "success", "failed"
	Provider ModelProvider `json:"provider"` // Always populated
	Error    *BifrostError `json:"error,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for KeyStatus to prevent
// circular reference: KeyStatus.Error → BifrostError.ExtraFields.KeyStatuses → KeyStatus.
func (k KeyStatus) MarshalJSON() ([]byte, error) {
	type Alias KeyStatus
	alias := Alias(k)
	if alias.Error != nil {
		errCopy := *alias.Error
		errCopy.ExtraFields.KeyStatuses = nil
		alias.Error = &errCopy
	}
	return MarshalSorted(alias)
}

type BifrostListModelsRequest struct {
	Provider ModelProvider `json:"provider"`

	PageSize int `json:"page_size"`

	// PageToken: Token received from previous request to retrieve next page
	PageToken string `json:"page_token"`

	// Unfiltered: If true, the response will include all models for the provider, regardless of the allowed models (internal bifrost use only, not sent to the provider)
	Unfiltered bool `json:"-"`

	// KeyID: If non-nil, scope the call to a single key (matched by Key.ID).
	// Lets callers cache list-models output per-key for fine-grained
	// invalidation. Internal bifrost use only; not sent to the provider.
	//
	// Matching runs against the already-filtered set of supported keys for the
	// provider — keys that are disabled (Enabled == false) or fail validation
	// are excluded before the lookup, so a KeyID referring to such a key
	// produces the same "no key found" error as a KeyID that does not exist
	// at all. Callers needing to distinguish those cases must check the raw
	// account configuration themselves.
	KeyID *string `json:"-"`

	// ExtraParams: Additional provider-specific query parameters
	// This allows for flexibility to pass any custom parameters that specific providers might support
	ExtraParams map[string]interface{} `json:"-"`
}

type BifrostListModelsResponse struct {
	Data          []Model                    `json:"data"`
	ExtraFields   BifrostResponseExtraFields `json:"extra_fields"`
	NextPageToken string                     `json:"next_page_token,omitempty"` // Token to retrieve next page

	// Key-level status tracking for multi-key providers
	KeyStatuses []KeyStatus `json:"key_statuses,omitempty"`

	// Anthropic specific fields
	FirstID *string `json:"-"`
	LastID  *string `json:"-"`
	HasMore *bool   `json:"-"`
}

// ApplyPagination applies offset-based pagination to a BifrostListModelsResponse.
// Uses opaque tokens with LastID validation to ensure cursor integrity.
// Returns the paginated response with properly set NextPageToken.
func (response *BifrostListModelsResponse) ApplyPagination(pageSize int, pageToken string) *BifrostListModelsResponse {
	if response == nil {
		return nil
	}

	totalItems := len(response.Data)

	if pageSize <= 0 {
		return response
	}

	cursor := decodePaginationCursor(pageToken)
	offset := cursor.Offset

	// Validate cursor integrity if LastID is present
	if cursor.LastID != "" && !validatePaginationCursor(cursor, response.Data) {
		// Invalid cursor: reset to beginning
		offset = 0
	}

	if offset >= totalItems {
		// Return empty page, no next token
		return &BifrostListModelsResponse{
			Data:          []Model{},
			ExtraFields:   response.ExtraFields,
			NextPageToken: "",
			KeyStatuses:   response.KeyStatuses,
		}
	}

	endIndex := offset + pageSize
	if endIndex > totalItems {
		endIndex = totalItems
	}

	paginatedData := response.Data[offset:endIndex]

	paginatedResponse := &BifrostListModelsResponse{
		Data:        paginatedData,
		ExtraFields: response.ExtraFields,
		KeyStatuses: response.KeyStatuses,
	}

	if endIndex < totalItems {
		// Get the last item ID for cursor validation
		var lastID string
		if len(paginatedData) > 0 {
			lastID = paginatedData[len(paginatedData)-1].ID
		}

		nextToken, err := encodePaginationCursor(endIndex, lastID)
		if err == nil {
			paginatedResponse.NextPageToken = nextToken
		}
	} else {
		paginatedResponse.NextPageToken = ""
	}

	return paginatedResponse
}

type Model struct {
	ID                  string             `json:"id"`
	CanonicalSlug       *string            `json:"canonical_slug,omitempty"`
	Name                *string            `json:"name,omitempty"`
	NormalizedName      *string            `json:"normalized_name,omitempty"` // Human-readable name derived from the datasheet base_model (e.g. "Claude Sonnet 4.5")
	Alias               *string            `json:"alias,omitempty"`           // Provider API identifier this model alias maps to (e.g. Azure deployment name, Bedrock ARN)
	Created             *int64             `json:"created,omitempty"`
	ContextLength       *int               `json:"context_length,omitempty"`
	MaxInputTokens      *int               `json:"max_input_tokens,omitempty"`
	MaxOutputTokens     *int               `json:"max_output_tokens,omitempty"`
	Architecture        *Architecture      `json:"architecture,omitempty"`
	Pricing             *Pricing           `json:"pricing,omitempty"`
	TopProvider         *TopProvider       `json:"top_provider,omitempty"`
	PerRequestLimits    *PerRequestLimits  `json:"per_request_limits,omitempty"`
	SupportedParameters []string           `json:"supported_parameters,omitempty"`
	DefaultParameters   *DefaultParameters `json:"default_parameters,omitempty"`
	HuggingFaceID       *string            `json:"hugging_face_id,omitempty"`
	Description         *string            `json:"description,omitempty"`

	// AdditionalAttributes carries editorial per-model metadata stored on the
	// governance_model_pricing row (e.g. description, tags). Preserved across
	// the 24-hour pricing sync.
	AdditionalAttributes map[string]string `json:"additional_attributes,omitempty"`

	OwnedBy          *string  `json:"owned_by,omitempty"`
	SupportedMethods []string `json:"supported_methods,omitempty"`

	RawModelJSON json.RawMessage `json:"-"`

	// ProviderExtra carries opaque provider-specific data (e.g. Anthropic capabilities)
	// through the Bifrost pipeline for integration reverse-conversion. Never serialized.
	ProviderExtra json.RawMessage `json:"-"`
}

type modelAlias Model

type modelUnmarshalAlias struct {
	modelAlias
	ContextWindow *int `json:"context_window,omitempty"`
}

var nestedModelJSONKeys = map[string]struct{}{
	"architecture":       {},
	"pricing":            {},
	"top_provider":       {},
	"per_request_limits": {},
	"default_parameters": {},
}

func nilIfZeroStruct[T any](value *T) *T {
	if value == nil {
		return nil
	}
	if reflect.ValueOf(*value).IsZero() {
		return nil
	}
	return value
}

func isJSONObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal(raw, &value) == nil
}

func isEmptyJSONObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return len(value) == 0
}

func mergeJSONObject(base, overlay json.RawMessage) (json.RawMessage, bool) {
	var baseMap map[string]json.RawMessage
	if err := json.Unmarshal(base, &baseMap); err != nil {
		return nil, false
	}

	var overlayMap map[string]json.RawMessage
	if err := json.Unmarshal(overlay, &overlayMap); err != nil {
		return nil, false
	}

	for key, value := range overlayMap {
		if existing, ok := baseMap[key]; ok && isJSONObject(existing) && isJSONObject(value) {
			if merged, ok := mergeJSONObject(existing, value); ok {
				baseMap[key] = merged
				continue
			}
		}
		baseMap[key] = value
	}

	merged, err := json.Marshal(baseMap)
	if err != nil {
		return nil, false
	}
	return merged, true
}

func (m *Model) UnmarshalJSON(data []byte) error {
	var decoded modelUnmarshalAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	result := Model(decoded.modelAlias)
	if result.ContextLength == nil && decoded.ContextWindow != nil {
		result.ContextLength = decoded.ContextWindow
	}
	result.Architecture = nilIfZeroStruct(result.Architecture)
	result.Pricing = nilIfZeroStruct(result.Pricing)
	result.TopProvider = nilIfZeroStruct(result.TopProvider)
	result.PerRequestLimits = nilIfZeroStruct(result.PerRequestLimits)
	result.DefaultParameters = nilIfZeroStruct(result.DefaultParameters)
	if len(data) > 0 {
		result.RawModelJSON = append(json.RawMessage(nil), data...)
	}

	*m = result
	return nil
}

func (m Model) MarshalJSON() ([]byte, error) {
	alias := modelAlias(m)
	if len(m.RawModelJSON) == 0 {
		return json.Marshal(alias)
	}

	merged := map[string]json.RawMessage{}
	if err := json.Unmarshal(m.RawModelJSON, &merged); err != nil {
		return json.Marshal(alias)
	}

	overlayBytes, err := json.Marshal(alias)
	if err != nil {
		return nil, err
	}

	var overlay map[string]json.RawMessage
	if err := json.Unmarshal(overlayBytes, &overlay); err != nil {
		return nil, err
	}

	for key, value := range overlay {
		if _, ok := nestedModelJSONKeys[key]; ok {
			if existing, hasExisting := merged[key]; hasExisting {
				if isEmptyJSONObject(value) {
					continue
				}
				if mergedValue, ok := mergeJSONObject(existing, value); ok {
					merged[key] = mergedValue
					continue
				}
			}
		}
		merged[key] = value
	}

	return json.Marshal(merged)
}

type Architecture struct {
	Modality         *string  `json:"modality,omitempty"`
	Tokenizer        *string  `json:"tokenizer,omitempty"`
	InstructType     *string  `json:"instruct_type,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

type Pricing struct {
	Prompt            *string `json:"prompt,omitempty"`
	Completion        *string `json:"completion,omitempty"`
	Request           *string `json:"request,omitempty"`
	Image             *string `json:"image,omitempty"`
	WebSearch         *string `json:"web_search,omitempty"`
	InternalReasoning *string `json:"internal_reasoning,omitempty"`
	InputCacheRead    *string `json:"input_cache_read,omitempty"`
	InputCacheWrite   *string `json:"input_cache_write,omitempty"`
}

type TopProvider struct {
	IsModerated         *bool `json:"is_moderated,omitempty"`
	ContextLength       *int  `json:"context_length,omitempty"`
	MaxCompletionTokens *int  `json:"max_completion_tokens,omitempty"`
}

type PerRequestLimits struct {
	PromptTokens     *int `json:"prompt_tokens,omitempty"`
	CompletionTokens *int `json:"completion_tokens,omitempty"`
}

type DefaultParameters struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
}

// paginationCursor represents the internal cursor structure for pagination.
type paginationCursor struct {
	Offset int    `json:"o"`
	LastID string `json:"l,omitempty"`
}

// encodePaginationCursor creates an opaque base64-encoded page token from cursor data.
// Returns empty string if offset is 0 or negative.
func encodePaginationCursor(offset int, lastID string) (string, error) {
	if offset <= 0 {
		return "", nil
	}

	cursor := paginationCursor{
		Offset: offset,
		LastID: lastID,
	}

	jsonData, err := MarshalSorted(cursor)
	if err != nil {
		return "", fmt.Errorf("failed to marshal pagination cursor: %w", err)
	}

	// Use URL-safe base64 encoding without padding for opaque token
	encoded := base64.RawURLEncoding.EncodeToString(jsonData)
	return encoded, nil
}

// decodePaginationCursor extracts cursor data from an opaque base64-encoded page token.
// Returns cursor with 0 offset for empty or invalid tokens.
func decodePaginationCursor(token string) paginationCursor {
	if token == "" {
		return paginationCursor{}
	}

	// Decode base64
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return paginationCursor{}
	}

	var cursor paginationCursor
	if err := Unmarshal(decoded, &cursor); err != nil {
		return paginationCursor{}
	}

	if cursor.Offset < 0 {
		return paginationCursor{}
	}

	return cursor
}

// validatePaginationCursor validates that the cursor matches the expected position in the data.
// Returns true if the cursor is valid, false otherwise.
func validatePaginationCursor(cursor paginationCursor, data []Model) bool {
	if cursor.LastID == "" {
		return true
	}

	if cursor.Offset <= 0 || cursor.Offset > len(data) {
		return false
	}

	prevIndex := cursor.Offset - 1
	if prevIndex >= 0 && prevIndex < len(data) {
		return data[prevIndex].ID == cursor.LastID
	}

	return true
}
