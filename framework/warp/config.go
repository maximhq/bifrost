package warp

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ConfigView is what the settings page renders. It can be returned whole, with
// no redaction step, because the config stores a key reference rather than a
// key.
type ConfigView struct {
	Configured              bool                  `json:"configured"`
	Enabled                 bool                  `json:"enabled"`
	Provider                schemas.ModelProvider `json:"provider"`
	Model                   string                `json:"model"`
	BaseURL                 string                `json:"base_url,omitempty"`
	APIKeyID                string                `json:"api_key_id,omitempty"`
	MaxIterations           int                   `json:"max_iterations"`
	RequestTimeoutSeconds   int                   `json:"request_timeout_seconds"`
	HistoryRetentionDays    int                   `json:"history_retention_days"`
	SystemPromptSuffix      string                `json:"system_prompt_suffix,omitempty"`
	Temperature             *float64              `json:"temperature,omitempty"`
	ReasoningEffort         string                `json:"reasoning_effort,omitempty"`
	EmbeddingProvider       schemas.ModelProvider `json:"embedding_provider"`
	EmbeddingModel          string                `json:"embedding_model"`
	EmbeddingAPIKeyID       string                `json:"embedding_api_key_id,omitempty"`
	EmbeddingDimension      int                   `json:"embedding_dimension"`
	LogVectorStoreNamespace string                `json:"log_vector_store_namespace"`
	SemanticSearchThreshold float64               `json:"semantic_search_threshold"`
	SemanticSearchLimit     int                   `json:"semantic_search_limit"`
	VectorStoreConnected    bool                  `json:"vector_store_connected"`
}

// ConfigInput is a configuration write.
type ConfigInput struct {
	Enabled  bool                  `json:"enabled"`
	Provider schemas.ModelProvider `json:"provider"`
	Model    string                `json:"model"`
	BaseURL  string                `json:"base_url,omitempty"`
	// APIKeyID names one of the provider's configured keys, or is empty for a
	// provider that needs none. It round-trips like any other field - no
	// omitted-means-unchanged special case, because there is no secret to lose.
	APIKeyID              string `json:"api_key_id,omitempty"`
	MaxIterations         int    `json:"max_iterations,omitempty"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds,omitempty"`
	HistoryRetentionDays  int    `json:"history_retention_days,omitempty"`
	SystemPromptSuffix    string `json:"system_prompt_suffix,omitempty"`
	// Temperature is a pointer so an explicit 0 (fully deterministic) round-trips
	// distinctly from "not set". A request that omits the field entirely leaves
	// this nil, same as one that sends "temperature": null.
	Temperature             *float64              `json:"temperature,omitempty"`
	ReasoningEffort         string                `json:"reasoning_effort,omitempty"`
	EmbeddingProvider       schemas.ModelProvider `json:"embedding_provider"`
	EmbeddingModel          string                `json:"embedding_model"`
	EmbeddingAPIKeyID       string                `json:"embedding_api_key_id,omitempty"`
	EmbeddingDimension      int                   `json:"embedding_dimension"`
	LogVectorStoreNamespace string                `json:"log_vector_store_namespace"`
	SemanticSearchThreshold float64               `json:"semantic_search_threshold,omitempty"`
	SemanticSearchLimit     int                   `json:"semantic_search_limit,omitempty"`

	// present records the keys the decoded request carried. See UnmarshalJSON.
	present map[string]bool
}

// UnmarshalJSON records which fields the request actually carried.
//
// Every field on this type is a value, so an omitted string and an explicit ""
// decode identically - and the two mean opposite things once a stored
// configuration exists: one says "leave it alone", the other says "clear it".
// Guessing between them is what let a draft either wipe settings it never
// mentioned or refuse to clear one it did.
//
// A ConfigInput built in Go rather than decoded has no presence set, and is
// treated as supplying only its non-zero fields. That is the conservative
// reading for an in-process caller, which has no way to express absence.
func (c *ConfigInput) UnmarshalJSON(data []byte) error {
	type plain ConfigInput
	var decoded plain
	if err := sonic.Unmarshal(data, &decoded); err != nil {
		return err
	}
	keys := map[string]sonic.NoCopyRawMessage{}
	if err := sonic.Unmarshal(data, &keys); err != nil {
		return err
	}
	*c = ConfigInput(decoded)
	c.present = make(map[string]bool, len(keys))
	for key := range keys {
		c.present[key] = true
	}
	return nil
}

