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
)

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
// as exemplars. The lexical matcher also reads them as keywords, but it no
// longer publishes a tier, so nothing an operator writes here reaches routing
// by that path.
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
	return nil
}

// Complexity session modes.
//
// "off" is a first-class value rather than only an absent block so the UI's
// three-way toggle can disable session behaviour without discarding the TTL and
// threshold settings an admin already tuned. A nil *ComplexitySessionConfig and
// an explicit "off" are equivalent at runtime; use Enabled to test either.
//
// "pinned" classifies once per session and reuses the tier until the pin is
// released. "cache_aware" classifies every turn whose routing rules consult
// complexity and gates tier changes; it needs that proposal to decide.
const (
	ComplexitySessionModeOff        = "off"
	ComplexitySessionModePinned     = "pinned"
	ComplexitySessionModeCacheAware = "cache_aware"
)

// Complexity session identity sources, listed in resolution order: an explicit
// x-bf-session-id header, then a harness-native ID.
const (
	ComplexitySessionIdentityHeader  = "header"
	ComplexitySessionIdentityHarness = "harness"
)

// Complexity session defaults, applied by normalized when a field is unset.
// SwitchMinSimilarity is deliberately absent: a sensible absolute value depends
// on the exemplar set's score distribution, so it ships unset (no gating).
const (
	DefaultComplexitySessionTTL                   = time.Hour
	DefaultComplexitySessionDowngradeAfterNTurns  = 2
	DefaultComplexitySessionMinCachedTokensToHold = 1024
)

// DefaultComplexitySessionIdentitySources is the identity ladder applied when
// none is configured.
func DefaultComplexitySessionIdentitySources() []string {
	return []string{ComplexitySessionIdentityHeader, ComplexitySessionIdentityHarness}
}

// ComplexitySessionConfig gives the complexity router a memory of the
// conversation a request belongs to. A nil value, like Mode "off", disables it.
//
// Session state is a memoised classifier result with a TTL, not an override of
// routing: it replaces the lazy complexity computation, so every routing rule
// still evaluates normally and only the classification is skipped.
type ComplexitySessionConfig struct {
	Mode string `json:"mode"`
	// TTL bounds how long a session record survives without traffic. It slides
	// on every read, so it measures idle time rather than total session age.
	TTL time.Duration `json:"ttl,omitempty"`
	// IdentitySources selects which of the resolution steps may establish a
	// session ID, tried in the constant order above regardless of the order
	// given here.
	IdentitySources []string `json:"identity_sources,omitempty"`

	// SwitchMinSimilarity is the confidence a classification must reach before
	// it may change session state, forming hysteresis with the semantic
	// classifier's MinSimilarity: a low bar to classify, a higher bar to switch.
	// It must be at least Semantic.MinSimilarity. Zero means no similarity
	// gating at all — escalations pass and the sustained-confidence downgrade
	// check is skipped — rather than a threshold of zero.
	//
	// Like MinSimilarity this is compared against the VectorStore backend's own
	// score, and those scales differ (chromem, Qdrant, Pinecone, and Redis
	// report raw cosine; Weaviate reports certainty). Retune on a backend
	// switch. A lexical score is a different scale entirely and is never
	// comparable against this value.
	SwitchMinSimilarity float64 `json:"switch_min_similarity,omitempty"`
	// DowngradeAfterNTurns is how many consecutive turns a lower tier must be
	// proposed before a downgrade is taken. Long agentic sessions are full of
	// turns like "yes" or "run the tests" that classify as confidently simple;
	// those are exactly the turns not to downgrade on, because the classifier is
	// right about the turn and wrong about the session.
	DowngradeAfterNTurns int `json:"downgrade_after_n_turns,omitempty"`
	// MinCachedTokensToHold is the observed cached-token count below which the
	// current tier's cache is considered too small to be worth protecting.
	MinCachedTokensToHold int `json:"min_cached_tokens_to_hold,omitempty"`
	// MaxSwitchesPerSession caps total tier moves as an oscillation backstop.
	// Zero means unlimited.
	MaxSwitchesPerSession int `json:"max_switches_per_session,omitempty"`
	// AlwaysAllowEscalation lets an escalation bypass the SwitchMinSimilarity
	// gate. Escalations already ignore cache cost: holding a session on an
	// undersized model to protect cache spend produces bad answers, which is a
	// worse failure than overpaying.
	AlwaysAllowEscalation bool `json:"always_allow_escalation,omitempty"`
}

