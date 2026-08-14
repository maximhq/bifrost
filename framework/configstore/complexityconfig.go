package configstore

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

// Default legacy complexity tier boundaries keep omitted pre-semantic config
// compatible with the dormant lexical analyzer.
const (
	DefaultComplexitySimpleMediumBoundary  = 0.20
	DefaultComplexityMediumComplexBoundary = 0.40
	// DefaultComplexityComplexReasoningBoundary is the third boundary the
	// pre-semantic analyzer required. Nothing here reads it: it exists so the
	// persisted row keeps a value a rolled-back Bifrost will accept, and it
	// matches what that Bifrost ships as its own default.
	DefaultComplexityComplexReasoningBoundary = 0.60
)

// legacyComplexReasoningBoundary returns a third boundary that satisfies the
// pre-semantic validation rule 0 < simple_medium < medium_complex <
// complex_reasoning < 1.
//
// The released default is used whenever it fits, so a rolled-back Bifrost
// behaves as it would on a fresh install. It does not fit when an operator has
// raised medium_complex above it, and a persisted value that fails the old
// validator is exactly the breakage this exists to prevent, so the midpoint of
// the remaining range is used instead.
func legacyComplexReasoningBoundary(mediumComplex float64) float64 {
	if DefaultComplexityComplexReasoningBoundary > mediumComplex &&
		DefaultComplexityComplexReasoningBoundary < 1 {
		return DefaultComplexityComplexReasoningBoundary
	}
	return mediumComplex + (1-mediumComplex)/2
}

// ComplexityTierBoundaries contains deprecated lexical score thresholds.
// Semantic classification ignores these values, but they remain accepted for
// compatibility with existing persisted and file-backed configurations.
type ComplexityTierBoundaries struct {
	SimpleMedium  float64 `json:"simple_medium"`
	MediumComplex float64 `json:"medium_complex"`
}

// DefaultComplexityTierBoundaries returns the legacy lexical thresholds used
// when an older config omits the now-optional boundary block.
func DefaultComplexityTierBoundaries() ComplexityTierBoundaries {
	return ComplexityTierBoundaries{
		SimpleMedium:  DefaultComplexitySimpleMediumBoundary,
		MediumComplex: DefaultComplexityMediumComplexBoundary,
	}
}

// Validate checks that tier boundaries are ordered and inside the analyzer score range.
func (b *ComplexityTierBoundaries) Validate() error {
	if b == nil {
		return nil
	}
	if !(0 < b.SimpleMedium &&
		b.SimpleMedium < b.MediumComplex &&
		b.MediumComplex < 1) {
		return fmt.Errorf(
			"tier boundaries must satisfy 0 < simple_medium (%.4f) < medium_complex (%.4f) < 1",
			b.SimpleMedium, b.MediumComplex,
		)
	}
	return nil
}

// ComplexityEditableKeywordConfig contains the user-editable per-tier lists.
// Entries are reference phrases for the semantic classifier, which embeds them
// as exemplars.
//
// These are stored in the semantic row and belong to the semantic classifier
// alone. The dormant lexical matcher keeps its own keyword lists in the analyzer
// row and never sees these; the two were the same field once and are not any
// longer.
type ComplexityEditableKeywordConfig struct {
	SimpleKeywords  []string `json:"simple_keywords"`
	MediumKeywords  []string `json:"medium_keywords"`
	ComplexKeywords []string `json:"complex_keywords"`
}

type legacyComplexityEditableKeywordConfig struct {
	CodeKeywords      []string `json:"code_keywords"`
	ReasoningKeywords []string `json:"reasoning_keywords"`
	TechnicalKeywords []string `json:"technical_keywords"`
	SimpleKeywords    []string `json:"simple_keywords"`
}

// UnmarshalJSON accepts the canonical three-list shape and the deprecated
// four-list shape. Runtime and API callers always receive the canonical shape.
func (c *ComplexityEditableKeywordConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	allowed := map[string]struct{}{
		"simple_keywords":    {},
		"medium_keywords":    {},
		"complex_keywords":   {},
		"code_keywords":      {},
		"reasoning_keywords": {},
		"technical_keywords": {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown complexity keyword field %q", field)
		}
	}

	hasCanonical := hasAnyComplexityField(fields, "medium_keywords", "complex_keywords")
	hasLegacy := hasAnyComplexityField(fields, "code_keywords", "reasoning_keywords", "technical_keywords")
	if hasCanonical && hasLegacy {
		return fmt.Errorf("complexity keyword config cannot mix canonical and legacy fields")
	}

	if hasLegacy {
		var legacy legacyComplexityEditableKeywordConfig
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		*c = ComplexityEditableKeywordConfig{
			SimpleKeywords:  legacy.SimpleKeywords,
			MediumKeywords:  mergeComplexityKeywordLists(legacy.CodeKeywords, legacy.TechnicalKeywords),
			ComplexKeywords: legacy.ReasoningKeywords,
		}
		return nil
	}

	type canonicalComplexityEditableKeywordConfig ComplexityEditableKeywordConfig
	var canonical canonicalComplexityEditableKeywordConfig
	if err := json.Unmarshal(data, &canonical); err != nil {
		return err
	}
	*c = ComplexityEditableKeywordConfig(canonical)
	return nil
}

func hasAnyComplexityField(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if _, ok := fields[name]; ok {
			return true
		}
	}
	return false
}