// supplied reports whether the write named this field at all.
func (c *ConfigInput) supplied(field string) bool {
	if c == nil {
		return false
	}
	if c.present != nil {
		return c.present[field]
	}
	// No presence set: an in-process caller. Treat a non-zero value as supplied.
	switch field {
	case "embedding_provider":
		return c.EmbeddingProvider != ""
	case "embedding_model":
		return c.EmbeddingModel != ""
	case "embedding_api_key_id":
		return c.EmbeddingAPIKeyID != ""
	case "embedding_dimension":
		return c.EmbeddingDimension != 0
	case "log_vector_store_namespace":
		return strings.TrimSpace(c.LogVectorStoreNamespace) != ""
	case "semantic_search_threshold":
		return c.SemanticSearchThreshold != 0
	case "semantic_search_limit":
		return c.SemanticSearchLimit != 0
	}
	return false
}

// applyStoredEmbeddingSettings fills the embedding fields the write did not
// name from the stored row, producing the effective configuration.
//
// It runs before validation, not after. Validation expands omitted values into
// defaults, so a merge afterwards can never tell a field that was left out from
// one that was explicitly set to the default - and a deployment on a custom
// namespace was quietly moved back to the default by any save that did not
// restate it, orphaning every vector already indexed under it.
//
// Running first also makes change detection honest: a write that names only a
// new model becomes a complete space once the stored provider and dimension are
// filled in, so embeddingSpaceChanged sees the change, the namespace rule
// applies, and the old namespace is retired.
func applyStoredEmbeddingSettings(input *ConfigInput, previous *tables.TableWarpConfig) {
	if input == nil || previous == nil {
		return
	}
	if !input.supplied("embedding_provider") {
		input.EmbeddingProvider = schemas.ModelProvider(previous.EmbeddingProvider)
	}
	if !input.supplied("embedding_model") {
		input.EmbeddingModel = previous.EmbeddingModel
	}
	if !input.supplied("embedding_api_key_id") {
		input.EmbeddingAPIKeyID = previous.EmbeddingAPIKeyID
	}
	if !input.supplied("embedding_dimension") {
		input.EmbeddingDimension = previous.EmbeddingDimension
	}
	if !input.supplied("log_vector_store_namespace") {
		input.LogVectorStoreNamespace = previous.LogVectorStoreNamespace
	}
	if !input.supplied("semantic_search_threshold") {
		input.SemanticSearchThreshold = previous.SemanticSearchThreshold
	}
	if !input.supplied("semantic_search_limit") {
		input.SemanticSearchLimit = previous.SemanticSearchLimit
	}
}

// ConfigView returns the stored configuration for display.
//
// An unconfigured deployment gets a defaults view with Configured false rather
// than an error. The settings page needs to render its empty form, and a
// failure would make "Warp was never set up" indistinguishable from "this build
// has no Warp route" on the client.
func (s *Service) ConfigView(ctx context.Context) (ConfigView, error) {
	if s.store == nil {
		return ConfigView{}, ErrUnavailable
	}
	row, err := s.store.GetWarpConfig(ctx)
	if err != nil {
		return ConfigView{}, err
	}
	if row == nil {
		return ConfigView{
			MaxIterations:           schemas.WarpDefaultMaxIterations,
			RequestTimeoutSeconds:   schemas.WarpDefaultRequestTimeoutSeconds,
			HistoryRetentionDays:    schemas.WarpDefaultHistoryRetentionDays,
			LogVectorStoreNamespace: schemas.WarpDefaultLogVectorStoreNamespace,
			SemanticSearchThreshold: schemas.WarpDefaultSemanticSearchThreshold,
			SemanticSearchLimit:     schemas.WarpDefaultSemanticSearchLimit,
			VectorStoreConnected:    s.vectorStore != nil,
		}, nil
	}
	return s.configViewFromRow(row), nil
}