// UnmarshalJSON accepts TTL as a duration string ("30m") or a JSON number
// (milliseconds), matching ComplexitySemanticConfig.Timeout. All other fields
// decode through the default path via an alias.
func (c *ComplexitySessionConfig) UnmarshalJSON(data []byte) error {
	// alias suppresses ComplexitySessionConfig's UnmarshalJSON to avoid
	// infinite recursion. The outer TTL (json.RawMessage) shadows alias.TTL
	// because the json package picks the shallower field.
	type alias ComplexitySessionConfig
	aux := &struct {
		TTL json.RawMessage `json:"ttl,omitempty"`
		*alias
	}{alias: (*alias)(c)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	if len(aux.TTL) == 0 || string(aux.TTL) == "null" {
		return nil
	}

	var s string
	if err := json.Unmarshal(aux.TTL, &s); err == nil {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("failed to parse session ttl duration string %q: %w", s, err)
		}
		c.TTL = d
	} else {
		var ms float64
		if err := json.Unmarshal(aux.TTL, &ms); err != nil {
			return fmt.Errorf("unsupported session ttl value: %s", string(aux.TTL))
		}
		c.TTL = time.Duration(ms * float64(time.Millisecond))
	}
	if c.TTL < 0 {
		return fmt.Errorf("session ttl must be non-negative, got %v", c.TTL)
	}
	return nil
}

// MarshalJSON writes TTL as a duration string so persisted configs decode back
// to the same value (the default int encoding is nanoseconds, which the
// millisecond-number decode path would misread).
func (c ComplexitySessionConfig) MarshalJSON() ([]byte, error) {
	type alias ComplexitySessionConfig
	var ttl string
	if c.TTL != 0 {
		ttl = c.TTL.String()
	}
	return json.Marshal(struct {
		TTL string `json:"ttl,omitempty"`
		alias
	}{
		TTL:   ttl,
		alias: alias(c),
	})
}

// Enabled reports whether session behaviour should engage. It is the only
// correct way to test this: a nil block, an empty mode, and an explicit "off"
// all mean disabled.
func (c *ComplexitySessionConfig) Enabled() bool {
	if c == nil {
		return false
	}
	switch c.Mode {
	case ComplexitySessionModePinned, ComplexitySessionModeCacheAware:
		return true
	default:
		return false
	}
}

// normalized returns a canonical deep copy with defaults applied. Defaults are
// applied even when the mode is "off" so that toggling session behaviour back
// on preserves what the admin configured.
func (c *ComplexitySessionConfig) normalized() *ComplexitySessionConfig {
	if c == nil {
		return nil
	}
	out := &ComplexitySessionConfig{
		Mode:                  strings.ToLower(strings.TrimSpace(c.Mode)),
		TTL:                   c.TTL,
		IdentitySources:       normalizeComplexitySessionIdentitySources(c.IdentitySources),
		SwitchMinSimilarity:   c.SwitchMinSimilarity,
		DowngradeAfterNTurns:  c.DowngradeAfterNTurns,
		MinCachedTokensToHold: c.MinCachedTokensToHold,
		MaxSwitchesPerSession: c.MaxSwitchesPerSession,
		AlwaysAllowEscalation: c.AlwaysAllowEscalation,
	}
	if out.Mode == "" {
		out.Mode = ComplexitySessionModeOff
	}
	if out.TTL == 0 {
		out.TTL = DefaultComplexitySessionTTL
	}
	if len(out.IdentitySources) == 0 {
		out.IdentitySources = DefaultComplexitySessionIdentitySources()
	}
	if out.DowngradeAfterNTurns == 0 {
		out.DowngradeAfterNTurns = DefaultComplexitySessionDowngradeAfterNTurns
	}
	if out.MinCachedTokensToHold == 0 {
		out.MinCachedTokensToHold = DefaultComplexitySessionMinCachedTokensToHold
	}
	return out
}

