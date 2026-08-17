package schemas

import (
	"strings"
	"time"
)

// Odin is the dashboard's question-answering agent. It reads the deployment's
// own telemetry — logs, metrics, user and virtual-key usage, model performance —
// through a read-only tool set and answers in natural language.
//
// Its model is configured separately from the gateway's provider pool on
// purpose. The pool is what the deployment *serves*; Odin is what the
// deployment *runs for itself*. Sharing them would mean a key rotation aimed at
// tenant traffic silently changes who answers dashboard questions, and would
// make Odin's spend indistinguishable from tenant spend in the very logs it
// reads.
const (
	// OdinDefaultMaxIterations bounds the agent loop: how many times Odin may
	// call tools and feed the results back before it must answer with what it
	// has. Eight covers a discovery call plus a multi-flow question (metrics ->
	// rankings -> drill-down) with room to spare; past that a loop is almost
	// always the model failing to converge rather than a genuinely deep query.
	OdinDefaultMaxIterations = 8

	// OdinMaxIterationsCeiling is the highest value an operator may configure.
	// Every iteration is a billable round trip whose cost the operator does not
	// see until the invoice, so the ceiling is a guardrail rather than a
	// technical limit.
	OdinMaxIterationsCeiling = 20

	// OdinDefaultRequestTimeoutSeconds bounds a single upstream call.
	OdinDefaultRequestTimeoutSeconds = 120
)