// SaveConfig validates and stores a configuration, returning the view a caller
// would get from ConfigView afterwards. Validation failures wrap
// ErrInvalidConfig; anything else is a store error.
func (s *Service) SaveConfig(ctx context.Context, input *ConfigInput) (ConfigView, error) {
	if s.store == nil {
		return ConfigView{}, ErrUnavailable
	}
	// Stored values first, then validation. Validation expands omitted fields
	// into defaults, so anything merged afterwards is merging into values that
	// are no longer distinguishable from a deliberate choice.
	previous, err := s.store.GetWarpConfig(ctx)
	if err != nil {
		return ConfigView{}, err
	}
	applyStoredEmbeddingSettings(input, previous)
	if err := ValidateConfigInput(input); err != nil {
		return ConfigView{}, err
	}
	if input.Enabled && s.vectorStore == nil {
		return ConfigView{}, ErrNoVectorStore
	}
	// Everything that can reject the save happens before anything is created in
	// the vector store. Provisioning first left a namespace behind whenever the
	// save was then rejected, and surfaced whatever the store said about a
	// dimension mismatch in place of the ErrInvalidConfig the request earned.
	// Set only when this save is the one that created the namespace, so a failing
	// write can undo its own work without touching anybody else's.
	createdNamespace := ""
	retired := retiredNamespaces(previous)
	// Compared as effective namespaces, not raw strings. A legacy row storing ""
	// or a padded value resolves to the same namespace the request does, so a
	// raw comparison saw "different" and let an embedding-space change reuse the
	// namespace it was already indexed under - mixing vectors from two
	// configurations in one place, which is exactly what this rule prevents.
	if embeddingSpaceChanged(previous, input) &&
		normalizedNamespace(previous.LogVectorStoreNamespace) == normalizedNamespace(input.LogVectorStoreNamespace) {
		return ConfigView{}, fmt.Errorf("%w: log_vector_store_namespace must change when the embedding provider, model or dimension changes", ErrInvalidConfig)
	}
	if embeddingSpaceChanged(previous, input) && s.backfillJobs != nil {
		active, activeErr := s.backfillJobs.GetInFlightSidekiqJobByKind(ctx, BackfillJobKind)
		if activeErr != nil {
			return ConfigView{}, activeErr
		}
		if active != nil {
			return ConfigView{}, ErrBackfillInProgress
		}
	}
	// Retire what was actually in use. A blank stored value means the default
	// namespace, not "no namespace", so the raw check skipped retiring the very
	// namespace a legacy row had been indexed under - and retiring "" would
	// have named nothing anyway.
	if embeddingSpaceChanged(previous, input) {
		retired = appendUnique(retired, normalizedNamespace(previous.LogVectorStoreNamespace))
	}
	if input.Enabled {
		// Still ahead of the write: a namespace that cannot be created is a save
		// that cannot work, and the config must not claim otherwise.
		//
		// createdNamespace records whether this call is the one that made it, so a
		// failing write can undo only its own work. A namespace that already
		// existed may hold vectors a previous configuration still indexes and is
		// never touched.
		created, err := ensureWarpNamespace(ctx, s.vectorStore, input.LogVectorStoreNamespace, input.EmbeddingDimension)
		if err != nil {
			return ConfigView{}, fmt.Errorf("ensure warp vector namespace: %w", err)
		}
		if created {
			createdNamespace = input.LogVectorStoreNamespace
		}
	}
	retiredJSON, err := sonic.Marshal(retired)
	if err != nil {
		return ConfigView{}, err
	}
	row := &tables.TableWarpConfig{
		Enabled:                 input.Enabled,
		Provider:                string(input.Provider),
		Model:                   input.Model,
		BaseURL:                 input.BaseURL,
		APIKeyID:                strings.TrimSpace(input.APIKeyID),
		MaxIterations:           input.MaxIterations,
		RequestTimeoutSeconds:   input.RequestTimeoutSeconds,
		HistoryRetentionDays:    input.HistoryRetentionDays,
		Temperature:             input.Temperature,
		ReasoningEffort:         input.ReasoningEffort,
		EmbeddingProvider:       string(input.EmbeddingProvider),
		EmbeddingModel:          input.EmbeddingModel,
		EmbeddingAPIKeyID:       strings.TrimSpace(input.EmbeddingAPIKeyID),
		EmbeddingDimension:      input.EmbeddingDimension,
		LogVectorStoreNamespace: input.LogVectorStoreNamespace,
		SemanticSearchThreshold: input.SemanticSearchThreshold,
		SemanticSearchLimit:     input.SemanticSearchLimit,
	}
	if len(retired) > 0 {
		value := string(retiredJSON)
		row.RetiredLogVectorStoreNamespaces = &value
	}
	if input.SystemPromptSuffix != "" {
		row.SystemPromptSuffix = &input.SystemPromptSuffix
	}
	if err := s.store.UpsertWarpConfig(ctx, row); err != nil {
		// Compensate: the namespace this save created is empty and no stored
		// config points at it, so leaving it behind is a leak that the next save
		// under a different name never reclaims. Only the one created here - a
		// pre-existing namespace belongs to whatever configuration is still live.
		//
		// A concurrent save that created the same namespace between our check and
		// our create would lose it here. That window is narrower than the
		// unconditional leak it replaces, and both saves are the same operator
		// action on one settings page.
		if createdNamespace != "" && s.vectorStore != nil {
			if cleanupErr := s.vectorStore.DeleteNamespace(ctx, createdNamespace); cleanupErr != nil {
				s.warnf("failed to remove warp vector namespace %s after a failed config save: %v", createdNamespace, cleanupErr)
			}
		}
		return ConfigView{}, err
	}
	return s.configViewFromRow(row), nil
}