// normalizeComplexitySessionIdentitySources lowercases, de-duplicates, and
// orders sources by the resolution ladder so callers can walk them directly.
func normalizeComplexitySessionIdentitySources(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		seen[normalized] = true
	}
	var out []string
	for _, source := range []string{
		ComplexitySessionIdentityHeader,
		ComplexitySessionIdentityHarness,
	} {
		if seen[source] {
			out = append(out, source)
			delete(seen, source)
		}
	}
	// Unknown values are preserved so Validate can name them rather than
	// silently dropping a typo'd source.
	unknown := make([]string, 0, len(seen))
	for source := range seen {
		unknown = append(unknown, source)
	}
	sort.Strings(unknown)
	return append(out, unknown...)
}

// Validate checks a normalized session config. Cross-section rules that need
// the surrounding analyzer config — notably SwitchMinSimilarity against
// Semantic.MinSimilarity — are enforced by ComplexityAnalyzerConfig.Validate.
func (c *ComplexitySessionConfig) Validate() error {
	if c == nil {
		return nil
	}
	switch c.Mode {
	case ComplexitySessionModeOff, ComplexitySessionModePinned, ComplexitySessionModeCacheAware:
	default:
		return fmt.Errorf("session mode must be %q, %q, or %q, got %q",
			ComplexitySessionModeOff, ComplexitySessionModePinned, ComplexitySessionModeCacheAware, c.Mode)
	}
	if c.TTL <= 0 {
		return fmt.Errorf("session ttl must be positive, got %v", c.TTL)
	}
	for _, source := range c.IdentitySources {
		switch source {
		case ComplexitySessionIdentityHeader, ComplexitySessionIdentityHarness:
		default:
			return fmt.Errorf("session identity_sources may contain %q or %q, got %q",
				ComplexitySessionIdentityHeader, ComplexitySessionIdentityHarness, source)
		}
	}
	if len(c.IdentitySources) == 0 {
		return fmt.Errorf("session identity_sources must list at least one source")
	}
	// 1 is a legal ceiling but rejects every real match, matching the
	// MinSimilarity treatment: a misconfiguration, not "never switch".
	if c.SwitchMinSimilarity < 0 || c.SwitchMinSimilarity >= 1 {
		return fmt.Errorf("session switch_min_similarity must be at least 0 and less than 1, got %v", c.SwitchMinSimilarity)
	}
	if c.DowngradeAfterNTurns < 1 {
		return fmt.Errorf("session downgrade_after_n_turns must be at least 1, got %d", c.DowngradeAfterNTurns)
	}
	if c.MinCachedTokensToHold < 0 {
		return fmt.Errorf("session min_cached_tokens_to_hold must be non-negative, got %d", c.MinCachedTokensToHold)
	}
	if c.MaxSwitchesPerSession < 0 {
		return fmt.Errorf("session max_switches_per_session must be non-negative, got %d", c.MaxSwitchesPerSession)
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
	// SessionSettings covers the session block (mode, ttl, identity sources,
	// and the cache-aware switch thresholds).
	SessionSettings string `json:"session_settings,omitempty"`
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
	Session        *ComplexitySessionConfig        `json:"session,omitempty"`
	ConfigHashes   ComplexityAnalyzerConfigHashes  `json:"-"`
	// EmbeddingFingerprint is reserved for config-store implementations that
	// persist routing state. The semantic classifier verifies a VectorStore-side
	// marker before reuse and never treats this field alone as proof vectors exist.
	EmbeddingFingerprint string `json:"-"`
}

type complexityAnalyzerConfigRecord struct {
	TierBoundaries       ComplexityTierBoundaries        `json:"tier_boundaries"`
	Keywords             ComplexityEditableKeywordConfig `json:"keywords"`
	Semantic             *ComplexitySemanticConfig       `json:"semantic,omitempty"`
	Session              *ComplexitySessionConfig        `json:"session,omitempty"`
	ConfigHashes         ComplexityAnalyzerConfigHashes  `json:"_config_hashes,omitempty"`
	EmbeddingFingerprint string                          `json:"_embedding_fingerprint,omitempty"`
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
	if err := c.Session.Validate(); err != nil {
		return err
	}
	// SwitchMinSimilarity and MinSimilarity form hysteresis: a low bar to accept
	// a classification, a higher bar to let it change session state. Inverting
	// them would let a classification too weak to be believed still move a
	// session, so the ordering is enforced rather than clamped. Zero means the
	// switch gate is off entirely and is exempt.
	if c.Session != nil && c.Semantic != nil && c.Session.SwitchMinSimilarity > 0 &&
		c.Session.SwitchMinSimilarity < c.Semantic.MinSimilarity {
		return fmt.Errorf(
			"session switch_min_similarity (%v) must be at least semantic min_similarity (%v)",
			c.Session.SwitchMinSimilarity, c.Semantic.MinSimilarity,
		)
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
		Session:              c.Session.normalized(),
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
		Session:              mergeComplexitySessionConfig(normalizedBase.Session, normalizedFile.Session),
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

// mergeComplexitySessionConfig overlays the file session settings. A nil file
// section keeps the base untouched.
func mergeComplexitySessionConfig(base, file *ComplexitySessionConfig) *ComplexitySessionConfig {
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
	// Same "absence means no opinion" rule as the semantic section above: a
	// config.json without a session block leaves DB session state alone.
	if normalizedFile.Session != nil {
		if merged.Session == nil || merged.ConfigHashes.SessionSettings != normalizedFile.ConfigHashes.SessionSettings {
			merged.Session = normalizedFile.Session.normalized()
			merged.ConfigHashes.SessionSettings = normalizedFile.ConfigHashes.SessionSettings
		}
	}
	normalizedMerged := merged.Normalized()
	if err := normalizedMerged.Validate(); err != nil {
		return nil, err
	}
	return &normalizedMerged, nil
}

// DecodeComplexityAnalyzerConfig decodes raw JSON into a normalized, validated config.
func DecodeComplexityAnalyzerConfig(data []byte) (*ComplexityAnalyzerConfig, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var record complexityAnalyzerConfigRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("failed to unmarshal complexity analyzer config: %w", err)
	}

	cfg := ComplexityAnalyzerConfig{
		TierBoundaries:       record.TierBoundaries,
		Keywords:             record.Keywords,
		Semantic:             record.Semantic,
		Session:              record.Session,
		ConfigHashes:         record.ConfigHashes,
		EmbeddingFingerprint: record.EmbeddingFingerprint,
	}
	normalized := cfg.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, fmt.Errorf("invalid complexity analyzer config: %w", err)
	}
	return &normalized, nil
}

func encodeComplexityAnalyzerConfig(config ComplexityAnalyzerConfig) ([]byte, error) {
	record := complexityAnalyzerConfigRecord{
		TierBoundaries:       config.TierBoundaries,
		Keywords:             config.Keywords,
		Semantic:             config.Semantic,
		Session:              config.Session,
		ConfigHashes:         config.ConfigHashes,
		EmbeddingFingerprint: config.EmbeddingFingerprint,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal complexity analyzer config: %w", err)
	}
	return data, nil
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