// Vector store selection modes for exemplar embeddings. "embedded" (the
// default) uses the built-in chromem store; "vector_store" uses the vector
// store configured for Bifrost, falling back to the embedded store when none
// is configured.
const (
	ComplexitySemanticVectorStoreEmbedded   = "embedded"
	ComplexitySemanticVectorStoreConfigured = "vector_store"
)

// MaxComplexitySemanticPhraseCharacters bounds one exemplar's input size.
// The number of exemplars is intentionally unrestricted.
const MaxComplexitySemanticPhraseCharacters = 2000

// Semantic message-history bounds. The ceiling keeps one classification
// embedding cheap and bounded. The dormant lexical analyzer happens to scan a
// conversation window of the same depth; the two are not wired together.
const (
	DefaultComplexitySemanticMessageHistoryCount = 1
	MaxComplexitySemanticMessageHistoryCount     = 10
)

// DefaultComplexitySemanticTimeout bounds per-request embedding generation.
const DefaultComplexitySemanticTimeout = 1500 * time.Millisecond

// ComplexitySemanticConfig configures the embedding-based complexity
// classifier. A non-nil value enables semantic classification. The classifier
// embeds the analyzer's shared per-tier keyword lists as its exemplars; there
// is no separate exemplar storage.
type ComplexitySemanticConfig struct {
	Provider       schemas.ModelProvider `json:"provider"`
	EmbeddingModel string                `json:"embedding_model"`
	Timeout        time.Duration         `json:"timeout,omitempty"`
	// MinSimilarity is the floor a nearest-exemplar match must clear before its
	// tier is used. Without it the nearest exemplar always wins, however
	// unrelated the request is, and semantic classification could never abstain.
	// This is the only way it abstains. A match below the floor is treated
	// exactly like an unavailable classifier: no tier is published and the
	// request is recorded as "skipped". Zero (the default) disables the floor
	// and restores "nearest exemplar always wins".
	//
	// The value is compared against the VectorStore backend's own similarity
	// score, and those scales are not identical: chromem, Qdrant, Pinecone, and
	// Redis report raw cosine similarity, while Weaviate reports certainty
	// ((cosine+1)/2). Retune this when switching backends.
	MinSimilarity float64 `json:"min_similarity,omitempty"`
	// MessageHistoryCount is how many of the most recent user messages are
	// combined into the text that gets embedded, oldest first. 1 (the default)
	// embeds only the latest message. Raising it lets a short follow-up ("and
	// make it faster") inherit the intent of the turns before it, at the cost of
	// diluting the latest message and embedding more input tokens per request.
	//
	// Only user turns are counted; system prompts and assistant replies are
	// never embedded. Requests with fewer available turns embed what they have.
	MessageHistoryCount int    `json:"message_history_count,omitempty"`
	CountTowardBudgets  bool   `json:"count_toward_budgets,omitempty"`
	VectorStore         string `json:"vector_store,omitempty"`
	// Fallback names what answers when semantic classification produces no
	// tier: "none" (the default) records the request as skipped, "llm" asks
	// the analyzer's llm block. The field lives here rather than at the top
	// level because the fallback is meaningless without a primary — the LLM
	// classifier only ever runs after a semantic non-answer.
	//
	// Session note: an LLM-classified turn carries no similarity score, so
	// with a positive session switch_min_similarity it can never move a
	// session tier (except through always_allow_escalation). That is
	// deliberate: the LLM speaks exactly on the turns semantic was least
	// confident about, which are the wrong turns to let re-pin a session.
	Fallback string `json:"fallback,omitempty"`
}