// mergeOmittedEmbeddingSettings fills embedding fields the write left empty
// from the stored row.
//
// Field by field rather than all-or-nothing, so a draft that names some of them
// keeps the rest. Replacing a space is still possible: a write that names a
// value overwrites, and only an absent one falls back.
func mergeOmittedEmbeddingSettings(row, previous *tables.TableWarpConfig) {
	if previous == nil {
		return
	}
	if row.EmbeddingProvider == "" {
		row.EmbeddingProvider = previous.EmbeddingProvider
	}
	if row.EmbeddingModel == "" {
		row.EmbeddingModel = previous.EmbeddingModel
	}
	if row.EmbeddingAPIKeyID == "" {
		row.EmbeddingAPIKeyID = previous.EmbeddingAPIKeyID
	}
	if row.EmbeddingDimension == 0 {
		row.EmbeddingDimension = previous.EmbeddingDimension
	}
	if strings.TrimSpace(row.LogVectorStoreNamespace) == "" {
		row.LogVectorStoreNamespace = previous.LogVectorStoreNamespace
	}
	if row.SemanticSearchThreshold == 0 {
		row.SemanticSearchThreshold = previous.SemanticSearchThreshold
	}
	if row.SemanticSearchLimit == 0 {
		row.SemanticSearchLimit = previous.SemanticSearchLimit
	}
}

