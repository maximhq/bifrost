package complexity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

// ErrJevAPIKeyMissing reports that the jev block is configured but no API key
// was found, so the decision API can never be reached. It is distinct from a
// transport failure because the fix is configuration, not connectivity.
var ErrJevAPIKeyMissing = errors.New("jev classifier has no API key: set the jev api_key or the TYPESAFE_API_KEY environment variable")

// jevAPIKeyEnvVar names the environment variable consulted when the jev block
// does not carry an inline api_key. The environment is preferred precisely so
// the key never lands in config.json.
const jevAPIKeyEnvVar = "TYPESAFE_API_KEY"

// System One question types. A decision request fans out one or more of these
// in a single call; the complexity classifier uses choice + score, and the
// remaining type is declared for wire completeness.
const (
	SystemOneQuestionChoice = "choice"
	SystemOneQuestionScore  = "score"
	SystemOneQuestionNoul   = "noul"
)

// SystemOneQuestion is one question in a decision request. Criteria is an
// object of label → description for choice questions and an ordered
// lowest → highest array of level descriptions for score questions.
type SystemOneQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

// SystemOneAnswer is the decision model's answer to one question. Confidence
// is the calibrated P(answer correct) and Probabilities the distribution over
// labels or levels; both may be absent and are then read as zero.
type SystemOneAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
}

// SystemOneRequest is one decision request. State is the ONLY input the
// decision model sees: no tools, no transcript access.
type SystemOneRequest struct {
	Model     string                       `json:"model"`
	State     string                       `json:"state"`
	Questions map[string]SystemOneQuestion `json:"questions"`
}

// SystemOneUsage reports token consumption for one decision request.
type SystemOneUsage struct {
	InputTokens int `json:"input_tokens"`
}

// SystemOneResponse carries the answers keyed by question id.
type SystemOneResponse struct {
	Answers map[string]SystemOneAnswer `json:"answers"`
	Usage   *SystemOneUsage            `json:"usage,omitempty"`
}

// SystemOneFunc executes one decision request. The shipped implementation is
// HTTPSystemOneFunc; tests supply fakes so every classifier behaviour is
// provable without a real endpoint.
type SystemOneFunc func(ctx context.Context, cfg *configstore.ComplexityJevConfig, req *SystemOneRequest) (*SystemOneResponse, error)

// Jev verdict reasons, mirroring the reference router's decision reasons.
const (
	// JevReasonConfident means all three decision gates passed and the tier
	// verdict may be used.
	JevReasonConfident = "jev-confident"
	// JevReasonLowConfidence means the tier choice or the complexity score
	// carried too little confidence to act on.
	JevReasonLowConfidence = "low-confidence"
	// JevReasonHighComplexity means the request is too demanding to publish
	// the chosen tier.
	JevReasonHighComplexity = "high-complexity"
	// JevReasonEngineUnavailable means the decision model answered without a
	// usable tier choice.
	JevReasonEngineUnavailable = "engine-unavailable"
	// JevReasonUnknownTier means the answer named a tier outside the
	// declared set. The decision model may only name tiers that exist.
	JevReasonUnknownTier = "unknown-tier"
)

// JevResult is the classifier's verdict for one request. Tier is empty unless
// every gate passed; the raw score and confidences are always reported so
// routing rules can apply their own predicates.
type JevResult struct {
	Tier                 string
	Complexity           float64
	Confidence           float64
	ComplexityConfidence float64
	Reason               string
}

// jevTierCriteria describes the shipped tiers for the decision model's choice
// question. The wording mirrors the llm classifier's guidance so both
// classifiers draw the same tier boundaries; only the transport differs.
func jevTierCriteria() map[string]string {
	return map[string]string{
		TierSimple:  "Greetings, short factual questions, small single-step edits or commands - work a small, fast model handles well.",
		TierMedium:  "Routine multi-step work - summarization, ordinary code changes, structured extraction, standard analysis.",
		TierComplex: "Deep or novel reasoning - cross-system debugging, architecture, long multi-constraint planning, tasks where a wrong answer is costly.",
	}
}

// jevComplexityCriteria are the levels of the complexity score question. The
// decision API answers with a weighted level index over these — raw 0..n-1
// (measured 0..2 on the wire) — so the gates compare the score normalized
// onto the 0..1 scale the config thresholds speak.
var jevComplexityCriteria = []string{
	"Mechanical: single location, known pattern, no design decision",
	"Moderate: several locations, or requires some judgment",
	"Hard: architectural, ambiguous, or getting it wrong is expensive",
}