// UnmarshalJSON accepts Timeout as a duration string ("500ms") or a JSON number
// (milliseconds). It rejects unknown fields so unshipped semantic-only settings
// cannot be silently accepted through config.json or the management API.
func (c *ComplexitySemanticConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"provider":              {},
		"embedding_model":       {},
		"timeout":               {},
		"min_similarity":        {},
		"message_history_count": {},
		"count_toward_budgets":  {},
		"vector_store":          {},
		"fallback":              {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown semantic complexity field %q", field)
		}
	}

	// alias suppresses ComplexitySemanticConfig's UnmarshalJSON to avoid
	// infinite recursion. The outer Timeout (json.RawMessage) shadows
	// alias.Timeout because the json package picks the shallower field.
	type alias ComplexitySemanticConfig
	aux := &struct {
		Timeout json.RawMessage `json:"timeout,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.Timeout) == 0 || string(aux.Timeout) == "null" {
		return nil
	}

	var s string
	if err := json.Unmarshal(aux.Timeout, &s); err == nil {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("failed to parse semantic timeout duration string %q: %w", s, err)
		}
		c.Timeout = d
	} else {
		var ms float64
		if err := json.Unmarshal(aux.Timeout, &ms); err != nil {
			return fmt.Errorf("unsupported semantic timeout value: %s", string(aux.Timeout))
		}
		c.Timeout = time.Duration(ms * float64(time.Millisecond))
	}
	if c.Timeout < 0 {
		return fmt.Errorf("semantic timeout must be non-negative, got %v", c.Timeout)
	}
	return nil
}

// MarshalJSON writes Timeout as a duration string so persisted configs decode
// back to the same value (the default int encoding is nanoseconds, which the
// millisecond-number decode path would misread).
func (c ComplexitySemanticConfig) MarshalJSON() ([]byte, error) {
	type alias ComplexitySemanticConfig
	var timeout string
	if c.Timeout != 0 {
		timeout = c.Timeout.String()
	}
	return json.Marshal(struct {
		Timeout string `json:"timeout,omitempty"`
		alias
	}{
		Timeout: timeout,
		alias:   alias(c),
	})
}

// normalized returns a canonical deep copy with defaults applied.
func (c *ComplexitySemanticConfig) normalized() *ComplexitySemanticConfig {
	if c == nil {
		return nil
	}
	out := &ComplexitySemanticConfig{
		Provider:            schemas.ModelProvider(strings.ToLower(strings.TrimSpace(string(c.Provider)))),
		EmbeddingModel:      strings.TrimSpace(c.EmbeddingModel),
		Timeout:             c.Timeout,
		MinSimilarity:       c.MinSimilarity,
		MessageHistoryCount: c.MessageHistoryCount,
		CountTowardBudgets:  c.CountTowardBudgets,
		VectorStore:         strings.ToLower(strings.TrimSpace(c.VectorStore)),
		Fallback:            strings.ToLower(strings.TrimSpace(c.Fallback)),
	}
	if out.Timeout == 0 {
		out.Timeout = DefaultComplexitySemanticTimeout
	}
	if out.VectorStore == "" {
		out.VectorStore = ComplexitySemanticVectorStoreEmbedded
	}
	if out.MessageHistoryCount == 0 {
		out.MessageHistoryCount = DefaultComplexitySemanticMessageHistoryCount
	}
	if out.Fallback == "" {
		out.Fallback = ComplexitySemanticFallbackNone
	}
	return out
}

// Validate checks a normalized semantic config.
func (c *ComplexitySemanticConfig) Validate() error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(string(c.Provider)) == "" {
		return fmt.Errorf("semantic config requires a provider")
	}
	if strings.TrimSpace(c.EmbeddingModel) == "" {
		return fmt.Errorf("semantic config requires an embedding_model")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("semantic timeout must be positive, got %v", c.Timeout)
	}
	// 1 is a legal ceiling but rejects every real match, so it is treated as a
	// misconfiguration rather than an intentional "never classify semantically".
	// NaN is checked separately because every comparison against it is false, so
	// the range test alone would let it through and silently disable the floor.
	if math.IsNaN(c.MinSimilarity) || c.MinSimilarity < 0 || c.MinSimilarity >= 1 {
		return fmt.Errorf("semantic min_similarity must be at least 0 and less than 1, got %v", c.MinSimilarity)
	}
	if c.MessageHistoryCount < 1 || c.MessageHistoryCount > MaxComplexitySemanticMessageHistoryCount {
		return fmt.Errorf(
			"semantic message_history_count must be between 1 and %d, got %d",
			MaxComplexitySemanticMessageHistoryCount,
			c.MessageHistoryCount,
		)
	}
	switch c.VectorStore {
	case ComplexitySemanticVectorStoreEmbedded, ComplexitySemanticVectorStoreConfigured:
	default:
		return fmt.Errorf("semantic vector_store must be %q or %q, got %q",
			ComplexitySemanticVectorStoreEmbedded, ComplexitySemanticVectorStoreConfigured, c.VectorStore)
	}
	switch c.Fallback {
	case ComplexitySemanticFallbackNone, ComplexitySemanticFallbackLLM:
	default:
		return fmt.Errorf("semantic fallback must be %q or %q, got %q",
			ComplexitySemanticFallbackNone, ComplexitySemanticFallbackLLM, c.Fallback)
	}
	return nil
}

// Semantic fallback selection. The fallback names what answers when semantic
// classification produces no tier — a rejection below min_similarity, a
// timeout, an unready warmup, or an unwired executor. "none" (also the
// meaning of an absent field) keeps today's behaviour: the request is
// recorded as "skipped". "llm" asks the configured chat model instead. The
// LLM classifier only ever runs on this path; it is never the primary.
const (
	ComplexitySemanticFallbackNone = "none"
	ComplexitySemanticFallbackLLM  = "llm"
)

// DefaultComplexityLLMTimeout bounds one LLM classification call. It is
// deliberately larger than the semantic default: a chat completion is slower
// than an embedding, and a budget the model can never meet turns every
// fallback into a skip.
const DefaultComplexityLLMTimeout = 4 * time.Second

// MaxComplexityLLMPromptCharacters bounds the administrator's editable
// classification guidance. The tier-name and response-format reinforcement is
// appended by the gateway and does not count against this; the bound exists
// because the guidance is sent with every fallback classification, so an
// unbounded prompt is a token cost multiplier.
const MaxComplexityLLMPromptCharacters = 4000

// LLM message-history bounds, mirroring the semantic classifier's window over
// recent user turns.
const (
	DefaultComplexityLLMMessageHistoryCount = 1
	MaxComplexityLLMMessageHistoryCount     = 10
)

// ComplexityLLMConfig configures the chat-completion fallback classifier. It
// is inert unless the semantic block's fallback field selects "llm". The
// classification prompt splits in two: Prompt is the editable guidance, and a
// fixed reinforcement naming the tiers and the response format is always
// appended by the gateway — there is no way to rename tiers or change the
// answer contract from configuration.
type ComplexityLLMConfig struct {
	Provider schemas.ModelProvider `json:"provider"`
	Model    string                `json:"model"`
	Timeout  time.Duration         `json:"timeout,omitempty"`
	// Prompt replaces the shipped classification guidance when set; empty
	// means the shipped guidance is used. The reinforcement section is
	// appended either way.
	Prompt string `json:"prompt,omitempty"`
	// MessageHistoryCount is how many of the most recent user messages are
	// given to the classifier, oldest first, matching the semantic field of
	// the same name.
	MessageHistoryCount int  `json:"message_history_count,omitempty"`
	CountTowardBudgets  bool `json:"count_toward_budgets,omitempty"`
}

// UnmarshalJSON accepts Timeout as a duration string ("2s") or a JSON number
// (milliseconds), and rejects unknown fields, both mirroring
// ComplexitySemanticConfig.
func (c *ComplexityLLMConfig) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"provider":              {},
		"model":                 {},
		"timeout":               {},
		"prompt":                {},
		"message_history_count": {},
		"count_toward_budgets":  {},
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return fmt.Errorf("unknown llm complexity field %q", field)
		}
	}

	// alias suppresses ComplexityLLMConfig's UnmarshalJSON to avoid infinite
	// recursion. The outer Timeout (json.RawMessage) shadows alias.Timeout
	// because the json package picks the shallower field.
	type alias ComplexityLLMConfig
	aux := &struct {
		Timeout json.RawMessage `json:"timeout,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.Timeout) == 0 || string(aux.Timeout) == "null" {
		return nil
	}

	var s string
	if err := json.Unmarshal(aux.Timeout, &s); err == nil {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("failed to parse llm timeout duration string %q: %w", s, err)
		}
		c.Timeout = d
	} else {
		var ms float64
		if err := json.Unmarshal(aux.Timeout, &ms); err != nil {
			return fmt.Errorf("unsupported llm timeout value: %s", string(aux.Timeout))
		}
		c.Timeout = time.Duration(ms * float64(time.Millisecond))
	}
	if c.Timeout < 0 {
		return fmt.Errorf("llm timeout must be non-negative, got %v", c.Timeout)
	}
	return nil
}