// ValidateConfigInput normalizes and checks a write in place.
//
// Completeness is only required when enabled is true: an operator saving a
// half-filled form with the toggle off is drafting, not misconfiguring, and
// rejecting that would make the form impossible to fill in over more than one
// sitting.
func ValidateConfigInput(input *ConfigInput) error {
	// Normalization writes through the pointer, so a nil input is a panic rather
	// than the ErrInvalidConfig an in-process caller expects. The PUT handler
	// always passes the address of a value, but SaveConfig is exported.
	if input == nil {
		return fmt.Errorf("%w: config input is required", ErrInvalidConfig)
	}
	input.Model = strings.TrimSpace(input.Model)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.Provider = schemas.ModelProvider(strings.TrimSpace(string(input.Provider)))
	input.EmbeddingProvider = schemas.ModelProvider(strings.TrimSpace(string(input.EmbeddingProvider)))
	input.EmbeddingModel = strings.TrimSpace(input.EmbeddingModel)
	input.EmbeddingAPIKeyID = strings.TrimSpace(input.EmbeddingAPIKeyID)
	input.LogVectorStoreNamespace = strings.TrimSpace(input.LogVectorStoreNamespace)
	if input.LogVectorStoreNamespace == "" {
		input.LogVectorStoreNamespace = schemas.WarpDefaultLogVectorStoreNamespace
	}
	if input.SemanticSearchThreshold == 0 {
		input.SemanticSearchThreshold = schemas.WarpDefaultSemanticSearchThreshold
	}
	if input.SemanticSearchLimit == 0 {
		input.SemanticSearchLimit = schemas.WarpDefaultSemanticSearchLimit
	}

	// BaseURL is handed to the Warp client's ProviderConfig verbatim, so a value
	// that is not an absolute http(s) URL would only surface on the first
	// outbound call - long after the operator has left the settings page.
	if input.BaseURL != "" {
		parsed, err := url.Parse(input.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%w: base_url must be an absolute http or https URL", ErrInvalidConfig)
		}
		// Userinfo is a credential. This column is stored unencrypted and read
		// back unredacted, deliberately, because the design is that Warp holds a
		// key reference and no secret of its own - a password in the URL would
		// quietly undo exactly that.
		if parsed.User != nil {
			return fmt.Errorf("%w: base_url must not contain credentials; use api_key_id to name a configured provider key", ErrInvalidConfig)
		}
	}

	if input.Enabled {
		if input.Provider == "" {
			return fmt.Errorf("%w: provider is required when warp is enabled", ErrInvalidConfig)
		}
		// Any non-empty string used to be accepted and stored, after which
		// IsConfigured reported the deployment as ready and the first question
		// failed at provider construction - long after the settings page said it
		// had saved. knownProviders is seeded with every standard provider at
		// package init and gains custom ones as Bifrost prepares them at startup,
		// so a value it does not recognise is one no request could have used.
		if !schemas.IsKnownProvider(string(input.Provider)) {
			return fmt.Errorf("%w: unknown provider %q", ErrInvalidConfig, input.Provider)
		}
		if input.Model == "" {
			return fmt.Errorf("%w: model is required when warp is enabled", ErrInvalidConfig)
		}
		if input.EmbeddingProvider == "" {
			return fmt.Errorf("%w: embedding_provider is required when warp is enabled", ErrInvalidConfig)
		}
		if input.EmbeddingModel == "" {
			return fmt.Errorf("%w: embedding_model is required when warp is enabled", ErrInvalidConfig)
		}
		if input.EmbeddingDimension <= 0 {
			return fmt.Errorf("%w: embedding_dimension must be positive when warp is enabled", ErrInvalidConfig)
		}
	}
	// One is refused rather than silently corrected: the loop reserves its last
	// step for answering, so a single step is spent researching and the run ends
	// out of iterations with nothing to show. Zero still means "use the default".
	if input.MaxIterations == 1 {
		return fmt.Errorf("%w: max_iterations must be at least %d, because the last step is reserved for answering; use 0 for the default", ErrInvalidConfig, schemas.WarpMinMaxIterations)
	}
	if input.MaxIterations < 0 || input.MaxIterations > schemas.WarpMaxIterationsCeiling {
		return fmt.Errorf("%w: max_iterations must be between 0 and %d", ErrInvalidConfig, schemas.WarpMaxIterationsCeiling)
	}
	if input.Temperature != nil && (*input.Temperature < schemas.WarpMinTemperature || *input.Temperature > schemas.WarpMaxTemperature) {
		return fmt.Errorf("%w: temperature must be between %g and %g", ErrInvalidConfig, schemas.WarpMinTemperature, schemas.WarpMaxTemperature)
	}
	input.ReasoningEffort = strings.TrimSpace(input.ReasoningEffort)
	if input.ReasoningEffort != "" && !schemas.IsWarpReasoningEffort(input.ReasoningEffort) {
		return fmt.Errorf("%w: reasoning_effort must be one of %s", ErrInvalidConfig, strings.Join(schemas.WarpReasoningEfforts, ", "))
	}
	if input.RequestTimeoutSeconds < 0 {
		return fmt.Errorf("%w: request_timeout_seconds must not be negative", ErrInvalidConfig)
	}
	// Rejected rather than read as "keep forever". There is no ceiling here (see
	// WarpConfig.HistoryRetentionDays), so a negative value has no plausible
	// meaning left, and silently reinterpreting it would hand an operator a
	// retention policy they did not choose.
	if input.HistoryRetentionDays < 0 {
		return fmt.Errorf("%w: history_retention_days must not be negative", ErrInvalidConfig)
	}
	if input.EmbeddingDimension < 0 {
		return fmt.Errorf("%w: embedding_dimension must not be negative", ErrInvalidConfig)
	}
	if input.SemanticSearchThreshold <= 0 || input.SemanticSearchThreshold > 1 {
		return fmt.Errorf("%w: semantic_search_threshold must be greater than 0 and at most 1", ErrInvalidConfig)
	}
	if input.SemanticSearchLimit < 1 || input.SemanticSearchLimit > schemas.WarpMaxSemanticSearchLimit {
		return fmt.Errorf("%w: semantic_search_limit must be between 1 and %d", ErrInvalidConfig, schemas.WarpMaxSemanticSearchLimit)
	}
	return nil
}