// normalizeJevComplexity maps a raw weighted level index onto the 0..1 scale
// used by the gate thresholds and the logged complexity value.
func normalizeJevComplexity(raw float64) float64 {
	return raw / float64(len(jevComplexityCriteria)-1)
}

// jevDecisionQuestions builds the two-question fan-out sent in ONE call: the
// choice says which tier suffices, the score independently says how hard the
// request is. Requiring both to agree before publishing a tier is what keeps
// a single wrong axis from degrading routing. The wording is part of the
// decision contract and deliberately not configurable.
func jevDecisionQuestions() map[string]SystemOneQuestion {
	return map[string]SystemOneQuestion{
		"tier": {
			Type:         SystemOneQuestionChoice,
			Instructions: "Which capability tier is sufficient to handle this request well?",
			Criteria:     jevTierCriteria(),
		},
		"complexity": {
			Type:         SystemOneQuestionScore,
			Instructions: "How demanding is this request to carry out correctly?",
			Criteria:     jevComplexityCriteria,
		},
	}
}

// clipJevState bounds the state to the decision API's input budget, keeping
// the leading prefix and cutting on a rune boundary so multi-byte text is
// never split mid-character.
func clipJevState(text string) string {
	limit := configstore.MaxComplexityJevStateCharacters
	if len(text) <= limit {
		return text
	}
	end := 0
	for pos, r := range text {
		next := pos + utf8.RuneLen(r)
		if next > limit {
			break
		}
		end = next
	}
	return text[:end]
}

// JevClassifier classifies a request by asking the System One decision API to
// name a tier and score the request's complexity in one fan-out. It is
// stateless between requests: Configure and SetSystemOneFunc only swap the
// snapshot the next classification reads, mirroring LLMClassifier.
type JevClassifier struct {
	logger schemas.Logger

	mu        sync.Mutex
	config    *AnalyzerConfig
	systemOne SystemOneFunc
}

// NewJevClassifier creates a disabled classifier. Callers configure it and
// supply the decision transport independently, matching NewLLMClassifier.
func NewJevClassifier(logger schemas.Logger) *JevClassifier {
	return &JevClassifier{logger: logger}
}

// Configure snapshots the current analyzer configuration.
func (c *JevClassifier) Configure(config *AnalyzerConfig) {
	c.mu.Lock()
	c.config = cloneAnalyzerConfig(config)
	c.mu.Unlock()
}

// SetSystemOneFunc supplies or clears the decision transport. The plugin wires
// HTTPSystemOneFunc at construction; tests replace it.
func (c *JevClassifier) SetSystemOneFunc(fn SystemOneFunc) {
	c.mu.Lock()
	c.systemOne = fn
	c.mu.Unlock()
}

// IsConfigured reports whether a jev block exists in the current
// configuration, independently from whether the transport is wired.
func (c *JevClassifier) IsConfigured() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config != nil && c.config.Jev != nil
}

// FallbackEnabled reports whether a semantic non-answer should be retried
// here, mirroring LLMClassifier.FallbackEnabled: a dormant jev block retained
// while the fallback selector says "none" must not run on the request path.
func (c *JevClassifier) FallbackEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.config.JevFallbackEnabled()
}

// Timeout returns the per-decision budget currently in force.
func (c *JevClassifier) Timeout() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config == nil || c.config.Jev == nil || c.config.Jev.Timeout <= 0 {
		return configstore.DefaultComplexityJevTimeout
	}
	return c.config.Jev.Timeout
}

// Classify asks the decision API for a tier and complexity score for the
// request's recent user turns. A nil result with a nil error means the
// classifier is not configured or not wired, matching LLMClassifier.Classify.
// A non-nil result with an empty Tier means the decision model answered but
// the verdict was withheld (gate failure, unknown tier, unusable answers); the
// reason names which.
func (c *JevClassifier) Classify(ctx context.Context, input ComplexityInput) (*JevResult, error) {
	c.mu.Lock()
	if c.config == nil || c.config.Jev == nil || c.systemOne == nil {
		c.mu.Unlock()
		return nil, nil
	}
	jev := cloneJevConfig(c.config.Jev)
	systemOne := c.systemOne
	c.mu.Unlock()

	text := clipJevState(SemanticInputText(input, jev.MessageHistoryCount))
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}

	decisionCtx, cancel := context.WithTimeout(ctx, c.Timeout())
	defer cancel()

	resp, err := systemOne(decisionCtx, jev, &SystemOneRequest{
		Model:     jev.Model,
		State:     text,
		Questions: jevDecisionQuestions(),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return &JevResult{Complexity: 1, Reason: JevReasonEngineUnavailable}, nil
	}
	return decideJev(resp.Answers, jev), nil
}