// MarshalJSON writes Timeout as a duration string so persisted configs decode
// back to the same value, matching ComplexitySemanticConfig.
func (c ComplexityLLMConfig) MarshalJSON() ([]byte, error) {
	type alias ComplexityLLMConfig
	var timeout string
	if c.Timeout != 0 {
		timeout = c.Timeout.String()
	}
	return json.Marshal(struct {
		Timeout string `json:"timeout,omitempty"`
		alias
	}{
		Timeout: timeout,
		alias:   alias(c),
	})
}

// normalized returns a canonical deep copy with defaults applied.
func (c *ComplexityLLMConfig) normalized() *ComplexityLLMConfig {
	if c == nil {
		return nil
	}
	out := &ComplexityLLMConfig{
		Provider:            schemas.ModelProvider(strings.ToLower(strings.TrimSpace(string(c.Provider)))),
		Model:               strings.TrimSpace(c.Model),
		Timeout:             c.Timeout,
		Prompt:              strings.TrimSpace(c.Prompt),
		MessageHistoryCount: c.MessageHistoryCount,
		CountTowardBudgets:  c.CountTowardBudgets,
	}
	if out.Timeout == 0 {
		out.Timeout = DefaultComplexityLLMTimeout
	}
	if out.MessageHistoryCount == 0 {
		out.MessageHistoryCount = DefaultComplexityLLMMessageHistoryCount
	}
	return out
}

// Validate checks a normalized llm config.
func (c *ComplexityLLMConfig) Validate() error {
	if c == nil {
		return nil
	}
	if strings.TrimSpace(string(c.Provider)) == "" {
		return fmt.Errorf("llm config requires a provider")
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("llm config requires a model")
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("llm timeout must be positive, got %v", c.Timeout)
	}
	if characters := utf8.RuneCountInString(c.Prompt); characters > MaxComplexityLLMPromptCharacters {
		return fmt.Errorf(
			"llm prompt exceeds the %d-character limit: got %d characters",
			MaxComplexityLLMPromptCharacters,
			characters,
		)
	}
	if c.MessageHistoryCount < 1 || c.MessageHistoryCount > MaxComplexityLLMMessageHistoryCount {
		return fmt.Errorf(
			"llm message_history_count must be between 1 and %d, got %d",
			MaxComplexityLLMMessageHistoryCount,
			c.MessageHistoryCount,
		)
	}
	return nil
}

// ComplexityAnalyzerConfigHashes tracks the config.json hash for each editable
// analyzer section. It is persisted with the config row, but not exposed through
// API responses or config.json.
type ComplexityAnalyzerConfigHashes struct {
	TierBoundaries  string `json:"tier_boundaries,omitempty"`
	SimpleKeywords  string `json:"simple_keywords,omitempty"`
	MediumKeywords  string `json:"medium_keywords,omitempty"`
	ComplexKeywords string `json:"complex_keywords,omitempty"`
	// SemanticSettings covers the semantic block (provider, model, timeout,
	// budgets flag, vector store). The semantic classifier's
	// exemplars are the shared keyword lists, tracked by the sections above.
	SemanticSettings string `json:"semantic_settings,omitempty"`
	// LLMSettings covers the llm block (provider, model, timeout, prompt,
	// history window, budgets flag). The fallback selector rides the
	// SemanticSettings hash: it is a field of the semantic block.
	LLMSettings string `json:"llm_settings,omitempty"`
}

