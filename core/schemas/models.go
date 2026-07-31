package schemas

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
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
	IsDeprecated        bool               `json:"is_deprecated,omitempty"`
	Pricing             *Pricing           `json:"pricing,omitempty"`
	TopProvider         *TopProvider       `json:"top_provider,omitempty"`
	PerRequestLimits    *PerRequestLimits  `json:"per_request_limits,omitempty"`
	SupportedParameters []string           `json:"supported_parameters,omitempty"`
	DefaultParameters   *DefaultParameters `json:"default_parameters,omitempty"`
	Reasoning           *ModelReasoning    `json:"reasoning,omitempty"`
	HuggingFaceID       *string            `json:"hugging_face_id,omitempty"`
	Description         *string            `json:"description,omitempty"`

	// AdditionalAttributes carries editorial per-model metadata stored on the
	// governance_model_pricing row (e.g. description, tags). Preserved across
	// the 24-hour pricing sync.
	AdditionalAttributes map[string]string `json:"additional_attributes,omitempty"`

	OwnedBy          *string  `json:"owned_by,omitempty"`
	SupportedMethods []string `json:"supported_methods,omitempty"`

	// ProviderExtra carries opaque provider-specific data (e.g. Anthropic capabilities)
	// through the Bifrost pipeline for integration reverse-conversion. Never serialized.
	ProviderExtra json.RawMessage `json:"-"`
}

// DeepCopy returns a copy that shares nothing with m.
//
// A plain assignment is not enough: Model is almost entirely pointers, slices
// and a map, so a copy still writes through to whatever the original aliased.
// Callers handing a Model out of a cache need this — a holder that mutates a
// shared map rewrites the cache for everyone, and one that reads it while the
// cache is replaced takes down the process on a concurrent map access.
//
// Named DeepCopy rather than Clone to match ImageUsage.DeepCopy, and because
// Clone means shallow both elsewhere in this package and in the standard
// library's slices.Clone / maps.Clone.
//
// Lives next to the struct so a new field is added to both at once.
func (m Model) DeepCopy() Model {
	out := m

	if m.CanonicalSlug != nil {
		out.CanonicalSlug = new(*m.CanonicalSlug)
	}
	if m.Name != nil {
		out.Name = new(*m.Name)
	}
	if m.NormalizedName != nil {
		out.NormalizedName = new(*m.NormalizedName)
	}
	if m.Alias != nil {
		out.Alias = new(*m.Alias)
	}
	if m.Created != nil {
		out.Created = new(*m.Created)
	}
	if m.ContextLength != nil {
		out.ContextLength = new(*m.ContextLength)
	}
	if m.MaxInputTokens != nil {
		out.MaxInputTokens = new(*m.MaxInputTokens)
	}
	if m.MaxOutputTokens != nil {
		out.MaxOutputTokens = new(*m.MaxOutputTokens)
	}
	if m.HuggingFaceID != nil {
		out.HuggingFaceID = new(*m.HuggingFaceID)
	}
	if m.Description != nil {
		out.Description = new(*m.Description)
	}
	if m.OwnedBy != nil {
		out.OwnedBy = new(*m.OwnedBy)
	}

	// Every nested struct is itself all pointers, so copying the pointee alone
	// would still share each field. Each one copies itself.
	out.Pricing = m.Pricing.DeepCopy()
	out.TopProvider = m.TopProvider.DeepCopy()
	out.PerRequestLimits = m.PerRequestLimits.DeepCopy()
	out.DefaultParameters = m.DefaultParameters.DeepCopy()
	out.Architecture = m.Architecture.DeepCopy()
	out.Reasoning = m.Reasoning.DeepCopy()

	out.SupportedParameters = slices.Clone(m.SupportedParameters)
	out.SupportedMethods = slices.Clone(m.SupportedMethods)
	out.AdditionalAttributes = maps.Clone(m.AdditionalAttributes)
	out.ProviderExtra = slices.Clone(m.ProviderExtra)

	return out
}

// HasMetadata reports whether anything beyond the identifier is known.
//
// Any single populated field is enough: providers and the datasheet describe
// models unevenly, so requiring a particular one would discard models that are
// perfectly usable. A model failing this carries nothing to act on — no context
// length to size a request against, no pricing to budget with, no capabilities
// to branch on — which is why both /v1/models and the plugin-facing lookup
// treat it as unknown rather than reporting a bare identifier.
func (m Model) HasMetadata() bool {
	return m.Name != nil ||
		m.NormalizedName != nil ||
		m.CanonicalSlug != nil ||
		m.Description != nil ||
		m.Alias != nil ||
		m.OwnedBy != nil ||
		m.Created != nil ||
		m.ContextLength != nil ||
		m.MaxInputTokens != nil ||
		m.MaxOutputTokens != nil ||
		m.Architecture != nil ||
		m.Pricing != nil ||
		m.TopProvider != nil ||
		m.PerRequestLimits != nil ||
		m.DefaultParameters != nil ||
		m.Reasoning != nil ||
		m.HuggingFaceID != nil ||
		len(m.SupportedParameters) > 0 ||
		len(m.SupportedMethods) > 0 ||
		len(m.AdditionalAttributes) > 0 ||
		m.IsDeprecated
}