// OdinConfig is the deployment's Odin settings. Exactly one row exists.
type OdinConfig struct {
	Enabled bool `json:"enabled"`
	// Provider and Model name the model that runs the agent loop.
	Provider ModelProvider `json:"provider"`
	Model    string        `json:"model"`
	// APIKeyID names one of the provider's already-configured keys.
	//
	// This is a reference, not a credential: Odin reaches its model through this
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
	// MaxIterations bounds the agent loop. Zero means OdinDefaultMaxIterations.
	MaxIterations int `json:"max_iterations,omitempty"`
	// RequestTimeoutSeconds bounds a single upstream call. Zero means
	// OdinDefaultRequestTimeoutSeconds. This feeds the dedicated Odin client's
	// NetworkConfig, which is why it is stored rather than hardcoded: a local
	// model behind BaseURL can be far slower than a hosted frontier model.
	RequestTimeoutSeconds int `json:"request_timeout_seconds,omitempty"`
	// SystemPromptSuffix is appended to Odin's built-in system prompt. It is
	// additive only: operators can teach Odin about their naming conventions
	// and cost model, but cannot remove the tool-use and scoping instructions
	// the built-in prompt establishes.
	SystemPromptSuffix string `json:"system_prompt_suffix,omitempty"`

	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// EffectiveMaxIterations resolves the configured loop bound, substituting the
// default for an unset value and clamping anything above the ceiling. Callers
// use this rather than reading MaxIterations directly, so a row written before
// the ceiling existed cannot uncap the loop.
func (c *OdinConfig) EffectiveMaxIterations() int {
	if c == nil || c.MaxIterations <= 0 {
		return OdinDefaultMaxIterations
	}
	return min(c.MaxIterations, OdinMaxIterationsCeiling)
}

// EffectiveRequestTimeoutSeconds resolves the per-call timeout, substituting
// the default for an unset value.
func (c *OdinConfig) EffectiveRequestTimeoutSeconds() int {
	if c == nil || c.RequestTimeoutSeconds <= 0 {
		return OdinDefaultRequestTimeoutSeconds
	}
	return c.RequestTimeoutSeconds
}

// IsConfigured reports whether Odin has enough settings to answer a question.
// A row can exist and still be unusable — the settings page writes as the
// operator fills it in — so callers must check this rather than the row's
// presence.
//
// The key reference is deliberately not part of the test: a provider on a
// trusted network, or one using ambient credentials, needs none.
func (c *OdinConfig) IsConfigured() bool {
	return c != nil && c.Enabled && c.Provider != "" && c.Model != ""
}

// OdinUnavailableReason tells the dashboard *why* Odin cannot answer, because
// the two causes need opposite treatment in the UI: an unconfigured Odin is
// fixable by the operator and must stay visible with a link to its settings,
// while a deployment with no log store has nothing for Odin to read and no
// in-panel remedy, so the launcher is hidden entirely.
//
// Both are served as 503. Without this field the dashboard would have to guess
// from the message text, which is exactly the kind of coupling that breaks
// silently when the message is reworded.
type OdinUnavailableReason string

const (
	// OdinUnavailableNotConfigured means Odin has no usable settings, or is
	// switched off. The dashboard shows a configure prompt.
	OdinUnavailableNotConfigured OdinUnavailableReason = "not_configured"
	// OdinUnavailableNoLogStore means the deployment persists no logs. The
	// dashboard hides Odin.
	OdinUnavailableNoLogStore OdinUnavailableReason = "no_log_store"
)

// OdinUnavailableResponse is the 503 body for both reasons above.
type OdinUnavailableResponse struct {
	Reason  OdinUnavailableReason `json:"reason"`
	Message string                `json:"message"`
}

// Conversation history.
//
// A conversation is owned by whoever created it. On a deployment with
// authentication that is the user id; without one there is no identity to scope
// by, so every conversation shares a single owner and the history is common to
// the deployment. Both cases use the same column and the same query - the only
// difference is what OdinOwnerID resolves to - so there is no second code path
// that could get the scoping wrong.
const (
	// OdinGlobalOwnerID owns conversations on deployments with no user identity.
	//
	// A sentinel rather than an empty string: empty reads as "not set yet" at
	// every call site it passes through, and a scoping value that can be confused
	// with a missing one is how conversations end up visible to the wrong person.
	OdinGlobalOwnerID = "__global__"

	// OdinMaxConversationsPerOwner caps stored conversations. Odin's history is a
	// convenience, not a record of account: past this the oldest are pruned, so a
	// deployment that never cleans up cannot grow the table without bound.
	OdinMaxConversationsPerOwner = 100

	// OdinConversationTitleChars bounds the generated title.
	OdinConversationTitleChars = 80
)

// OdinConversation is one thread, without its messages.
type OdinConversation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// MessageCount lets the list render without loading every transcript.
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// OdinConversationDetail is a conversation with its full transcript.
type OdinConversationDetail struct {
	OdinConversation
	Messages []OdinStoredMessage `json:"messages"`
}

// OdinStoredMessage is one persisted turn.
type OdinStoredMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls records what Odin queried, so a reopened thread shows the same
	// provenance the live one did. Without it a restored answer looks like it
	// came from nowhere.
	ToolCalls []OdinStoredToolCall `json:"tool_calls,omitempty"`
	Error     string               `json:"error,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

// OdinStoredToolCall is the persisted trace of one tool call.
type OdinStoredToolCall struct {
	Name       string `json:"name"`
	DurationMs int64  `json:"duration_ms,omitempty"`
	Failed     bool   `json:"failed,omitempty"`
}

// OdinConversationTitle derives a thread title from its opening question.
//
// The first question is used verbatim rather than asking a model to summarise
// it: a title is worth nothing if generating it costs a round trip, and the
// question someone typed is already the best short description of the thread.
func OdinConversationTitle(question string) string {
	title := strings.TrimSpace(question)
	if title == "" {
		return "New chat"
	}
	// Collapse whitespace so a pasted multi-line question does not become a
	// multi-line row in the history list.
	title = strings.Join(strings.Fields(title), " ")
	if len(title) <= OdinConversationTitleChars {
		return title
	}
	// Prefer a word boundary so the truncation does not cut mid-word.
	trimmed := title[:OdinConversationTitleChars]
	if space := strings.LastIndex(trimmed, " "); space > OdinConversationTitleChars/2 {
		trimmed = trimmed[:space]
	}
	return trimmed + "..."
}

// OdinOwnerID resolves the owner for a caller, falling back to the shared owner
// when the deployment has no user identity.
func OdinOwnerID(userID string) string {
	if strings.TrimSpace(userID) == "" {
		return OdinGlobalOwnerID
	}
	return userID
}
