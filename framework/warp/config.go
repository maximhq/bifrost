package warp

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// ConfigView is what the settings page renders. It can be returned whole, with
// no redaction step, because the config stores a key reference rather than a
// key.
type ConfigView struct {
	Configured            bool                  `json:"configured"`
	Enabled               bool                  `json:"enabled"`
	Provider              schemas.ModelProvider `json:"provider"`
	Model                 string                `json:"model"`
	BaseURL               string                `json:"base_url,omitempty"`
	APIKeyID              string                `json:"api_key_id,omitempty"`
	MaxIterations         int                   `json:"max_iterations"`
	RequestTimeoutSeconds int                   `json:"request_timeout_seconds"`
	SystemPromptSuffix    string                `json:"system_prompt_suffix,omitempty"`
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
	SystemPromptSuffix    string `json:"system_prompt_suffix,omitempty"`
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
			MaxIterations:         schemas.WarpDefaultMaxIterations,
			RequestTimeoutSeconds: schemas.WarpDefaultRequestTimeoutSeconds,
		}, nil
	}
	return configViewFromRow(row), nil
}

// SaveConfig validates and stores a configuration, returning the view a caller
// would get from ConfigView afterwards. Validation failures wrap
// ErrInvalidConfig; anything else is a store error.
func (s *Service) SaveConfig(ctx context.Context, input *ConfigInput) (ConfigView, error) {
	if s.store == nil {
		return ConfigView{}, ErrUnavailable
	}
	if err := ValidateConfigInput(input); err != nil {
		return ConfigView{}, err
	}
	row := &tables.TableWarpConfig{
		Enabled:               input.Enabled,
		Provider:              string(input.Provider),
		Model:                 input.Model,
		BaseURL:               input.BaseURL,
		APIKeyID:              strings.TrimSpace(input.APIKeyID),
		MaxIterations:         input.MaxIterations,
		RequestTimeoutSeconds: input.RequestTimeoutSeconds,
	}
	if input.SystemPromptSuffix != "" {
		row.SystemPromptSuffix = &input.SystemPromptSuffix
	}
	if err := s.store.UpsertWarpConfig(ctx, row); err != nil {
		return ConfigView{}, err
	}
	return configViewFromRow(row), nil
}

// ValidateConfigInput normalizes and checks a write in place.
//
// Completeness is only required when enabled is true: an operator saving a
// half-filled form with the toggle off is drafting, not misconfiguring, and
// rejecting that would make the form impossible to fill in over more than one
// sitting.
func ValidateConfigInput(input *ConfigInput) error {
	input.Model = strings.TrimSpace(input.Model)
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	input.Provider = schemas.ModelProvider(strings.TrimSpace(string(input.Provider)))

	if input.Enabled {
		if input.Provider == "" {
			return fmt.Errorf("%w: provider is required when warp is enabled", ErrInvalidConfig)
		}
		if input.Model == "" {
			return fmt.Errorf("%w: model is required when warp is enabled", ErrInvalidConfig)
		}
	}
	if input.MaxIterations < 0 || input.MaxIterations > schemas.WarpMaxIterationsCeiling {
		return fmt.Errorf("%w: max_iterations must be between 0 and %d", ErrInvalidConfig, schemas.WarpMaxIterationsCeiling)
	}
	if input.RequestTimeoutSeconds < 0 {
		return fmt.Errorf("%w: request_timeout_seconds must not be negative", ErrInvalidConfig)
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
	return config, nil
}

// configViewFromRow renders a stored row for display, resolving defaults so the
// form never has to show a zero where a default applies.
func configViewFromRow(row *tables.TableWarpConfig) ConfigView {
	config := configFromRow(row)
	return ConfigView{
		Configured:            config.IsConfigured(),
		Enabled:               row.Enabled,
		Provider:              schemas.ModelProvider(row.Provider),
		Model:                 row.Model,
		BaseURL:               row.BaseURL,
		APIKeyID:              row.APIKeyID,
		MaxIterations:         config.EffectiveMaxIterations(),
		RequestTimeoutSeconds: config.EffectiveRequestTimeoutSeconds(),
		SystemPromptSuffix:    derefString(row.SystemPromptSuffix),
	}
}

// configFromRow lifts a stored row into the shared schema type.
func configFromRow(row *tables.TableWarpConfig) *schemas.WarpConfig {
	if row == nil {
		return nil
	}
	return &schemas.WarpConfig{
		Enabled:               row.Enabled,
		APIKeyID:              row.APIKeyID,
		Provider:              schemas.ModelProvider(row.Provider),
		Model:                 row.Model,
		BaseURL:               row.BaseURL,
		MaxIterations:         row.MaxIterations,
		RequestTimeoutSeconds: row.RequestTimeoutSeconds,
		SystemPromptSuffix:    derefString(row.SystemPromptSuffix),
		UpdatedAt:             row.UpdatedAt,
	}
}

// derefString reads a *string, treating nil as empty.
func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