type legacyComplexityAnalyzerConfigHashes struct {
	TierBoundaries    string `json:"tier_boundaries,omitempty"`
	CodeKeywords      string `json:"code_keywords,omitempty"`
	ReasoningKeywords string `json:"reasoning_keywords,omitempty"`
	TechnicalKeywords string `json:"technical_keywords,omitempty"`
	SimpleKeywords    string `json:"simple_keywords,omitempty"`
}

// UnmarshalJSON translates persisted legacy section hashes into the canonical
// three-list representation without hashing the runtime DB keyword values.
func (h *ComplexityAnalyzerConfigHashes) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	hasCanonical := hasAnyComplexityField(fields, "medium_keywords", "complex_keywords")
	hasLegacy := hasAnyComplexityField(fields, "code_keywords", "reasoning_keywords", "technical_keywords")
	if hasCanonical && hasLegacy {
		return fmt.Errorf("complexity config hashes cannot mix canonical and legacy fields")
	}

	if hasLegacy {
		var legacy legacyComplexityAnalyzerConfigHashes
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		mediumHash, err := legacyMediumKeywordsHashFromSectionHashes(legacy.CodeKeywords, legacy.TechnicalKeywords)
		if err != nil {
			return err
		}
		*h = ComplexityAnalyzerConfigHashes{
			TierBoundaries:  legacy.TierBoundaries,
			SimpleKeywords:  legacy.SimpleKeywords,
			MediumKeywords:  mediumHash,
			ComplexKeywords: legacy.ReasoningKeywords,
		}
		return nil
	}

	type canonicalComplexityAnalyzerConfigHashes ComplexityAnalyzerConfigHashes
	var canonical canonicalComplexityAnalyzerConfigHashes
	if err := json.Unmarshal(data, &canonical); err != nil {
		return err
	}
	*h = ComplexityAnalyzerConfigHashes(canonical)
	return nil
}

// Empty reports whether no file-backed section hashes are present.
func (h ComplexityAnalyzerConfigHashes) Empty() bool {
	return h == ComplexityAnalyzerConfigHashes{}
}

// Equal reports whether all section hashes match.
func (h ComplexityAnalyzerConfigHashes) Equal(other ComplexityAnalyzerConfigHashes) bool {
	return h == other
}

// ComplexityAnalyzerConfig is the persisted runtime configuration for the complexity analyzer.
type ComplexityAnalyzerConfig struct {
	TierBoundaries ComplexityTierBoundaries        `json:"tier_boundaries"`
	Keywords       ComplexityEditableKeywordConfig `json:"keywords"`
	Semantic       *ComplexitySemanticConfig       `json:"semantic,omitempty"`
	// LLM configures the chat-completion fallback classifier, engaged only
	// when Semantic.Fallback selects "llm". It may be present while the
	// fallback says "none": the block is retained so toggling the fallback
	// never loses settings.
	LLM          *ComplexityLLMConfig           `json:"llm,omitempty"`
	ConfigHashes ComplexityAnalyzerConfigHashes `json:"-"`
	// EmbeddingFingerprint is reserved for config-store implementations that
	// persist routing state. The semantic classifier verifies a VectorStore-side
	// marker before reuse and never treats this field alone as proof vectors exist.
	EmbeddingFingerprint string `json:"-"`
}

// complexityAnalyzerConfigRecord is the shape read back from the
// ConfigComplexityAnalyzerConfigKey row.
//
// Only the tier boundaries are read back. The keyword lists in that row are the
// frozen pre-semantic defaults kept for a rolled-back binary, not the exemplars
// this version routes with -- those live in the semantic row. The two are no
// longer the same field in two shapes; they are two different settings for two
// different classifiers.
type complexityAnalyzerConfigRecord struct {
	TierBoundaries ComplexityTierBoundaries    `json:"tier_boundaries"`
	ConfigHashes   complexityAnalyzerRowHashes `json:"_config_hashes,omitempty"`
}

// complexityAnalyzerConfigPersistedRecord is the shape written to the
// ConfigComplexityAnalyzerConfigKey row: exactly what a Bifrost predating the
// semantic router expects, and nothing else. Everything in it is either a value
// that version reads or a value it requires in order to validate.
type complexityAnalyzerConfigPersistedRecord struct {
	TierBoundaries persistedComplexityTierBoundaries     `json:"tier_boundaries"`
	Keywords       legacyComplexityEditableKeywordConfig `json:"keywords"`
	ConfigHashes   complexityAnalyzerRowHashes           `json:"_config_hashes,omitempty"`
}

// complexityAnalyzerRowHashes is the config.json section-hash bookkeeping that
// belongs with the analyzer row. The keyword and semantic section hashes travel
// with the semantic row instead, next to the values they hash.
type complexityAnalyzerRowHashes struct {
	TierBoundaries string `json:"tier_boundaries,omitempty"`
}

// persistedComplexityTierBoundaries adds the third boundary the pre-semantic
// validator requires. Nothing in this version reads ComplexReasoning.
type persistedComplexityTierBoundaries struct {
	SimpleMedium     float64 `json:"simple_medium"`
	MediumComplex    float64 `json:"medium_complex"`
	ComplexReasoning float64 `json:"complex_reasoning"`
}