// decideJev applies the three independent gates from the reference router.
// Unknown complexity defaults to HARD and unknown confidence to zero — both
// failures read as expensive, never as easy. Any gate failing yields a
// non-nil result with an empty Tier; the caller records the reason.
func decideJev(answers map[string]SystemOneAnswer, jev *configstore.ComplexityJevConfig) *JevResult {
	// An absent complexity score counts as maximum complexity: unsure must
	// never read as easy. Present scores are raw weighted level indices
	// (0..n-1) and are normalized before the gates compare them.
	complexity := 1.0
	complexityConfidence := 0.0
	if answer, ok := answers["complexity"]; ok && answer.Score != nil {
		complexity = normalizeJevComplexity(*answer.Score)
		complexityConfidence = answer.Confidence
	}

	result := &JevResult{
		Complexity:           complexity,
		ComplexityConfidence: complexityConfidence,
	}

	choice, ok := answers["tier"]
	if !ok || choice.Choice == "" {
		result.Reason = JevReasonEngineUnavailable
		return result
	}
	result.Confidence = choice.Confidence

	tier := normalizeLLMTier(choice.Choice)
	if tier == "" {
		result.Reason = JevReasonUnknownTier
		return result
	}

	// Three independent gates. Any one failing withholds the tier.
	if choice.Confidence < jev.MinConfidenceToDegrade {
		result.Reason = JevReasonLowConfidence
		return result
	}
	if complexity > jev.MaxComplexityForDegrade {
		result.Reason = JevReasonHighComplexity
		return result
	}
	if complexityConfidence < jev.MinComplexityConfidence {
		result.Reason = JevReasonLowConfidence
		return result
	}

	result.Tier = tier
	result.Reason = JevReasonConfident
	return result
}

// resolveJevAPIKey prefers the configured key and falls back to the
// environment at call time, so rotating the env var needs no reload.
func resolveJevAPIKey(cfg *configstore.ComplexityJevConfig) string {
	if cfg != nil && cfg.APIKey != "" {
		return cfg.APIKey
	}
	return os.Getenv(jevAPIKeyEnvVar)
}

// httpJevClient is the shared transport for decision calls. It carries no
// overall timeout: the classifier bounds every call through its context.
var httpJevClient = &http.Client{}

// HTTPSystemOneFunc is the shipped decision transport: one POST per request,
// JSON in and out, Bearer auth. Zero new dependencies beyond net/http. Any
// non-200, timeout, or malformed body is an error — the classifier's callers
// treat every failure as "no verdict", never as a reason to block a request.
func HTTPSystemOneFunc(ctx context.Context, cfg *configstore.ComplexityJevConfig, req *SystemOneRequest) (*SystemOneResponse, error) {
	apiKey := resolveJevAPIKey(cfg)
	if apiKey == "" {
		return nil, ErrJevAPIKeyMissing
	}
	base := configstore.DefaultComplexityJevBaseURL
	if cfg != nil && cfg.BaseURL != "" {
		base = cfg.BaseURL
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("jev request encode failed: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(base, "/")+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("jev request build failed: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	httpResp, err := httpJevClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("jev request failed: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jev request returned %s", httpResp.Status)
	}
	parsed := &SystemOneResponse{}
	if err := json.NewDecoder(io.LimitReader(httpResp.Body, 1<<20)).Decode(parsed); err != nil {
		return nil, fmt.Errorf("jev response decode failed: %w", err)
	}
	return parsed, nil
}

// cloneJevConfig deep-copies a jev config so a snapshot read under the mutex
// stays immutable after release.
func cloneJevConfig(jev *configstore.ComplexityJevConfig) *configstore.ComplexityJevConfig {
	if jev == nil {
		return nil
	}
	clone := *jev
	return &clone
}