// Config returns the resolved configuration for in-process callers, or
// ErrUnavailable when Warp cannot answer: missing, disabled, or incomplete.
func (s *Service) Config(ctx context.Context) (*schemas.WarpConfig, error) {
	if s.store == nil {
		return nil, ErrUnavailable
	}
	row, err := s.store.GetWarpConfig(ctx)
	if err != nil {
		return nil, err
	}
	config := configFromRow(row)
	if !config.IsConfigured() {
		return nil, ErrUnavailable
	}
	// Deliberately no vector-store check. This is the read NewTurn makes on every
	// chat request, so refusing here failed the whole feature rather than the one
	// tool that needs a vector store - the handler maps the error to 503, so a
	// deployment without one could not ask Warp anything at all. Semantic search
	// is gated where it belongs: buildToolsFor only offers semantic_search_logs
	// when a searcher exists, so the loop simply runs with the other tools.
	// Enabling Warp without a vector store is still refused in SaveConfig, which
	// is a misconfiguration the operator can act on.
	return config, nil
}

// configViewFromRow renders a stored row for display, resolving defaults so the
// form never has to show a zero where a default applies.
func (s *Service) configViewFromRow(row *tables.TableWarpConfig) ConfigView {
	config := configFromRow(row)
	return ConfigView{
		Configured:              config.IsConfigured(),
		Enabled:                 row.Enabled,
		Provider:                schemas.ModelProvider(row.Provider),
		Model:                   row.Model,
		BaseURL:                 row.BaseURL,
		APIKeyID:                row.APIKeyID,
		MaxIterations:           config.EffectiveMaxIterations(),
		RequestTimeoutSeconds:   config.EffectiveRequestTimeoutSeconds(),
		HistoryRetentionDays:    config.EffectiveHistoryRetentionDays(),
		SystemPromptSuffix:      derefString(row.SystemPromptSuffix),
		Temperature:             config.Temperature,
		ReasoningEffort:         config.ReasoningEffort,
		EmbeddingProvider:       config.EmbeddingProvider,
		EmbeddingModel:          config.EmbeddingModel,
		EmbeddingAPIKeyID:       config.EmbeddingAPIKeyID,
		EmbeddingDimension:      config.EmbeddingDimension,
		LogVectorStoreNamespace: config.EffectiveLogVectorStoreNamespace(),
		SemanticSearchThreshold: config.EffectiveSemanticSearchThreshold(),
		SemanticSearchLimit:     config.EffectiveSemanticSearchLimit(),
		VectorStoreConnected:    s.vectorStore != nil,
	}
}