// complexitySemanticConfigRecord is the shape of the
// ConfigComplexitySemanticConfigKey row. It owns the exemplars as well as the
// semantic settings: an exemplar is only meaningful to the classifier that
// embeds it, and keeping it here is what stops a Bifrost predating the semantic
// router from rewriting it as though it were a lexical keyword.
type complexitySemanticConfigRecord struct {
	Keywords             ComplexityEditableKeywordConfig `json:"keywords"`
	Semantic             *ComplexitySemanticConfig       `json:"semantic,omitempty"`
	LLM                  *ComplexityLLMConfig            `json:"llm,omitempty"`
	ConfigHashes         complexitySemanticRowHashes     `json:"_config_hashes,omitempty"`
	EmbeddingFingerprint string                          `json:"_embedding_fingerprint,omitempty"`
}

// complexitySemanticRowHashes carries the section hashes for everything the
// semantic row owns.
type complexitySemanticRowHashes struct {
	SimpleKeywords   string `json:"simple_keywords,omitempty"`
	MediumKeywords   string `json:"medium_keywords,omitempty"`
	ComplexKeywords  string `json:"complex_keywords,omitempty"`
	SemanticSettings string `json:"semantic_settings,omitempty"`
	LLMSettings      string `json:"llm_settings,omitempty"`
}

// LLMFallbackEnabled reports whether a semantic non-answer should be retried
// through the llm block. Both halves are required: a fallback selector with
// no llm block is rejected by Validate, and a dormant llm block with the
// fallback on "none" never runs.
func (c *ComplexityAnalyzerConfig) LLMFallbackEnabled() bool {
	return c != nil && c.Semantic != nil &&
		c.Semantic.Fallback == ComplexitySemanticFallbackLLM &&
		c.LLM != nil
}

// Validate checks that the config is internally consistent.
func (c *ComplexityAnalyzerConfig) Validate() error {
	if c == nil {
		return nil
	}
	if err := c.TierBoundaries.Validate(); err != nil {
		return err
	}

	var missing []string
	if len(c.Keywords.SimpleKeywords) == 0 {
		missing = append(missing, "simple_keywords")
	}
	if len(c.Keywords.MediumKeywords) == 0 {
		missing = append(missing, "medium_keywords")
	}
	if len(c.Keywords.ComplexKeywords) == 0 {
		missing = append(missing, "complex_keywords")
	}
	if len(missing) > 0 {
		return fmt.Errorf("keyword lists must be non-empty: %s", strings.Join(missing, ", "))
	}
	if err := c.Semantic.Validate(); err != nil {
		return err
	}
	if c.Semantic != nil {
		if err := validateComplexitySemanticPhrases(c.Keywords); err != nil {
			return err
		}
	}
	if err := c.LLM.Validate(); err != nil {
		return err
	}
	if c.Semantic != nil && c.Semantic.Fallback == ComplexitySemanticFallbackLLM && c.LLM == nil {
		return fmt.Errorf("semantic fallback %q requires an llm config block", ComplexitySemanticFallbackLLM)
	}
	return nil
}

// Normalized returns a canonical copy suitable for persistence and runtime use.
func (c *ComplexityAnalyzerConfig) Normalized() ComplexityAnalyzerConfig {
	if c == nil {
		return ComplexityAnalyzerConfig{}
	}
	tierBoundaries := c.TierBoundaries
	if tierBoundaries == (ComplexityTierBoundaries{}) {
		tierBoundaries = DefaultComplexityTierBoundaries()
	}
	return ComplexityAnalyzerConfig{
		TierBoundaries: tierBoundaries,
		Keywords: ComplexityEditableKeywordConfig{
			SimpleKeywords:  normalizeComplexityKeywordList(c.Keywords.SimpleKeywords),
			MediumKeywords:  normalizeComplexityKeywordList(c.Keywords.MediumKeywords),
			ComplexKeywords: normalizeComplexityKeywordList(c.Keywords.ComplexKeywords),
		},
		Semantic:             c.Semantic.normalized(),
		LLM:                  c.LLM.normalized(),
		ConfigHashes:         c.ConfigHashes,
		EmbeddingFingerprint: c.EmbeddingFingerprint,
	}
}

// validateComplexitySemanticPhrases bounds individual inputs and rejects
// phrases labelled with more than one tier.
func validateComplexitySemanticPhrases(keywords ComplexityEditableKeywordConfig) error {
	type tierPhrases struct {
		name   string
		values []string
	}
	tiers := []tierPhrases{
		{name: "simple_keywords", values: keywords.SimpleKeywords},
		{name: "medium_keywords", values: keywords.MediumKeywords},
		{name: "complex_keywords", values: keywords.ComplexKeywords},
	}
	seen := make(map[string]string)
	for _, tier := range tiers {
		for _, phrase := range tier.values {
			if characters := utf8.RuneCountInString(phrase); characters > MaxComplexitySemanticPhraseCharacters {
				return fmt.Errorf(
					"semantic phrase in %s exceeds the %d-character limit: got %d characters",
					tier.name,
					MaxComplexitySemanticPhraseCharacters,
					characters,
				)
			}
			normalized := strings.ToLower(strings.Join(strings.Fields(phrase), " "))
			if previousTier, ok := seen[normalized]; ok && previousTier != tier.name {
				return fmt.Errorf(
					"semantic phrase %q appears in both %s and %s; assign each semantic phrase to exactly one tier",
					phrase,
					previousTier,
					tier.name,
				)
			}
			seen[normalized] = tier.name
		}
	}
	return nil
}

