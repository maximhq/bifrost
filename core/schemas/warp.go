package schemas

import "time"

// Warp is the dashboard's question-answering agent. It reads the deployment's
// own telemetry — logs, metrics, user and virtual-key usage, model performance —
// through a read-only tool set and answers in natural language.
//
// Its model is configured separately from the gateway's provider pool on
// purpose. The pool is what the deployment *serves*; Warp is what the
// deployment *runs for itself*. Sharing them would mean a key rotation aimed at
// tenant traffic silently changes who answers dashboard questions, and would
// make Warp's spend indistinguishable from tenant spend in the very logs it
// reads.
const (
	// WarpDefaultMaxIterations bounds the agent loop: how many times Warp may
	// call tools and feed the results back before it must answer with what it
	// has. Eight covers a discovery call plus a multi-flow question (metrics ->
	// rankings -> drill-down) with room to spare; past that a loop is almost
	// always the model failing to converge rather than a genuinely deep query.
	WarpDefaultMaxIterations = 8

	// WarpMaxIterationsCeiling is the highest value an operator may configure.
	// Every iteration is a billable round trip whose cost the operator does not
	// see until the invoice, so the ceiling is a guardrail rather than a
	// technical limit.
	WarpMaxIterationsCeiling = 20

	// WarpDefaultRequestTimeoutSeconds bounds a single upstream call.
	WarpDefaultRequestTimeoutSeconds = 120
)

// WarpConfig is the deployment's Warp settings. Exactly one row exists.
type WarpConfig struct {
	Enabled bool `json:"enabled"`
	// Provider and Model name the model that runs the agent loop.
	Provider ModelProvider `json:"provider"`
	Model    string        `json:"model"`
	// APIKeyID names one of the provider's already-configured keys.
	//
	// This is a reference, not a credential: Warp reaches its model through this
	// Bifrost, which resolves the id against its own key pool. Storing a
	// reference rather than a secret is what lets this whole type skip
	// encryption at rest, redaction on read, and the "was the key omitted or
	// cleared?" ambiguity a write-only secret field forces on every caller.
	//
	// Empty is valid and common: a provider on a trusted network, or one using
	// ambient IAM credentials, needs no key at all.
	APIKeyID string `json:"api_key_id,omitempty"`
	// BaseURL overrides the provider's default endpoint. Required for
	// self-hosted and proxied deployments, empty otherwise.
	BaseURL string `json:"base_url,omitempty"`
	// MaxIterations bounds the agent loop. Zero means WarpDefaultMaxIterations.
	MaxIterations int `json:"max_iterations,omitempty"`
	// RequestTimeoutSeconds bounds a single upstream call. Zero means
	// WarpDefaultRequestTimeoutSeconds. This feeds the dedicated Warp client's
	// NetworkConfig, which is why it is stored rather than hardcoded: a local
	// model behind BaseURL can be far slower than a hosted frontier model.
	RequestTimeoutSeconds int `json:"request_timeout_seconds,omitempty"`
	// SystemPromptSuffix is appended to Warp's built-in system prompt. It is
	// additive only: operators can teach Warp about their naming conventions
	// and cost model, but cannot remove the tool-use and scoping instructions
	// the built-in prompt establishes.
	SystemPromptSuffix string `json:"system_prompt_suffix,omitempty"`

	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// EffectiveMaxIterations resolves the configured loop bound, substituting the
// default for an unset value and clamping anything above the ceiling. Callers
// use this rather than reading MaxIterations directly, so a row written before
// the ceiling existed cannot uncap the loop.
func (c *WarpConfig) EffectiveMaxIterations() int {
	if c == nil || c.MaxIterations <= 0 {
		return WarpDefaultMaxIterations
	}
	return min(c.MaxIterations, WarpMaxIterationsCeiling)
}

// EffectiveRequestTimeoutSeconds resolves the per-call timeout, substituting
// the default for an unset value.
func (c *WarpConfig) EffectiveRequestTimeoutSeconds() int {
	if c == nil || c.RequestTimeoutSeconds <= 0 {
		return WarpDefaultRequestTimeoutSeconds
	}
	return c.RequestTimeoutSeconds
}

// IsConfigured reports whether Warp has enough settings to answer a question.
// A row can exist and still be unusable — the settings page writes as the
// operator fills it in — so callers must check this rather than the row's
// presence.
//
// The key reference is deliberately not part of the test: a provider on a
// trusted network, or one using ambient credentials, needs none.
func (c *WarpConfig) IsConfigured() bool {
	return c != nil && c.Enabled && c.Provider != "" && c.Model != ""
}

// WarpUnavailableReason tells the dashboard *why* Warp cannot answer, because
// the two causes need opposite treatment in the UI: an unconfigured Warp is
// fixable by the operator and must stay visible with a link to its settings,
// while a deployment with no log store has nothing for Warp to read and no
// in-panel remedy, so the launcher is hidden entirely.
//
// Both are served as 503. Without this field the dashboard would have to guess
// from the message text, which is exactly the kind of coupling that breaks
// silently when the message is reworded.
type WarpUnavailableReason string

const (
	// WarpUnavailableNotConfigured means Warp has no usable settings, or is
	// switched off. The dashboard shows a configure prompt.
	WarpUnavailableNotConfigured WarpUnavailableReason = "not_configured"
	// WarpUnavailableNoLogStore means the deployment persists no logs. The
	// dashboard hides Warp.
	WarpUnavailableNoLogStore WarpUnavailableReason = "no_log_store"
)

// WarpUnavailableResponse is the 503 body for both reasons above.
type WarpUnavailableResponse struct {
	Reason  WarpUnavailableReason `json:"reason"`
	Message string                `json:"message"`
}