// configFromRow lifts a stored row into the shared schema type.
func configFromRow(row *tables.TableWarpConfig) *schemas.WarpConfig {
	if row == nil {
		return nil
	}
	return &schemas.WarpConfig{
		Enabled:                         row.Enabled,
		APIKeyID:                        row.APIKeyID,
		Provider:                        schemas.ModelProvider(row.Provider),
		Model:                           row.Model,
		BaseURL:                         row.BaseURL,
		MaxIterations:                   row.MaxIterations,
		RequestTimeoutSeconds:           row.RequestTimeoutSeconds,
		HistoryRetentionDays:            row.HistoryRetentionDays,
		SystemPromptSuffix:              derefString(row.SystemPromptSuffix),
		Temperature:                     row.Temperature,
		ReasoningEffort:                 row.ReasoningEffort,
		UpdatedAt:                       row.UpdatedAt,
		EmbeddingProvider:               schemas.ModelProvider(row.EmbeddingProvider),
		EmbeddingModel:                  row.EmbeddingModel,
		EmbeddingAPIKeyID:               row.EmbeddingAPIKeyID,
		EmbeddingDimension:              row.EmbeddingDimension,
		LogVectorStoreNamespace:         row.LogVectorStoreNamespace,
		SemanticSearchThreshold:         row.SemanticSearchThreshold,
		SemanticSearchLimit:             row.SemanticSearchLimit,
		RetiredLogVectorStoreNamespaces: retiredNamespaces(row),
	}
}

func embeddingSpaceChanged(row *tables.TableWarpConfig, input *ConfigInput) bool {
	if row == nil || row.EmbeddingProvider == "" || row.EmbeddingModel == "" || row.EmbeddingDimension <= 0 {
		return false
	}
	// The input has to name a complete space before it can be said to name a
	// different one. A disabled save is a draft and may legitimately omit these
	// fields, and reading those zeros as a change either rejected the draft for
	// not renaming a namespace nobody touched, or retired the live namespace
	// while writing the zeros over the config.
	if input == nil || input.EmbeddingProvider == "" || input.EmbeddingModel == "" || input.EmbeddingDimension <= 0 {
		return false
	}
	// The namespace is part of the space, not a label on it. A running backfill
	// freezes a signature that includes the effective namespace, so a
	// namespace-only rename slipped past the active-job guard, was persisted,
	// and then made the job abort on its next signature check - which is not the
	// job continuing safely, it is the job failing.
	if normalizedNamespace(row.LogVectorStoreNamespace) != normalizedNamespace(input.LogVectorStoreNamespace) {
		return true
	}
	return row.EmbeddingProvider != string(input.EmbeddingProvider) || row.EmbeddingModel != input.EmbeddingModel || row.EmbeddingDimension != input.EmbeddingDimension
}

// normalizedNamespace matches what EffectiveLogVectorStoreNamespace resolves to,
// so whitespace alone never reads as a different space.
func normalizedNamespace(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return schemas.WarpDefaultLogVectorStoreNamespace
	}
	return trimmed
}

func retiredNamespaces(row *tables.TableWarpConfig) []string {
	if row == nil || row.RetiredLogVectorStoreNamespaces == nil || *row.RetiredLogVectorStoreNamespaces == "" {
		return nil
	}
	var values []string
	if err := sonic.Unmarshal([]byte(*row.RetiredLogVectorStoreNamespaces), &values); err != nil {
		return nil
	}
	return values
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// derefString reads a *string, treating nil as empty.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