// MergeComplexityAnalyzerConfig overlays file boundaries and additively merges keyword lists.
func MergeComplexityAnalyzerConfig(base, file *ComplexityAnalyzerConfig) (*ComplexityAnalyzerConfig, error) {
	if file == nil {
		if base == nil {
			return nil, nil
		}
		normalized := base.Normalized()
		if err := normalized.Validate(); err != nil {
			return nil, err
		}
		return &normalized, nil
	}

	normalizedFile := file.Normalized()
	if err := normalizedFile.Validate(); err != nil {
		return nil, err
	}

	var normalizedBase ComplexityAnalyzerConfig
	if base != nil {
		normalizedBase = base.Normalized()
		if err := normalizedBase.Validate(); err != nil {
			return nil, err
		}
	}

	merged := ComplexityAnalyzerConfig{
		TierBoundaries: normalizedFile.TierBoundaries,
		Keywords: ComplexityEditableKeywordConfig{
			SimpleKeywords:  mergeComplexityKeywordLists(normalizedBase.Keywords.SimpleKeywords, normalizedFile.Keywords.SimpleKeywords),
			MediumKeywords:  mergeComplexityKeywordLists(normalizedBase.Keywords.MediumKeywords, normalizedFile.Keywords.MediumKeywords),
			ComplexKeywords: mergeComplexityKeywordLists(normalizedBase.Keywords.ComplexKeywords, normalizedFile.Keywords.ComplexKeywords),
		},
		Semantic:             mergeComplexitySemanticConfig(normalizedBase.Semantic, normalizedFile.Semantic),
		LLM:                  mergeComplexityLLMConfig(normalizedBase.LLM, normalizedFile.LLM),
		ConfigHashes:         normalizedFile.ConfigHashes,
		EmbeddingFingerprint: normalizedBase.EmbeddingFingerprint,
	}
	normalizedMerged := merged.Normalized()
	if err := normalizedMerged.Validate(); err != nil {
		return nil, err
	}
	return &normalizedMerged, nil
}

// mergeComplexitySemanticConfig overlays the file semantic settings. A nil
// file section keeps the base untouched.
func mergeComplexitySemanticConfig(base, file *ComplexitySemanticConfig) *ComplexitySemanticConfig {
	if file == nil {
		return base.normalized()
	}
	return file.normalized()
}

// mergeComplexityLLMConfig overlays the file llm settings. A nil file section
// keeps the base untouched.
func mergeComplexityLLMConfig(base, file *ComplexityLLMConfig) *ComplexityLLMConfig {
	if file == nil {
		return base.normalized()
	}
	return file.normalized()
}

// MergeComplexityAnalyzerConfigByHashes overlays only file-backed sections whose
// config.json hash changed. Keyword sections are additive; tier boundaries replace.
func MergeComplexityAnalyzerConfigByHashes(base, file *ComplexityAnalyzerConfig) (*ComplexityAnalyzerConfig, error) {
	if file == nil {
		return MergeComplexityAnalyzerConfig(base, nil)
	}

	normalizedFile := file.Normalized()
	if err := normalizedFile.Validate(); err != nil {
		return nil, err
	}

	var merged ComplexityAnalyzerConfig
	if base != nil {
		merged = base.Normalized()
		if err := merged.Validate(); err != nil {
			return nil, err
		}
	}

	if merged.ConfigHashes.TierBoundaries != normalizedFile.ConfigHashes.TierBoundaries {
		merged.TierBoundaries = normalizedFile.TierBoundaries
		merged.ConfigHashes.TierBoundaries = normalizedFile.ConfigHashes.TierBoundaries
	}
	if merged.ConfigHashes.SimpleKeywords != normalizedFile.ConfigHashes.SimpleKeywords {
		merged.Keywords.SimpleKeywords = mergeComplexityKeywordLists(merged.Keywords.SimpleKeywords, normalizedFile.Keywords.SimpleKeywords)
		merged.ConfigHashes.SimpleKeywords = normalizedFile.ConfigHashes.SimpleKeywords
	}
	if merged.ConfigHashes.MediumKeywords != normalizedFile.ConfigHashes.MediumKeywords {
		merged.Keywords.MediumKeywords = mergeComplexityKeywordLists(merged.Keywords.MediumKeywords, normalizedFile.Keywords.MediumKeywords)
		merged.ConfigHashes.MediumKeywords = normalizedFile.ConfigHashes.MediumKeywords
	}
	if merged.ConfigHashes.ComplexKeywords != normalizedFile.ConfigHashes.ComplexKeywords {
		merged.Keywords.ComplexKeywords = mergeComplexityKeywordLists(merged.Keywords.ComplexKeywords, normalizedFile.Keywords.ComplexKeywords)
		merged.ConfigHashes.ComplexKeywords = normalizedFile.ConfigHashes.ComplexKeywords
	}
	// A config.json without a semantic section leaves DB semantic state (and its
	// section hash) untouched: the section is optional, so absence means "no
	// opinion", not removal.
	if normalizedFile.Semantic != nil {
		if merged.Semantic == nil || merged.ConfigHashes.SemanticSettings != normalizedFile.ConfigHashes.SemanticSettings {
			merged.Semantic = normalizedFile.Semantic.normalized()
			merged.ConfigHashes.SemanticSettings = normalizedFile.ConfigHashes.SemanticSettings
		}
	}
	// The llm block follows the same "absence means no opinion" rule as the
	// semantic section above: only a file that states it may change it, and
	// only when its hash moved.
	if normalizedFile.LLM != nil {
		if merged.LLM == nil || merged.ConfigHashes.LLMSettings != normalizedFile.ConfigHashes.LLMSettings {
			merged.LLM = normalizedFile.LLM.normalized()
			merged.ConfigHashes.LLMSettings = normalizedFile.ConfigHashes.LLMSettings
		}
	}
	normalizedMerged := merged.Normalized()
	if err := normalizedMerged.Validate(); err != nil {
		return nil, err
	}
	return &normalizedMerged, nil
}