type Architecture struct {
	Modality         *string  `json:"modality,omitempty"`
	Tokenizer        *string  `json:"tokenizer,omitempty"`
	InstructType     *string  `json:"instruct_type,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

// DeepCopy returns a copy sharing nothing with a, or nil for a nil receiver.
func (a *Architecture) DeepCopy() *Architecture {
	if a == nil {
		return nil
	}
	out := *a
	if a.Modality != nil {
		out.Modality = new(*a.Modality)
	}
	if a.Tokenizer != nil {
		out.Tokenizer = new(*a.Tokenizer)
	}
	if a.InstructType != nil {
		out.InstructType = new(*a.InstructType)
	}
	out.InputModalities = slices.Clone(a.InputModalities)
	out.OutputModalities = slices.Clone(a.OutputModalities)
	return &out
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

// DeepCopy returns a copy sharing nothing with p, or nil for a nil receiver.
func (p *Pricing) DeepCopy() *Pricing {
	if p == nil {
		return nil
	}
	out := *p
	if p.Prompt != nil {
		out.Prompt = new(*p.Prompt)
	}
	if p.Completion != nil {
		out.Completion = new(*p.Completion)
	}
	if p.Request != nil {
		out.Request = new(*p.Request)
	}
	if p.Image != nil {
		out.Image = new(*p.Image)
	}
	if p.WebSearch != nil {
		out.WebSearch = new(*p.WebSearch)
	}
	if p.InternalReasoning != nil {
		out.InternalReasoning = new(*p.InternalReasoning)
	}
	if p.InputCacheRead != nil {
		out.InputCacheRead = new(*p.InputCacheRead)
	}
	if p.InputCacheWrite != nil {
		out.InputCacheWrite = new(*p.InputCacheWrite)
	}
	return &out
}

type TopProvider struct {
	IsModerated         *bool `json:"is_moderated,omitempty"`
	ContextLength       *int  `json:"context_length,omitempty"`
	MaxCompletionTokens *int  `json:"max_completion_tokens,omitempty"`
}

// DeepCopy returns a copy sharing nothing with t, or nil for a nil receiver.
func (t *TopProvider) DeepCopy() *TopProvider {
	if t == nil {
		return nil
	}
	out := *t
	if t.IsModerated != nil {
		out.IsModerated = new(*t.IsModerated)
	}
	if t.ContextLength != nil {
		out.ContextLength = new(*t.ContextLength)
	}
	if t.MaxCompletionTokens != nil {
		out.MaxCompletionTokens = new(*t.MaxCompletionTokens)
	}
	return &out
}

type PerRequestLimits struct {
	PromptTokens     *int `json:"prompt_tokens,omitempty"`
	CompletionTokens *int `json:"completion_tokens,omitempty"`
}

// DeepCopy returns a copy sharing nothing with l, or nil for a nil receiver.
func (l *PerRequestLimits) DeepCopy() *PerRequestLimits {
	if l == nil {
		return nil
	}
	out := *l
	if l.PromptTokens != nil {
		out.PromptTokens = new(*l.PromptTokens)
	}
	if l.CompletionTokens != nil {
		out.CompletionTokens = new(*l.CompletionTokens)
	}
	return &out
}

type DefaultParameters struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"top_p,omitempty"`
	FrequencyPenalty *float64 `json:"frequency_penalty,omitempty"`
}

// DeepCopy returns a copy sharing nothing with d, or nil for a nil receiver.
func (d *DefaultParameters) DeepCopy() *DefaultParameters {
	if d == nil {
		return nil
	}
	out := *d
	if d.Temperature != nil {
		out.Temperature = new(*d.Temperature)
	}
	if d.TopP != nil {
		out.TopP = new(*d.TopP)
	}
	if d.FrequencyPenalty != nil {
		out.FrequencyPenalty = new(*d.FrequencyPenalty)
	}
	return &out
}

// ModelReasoning describes a model's reasoning capabilities as advertised by
// the provider's list-models API (e.g. OpenRouter's per-model `reasoning`
// object). All fields are optional — providers omit `supported_efforts` /
// `default_effort` for models whose reasoning has no selectable effort level.
type ModelReasoning struct {
	Mandatory        *bool    `json:"mandatory,omitempty"`
	DefaultEnabled   *bool    `json:"default_enabled,omitempty"`
	SupportedEfforts []string `json:"supported_efforts,omitempty"`
	DefaultEffort    *string  `json:"default_effort,omitempty"`
}

// DeepCopy returns a copy sharing nothing with r, or nil for a nil receiver.
func (r *ModelReasoning) DeepCopy() *ModelReasoning {
	if r == nil {
		return nil
	}
	out := *r
	if r.Mandatory != nil {
		out.Mandatory = new(*r.Mandatory)
	}
	if r.DefaultEnabled != nil {
		out.DefaultEnabled = new(*r.DefaultEnabled)
	}
	if r.DefaultEffort != nil {
		out.DefaultEffort = new(*r.DefaultEffort)
	}
	out.SupportedEfforts = slices.Clone(r.SupportedEfforts)
	return &out
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