// DecodeComplexityAnalyzerConfig decodes the analyzer row.
//
// The result carries tier boundaries only. Exemplars, semantic settings and the
// embedding fingerprint come from the semantic row and are attached by the
// caller, which then validates the combined config.
func DecodeComplexityAnalyzerConfig(data []byte) (*ComplexityAnalyzerConfig, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var record complexityAnalyzerConfigRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("%w: failed to unmarshal complexity analyzer config: %w", ErrConfigUnreadable, err)
	}

	cfg := ComplexityAnalyzerConfig{
		TierBoundaries: record.TierBoundaries,
		ConfigHashes:   ComplexityAnalyzerConfigHashes{TierBoundaries: record.ConfigHashes.TierBoundaries},
	}
	normalized := cfg.Normalized()
	// Only the boundaries: this row no longer carries the keyword lists, so the
	// full Validate would fail on them being empty.
	if err := normalized.TierBoundaries.Validate(); err != nil {
		return nil, fmt.Errorf("%w: invalid complexity analyzer config: %w", ErrConfigUnreadable, err)
	}
	return &normalized, nil
}

// encodeComplexityAnalyzerConfig writes the analyzer row in the pre-semantic
// shape. The keyword lists are always the frozen released defaults: an older
// Bifrost scores lexically against whatever is in them, and this version has no
// lexical keywords of its own to offer -- what an operator edits here is
// exemplars, which belong to the semantic row.
func encodeComplexityAnalyzerConfig(config ComplexityAnalyzerConfig) ([]byte, error) {
	record := complexityAnalyzerConfigPersistedRecord{
		TierBoundaries: persistedComplexityTierBoundaries{
			SimpleMedium:     config.TierBoundaries.SimpleMedium,
			MediumComplex:    config.TierBoundaries.MediumComplex,
			ComplexReasoning: legacyComplexReasoningBoundary(config.TierBoundaries.MediumComplex),
		},
		Keywords: legacyComplexityEditableKeywordConfig{
			CodeKeywords:      legacyCodeKeywords,
			ReasoningKeywords: legacyReasoningKeywords,
			TechnicalKeywords: legacyTechnicalKeywords,
			SimpleKeywords:    legacySimpleKeywords,
		},
		ConfigHashes: complexityAnalyzerRowHashes{
			TierBoundaries: config.ConfigHashes.TierBoundaries,
		},
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal complexity analyzer config: %w", err)
	}
	return data, nil
}

// decodeComplexitySemanticConfigRow decodes the semantic row. A missing or empty
// row means nothing has been configured yet, which is not an error.
func decodeComplexitySemanticConfigRow(data []byte) (*complexitySemanticConfigRecord, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var record complexitySemanticConfigRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("%w: failed to unmarshal complexity semantic config: %w", ErrConfigUnreadable, err)
	}
	return &record, nil
}

// encodeComplexitySemanticConfigRow writes the semantic row.
func encodeComplexitySemanticConfigRow(config ComplexityAnalyzerConfig) ([]byte, error) {
	record := complexitySemanticConfigRecord{
		Keywords: config.Keywords,
		Semantic: config.Semantic,
		LLM:      config.LLM,
		ConfigHashes: complexitySemanticRowHashes{
			SimpleKeywords:   config.ConfigHashes.SimpleKeywords,
			MediumKeywords:   config.ConfigHashes.MediumKeywords,
			ComplexKeywords:  config.ConfigHashes.ComplexKeywords,
			SemanticSettings: config.ConfigHashes.SemanticSettings,
			LLMSettings:      config.ConfigHashes.LLMSettings,
		},
		EmbeddingFingerprint: config.EmbeddingFingerprint,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal complexity semantic config: %w", err)
	}
	return data, nil
}

// applyComplexitySemanticConfigRow layers the semantic row onto a config decoded
// from the analyzer row, producing the combined runtime config.
func applyComplexitySemanticConfigRow(base *ComplexityAnalyzerConfig, row *complexitySemanticConfigRecord) *ComplexityAnalyzerConfig {
	if base == nil || row == nil {
		return nil
	}
	combined := *base
	combined.Keywords = row.Keywords
	combined.Semantic = row.Semantic
	combined.LLM = row.LLM
	combined.ConfigHashes.SimpleKeywords = row.ConfigHashes.SimpleKeywords
	combined.ConfigHashes.MediumKeywords = row.ConfigHashes.MediumKeywords
	combined.ConfigHashes.ComplexKeywords = row.ConfigHashes.ComplexKeywords
	combined.ConfigHashes.SemanticSettings = row.ConfigHashes.SemanticSettings
	combined.ConfigHashes.LLMSettings = row.ConfigHashes.LLMSettings
	combined.EmbeddingFingerprint = row.EmbeddingFingerprint
	return &combined
}

func normalizeComplexityKeywordList(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
}

func mergeComplexityKeywordLists(base, overlay []string) []string {
	values := make([]string, 0, len(base)+len(overlay))
	values = append(values, base...)
	values = append(values, overlay...)
	return normalizeComplexityKeywordList(values)
}
