package complexity

import (
	"math"
	"strings"
	"unicode"
)

// ComplexityInput is the normalized input for the analyzer.
// The caller is responsible for extracting text from request payloads.
type ComplexityInput struct {
	LastUserText   string   // last user message text
	PriorUserTexts []string // previous user message texts (up to 10)
	SystemText     string   // concatenated system/developer prompt text
}

// ComplexityResult holds the computed complexity scores and tier classification.
type ComplexityResult struct {
	// Weighted total score (0.0–1.0)
	Score float64
	// Computed tier: "SIMPLE", "MEDIUM", "COMPLEX", or "REASONING"
	Tier string

	// Individual dimension scores used in the weighted sum (0.0–1.0 each)
	CodePresence       float64
	ReasoningMarkers   float64
	TechnicalTerms     float64
	SimpleIndicators   float64
	TokenCount         float64
	ConversationCtx    float64
	SystemPromptSignal float64 // net weighted contribution from system lexical assist
	OutputComplexity   float64

	// Debug info — match counts per dimension (for logging, not exposed to CEL)
	CodeMatchCount      int
	ReasoningMatchCount int
	TechnicalMatchCount int
	SimpleMatchCount    int
	OutputMatchCount    int

	// Debug internals for eval/logging
	LastMessageScore    float64
	ConversationBlend   float64
	SimpleWeightApplied float64
	OutputFloorMinScore float64
	OutputFloorApplied  bool
	WordCount           int
}

// TierBoundaries defines the score thresholds for tier classification.
type TierBoundaries struct {
	SimpleMedium     float64 `json:"simple_medium"`
	MediumComplex    float64 `json:"medium_complex"`
	ComplexReasoning float64 `json:"complex_reasoning"`
}

// DefaultTierBoundaries returns the default tier boundary thresholds.
func DefaultTierBoundaries() TierBoundaries {
	return TierBoundaries{
		SimpleMedium:     0.15,
		MediumComplex:    0.35,
		ComplexReasoning: 0.60,
	}
}

// ComplexityAnalyzer computes complexity scores from normalized text input.
// It is stateless and safe for concurrent use.
type ComplexityAnalyzer struct {
	tierBoundaries TierBoundaries
}

// NewComplexityAnalyzer creates a new analyzer with the given tier boundaries.
// If boundaries is nil, default boundaries are used.
func NewComplexityAnalyzer(boundaries *TierBoundaries) *ComplexityAnalyzer {
	tb := DefaultTierBoundaries()
	if boundaries != nil {
		tb = *boundaries
	}
	return &ComplexityAnalyzer{tierBoundaries: tb}
}

// Analyze computes complexity scores from the normalized input.
func (a *ComplexityAnalyzer) Analyze(input ComplexityInput) *ComplexityResult {
	lastText := strings.ToLower(input.LastUserText)
	systemText := strings.ToLower(input.SystemText)

	// Primary message signals
	userCodeScore, codeCount := scoreKeywordDimension(lastText, codeKeywords, 3)
	reasoningScore, reasoningCount := scoreKeywordDimension(lastText, allReasoningKeywords, 2)
	userTechnicalScore, technicalCount := scoreKeywordDimension(lastText, technicalKeywords, 3)
	userSimpleScore, simpleCount := scoreKeywordDimension(lastText, simpleKeywords, 2)
	outputScore, outputCount := scoreOutputComplexity(lastText)
	tokenScore := scoreTokenCount(lastText)

	// System prompt provides soft lexical context for code/technical/simple signals,
	// but never drives reasoning override, token count, or output complexity.
	systemCodeScore, _ := scoreKeywordDimension(systemText, codeKeywords, 3)
	systemTechnicalScore, _ := scoreKeywordDimension(systemText, technicalKeywords, 3)
	systemSimpleScore, _ := scoreKeywordDimension(systemText, simpleKeywords, 2)

	codeScore := clamp(userCodeScore+(systemCodeScore*systemPromptAssistFactor), 0.0, 1.0)
	technicalScore := clamp(userTechnicalScore+(systemTechnicalScore*systemPromptAssistFactor), 0.0, 1.0)
	simpleScore := clamp(userSimpleScore+(systemSimpleScore*systemPromptAssistFactor), 0.0, 1.0)

	// Conditional simple dampener:
	// Only apply full dampener on short, low-signal asks
	wordCount := len(strings.Fields(lastText))
	effectiveSimpleWeight := simpleWeight
	signalCount := 0
	if userCodeScore >= 0.3 {
		signalCount++
	}
	if userTechnicalScore >= 0.3 {
		signalCount++
	}
	if reasoningScore >= 0.3 {
		signalCount++
	}
	if simpleCount > 0 && (wordCount >= 30 || signalCount >= 2) {
		effectiveSimpleWeight = 0.01
	}

	systemLexicalContribution := ((codeScore - userCodeScore) * codeWeight) +
		((technicalScore - userTechnicalScore) * technicalWeight) -
		((simpleScore - userSimpleScore) * effectiveSimpleWeight)

	// Weighted sum for last message (output complexity applied separately as a score floor)
	lastMsgScore := (codeScore * codeWeight) +
		(reasoningScore * reasoningWeight) +
		(technicalScore * technicalWeight) -
		(simpleScore * effectiveSimpleWeight) +
		(tokenScore * tokenCountWeight)
	lastMsgScore = clamp(lastMsgScore, 0.0, 1.0)

	// Conversation context score (prior user turns only)
	// Only blend when there is conversation history; otherwise use 100% last message score
	var blended float64
	var convScore float64
	if len(input.PriorUserTexts) > 0 {
		convScore = scoreConversationContext(input.PriorUserTexts)
		weightedBlend := (lastMsgScore * 0.6) + (convScore * 0.4)
		blended = math.Max(lastMsgScore, weightedBlend)
	} else {
		blended = lastMsgScore
	}

	// Output complexity as a score floor: strong output signals set a minimum score
	// This handles requests like "list every X and explain each" where the output
	// will be huge even though input keywords are sparse
	outputFloorMinScore := 0.0
	outputFloorApplied := false
	if outputScore > 0.5 {
		outputFloorMinScore = outputScore * 0.5
		if blended < outputFloorMinScore {
			blended = outputFloorMinScore
			outputFloorApplied = true
		}
	}

	finalScore := clamp(blended, 0.0, 1.0)

	// Tier classification with reasoning override
	strongCount := countPhraseMatches(lastText, strongReasoningKeywords)
	tier := a.classifyTier(finalScore)
	if strongCount >= 2 {
		tier = "REASONING"
	} else if strongCount >= 1 && (userCodeScore > 0.5 || userTechnicalScore > 0.5) {
		tier = "REASONING"
	}

	return &ComplexityResult{
		Score:               finalScore,
		Tier:                tier,
		CodePresence:        codeScore,
		ReasoningMarkers:    reasoningScore,
		TechnicalTerms:      technicalScore,
		SimpleIndicators:    simpleScore,
		TokenCount:          tokenScore,
		ConversationCtx:     convScore,
		SystemPromptSignal:  systemLexicalContribution,
		OutputComplexity:    outputScore,
		CodeMatchCount:      codeCount,
		ReasoningMatchCount: reasoningCount,
		TechnicalMatchCount: technicalCount,
		SimpleMatchCount:    simpleCount,
		OutputMatchCount:    outputCount,
		LastMessageScore:    lastMsgScore,
		ConversationBlend:   blended,
		SimpleWeightApplied: effectiveSimpleWeight,
		OutputFloorMinScore: outputFloorMinScore,
		OutputFloorApplied:  outputFloorApplied,
		WordCount:           wordCount,
	}
}

func (a *ComplexityAnalyzer) classifyTier(score float64) string {
	switch {
	case score < a.tierBoundaries.SimpleMedium:
		return "SIMPLE"
	case score < a.tierBoundaries.MediumComplex:
		return "MEDIUM"
	case score < a.tierBoundaries.ComplexReasoning:
		return "COMPLEX"
	default:
		return "REASONING"
	}
}

// --- Dimension weights ---

const (
	codeWeight               = 0.30
	reasoningWeight          = 0.25
	technicalWeight          = 0.25
	simpleWeight             = 0.05 // dampener, subtracted
	tokenCountWeight         = 0.10
	systemPromptAssistFactor = 0.25
	// Output complexity is applied as a score floor, not a weighted dimension
)

// --- Keyword lists ---
// CodePresence: implementation/code syntax/workflow signals
var codeKeywords = []string{
	"function", "class", "api", "database", "algorithm", "code", "implement",
	"debug", "error", "syntax", "compile", "runtime", "library", "framework",
	"variable", "loop", "array", "object", "method", "interface",
	"regex", "deploy", "docker", "sql", "query", "schema", "endpoint",
	"refactor", "bug", "parse", "async", "webhook", "migration",
	"ci/cd", "pipeline", "rest", "graphql", "test", "unit test",
	"python", "javascript", "typescript", "golang", "java", "ruby",
	"github actions", "monorepo", "aws cli", "config rule", "config rules",
	"retry", "fallback", "middleware", "patch", "diff", "pr", "pull request",
	"commit", "commit message", "behavior change",
	"cel", "auto-routing", "rwmutex", "goroutine",
}

// Reasoning markers — split into strong and weak for override logic
var strongReasoningKeywords = []string{
	"step by step", "think through", "tradeoffs", "pros and cons",
	"justify", "critique", "implications", "explain why",
	"root cause analysis", "reconstruct the sequence",
	"reconstruct the most likely sequence", "what should have happened instead",
	"explain your reasoning", "weigh the tradeoffs", "recommend a design",
}

var weakReasoningKeywords = []string{
	"reason", "analyze", "evaluate", "compare", "assess", "consider",
	"why does", "what if", "how would", "what are the", "which approach",
	"think about", "design", "most likely", "reconstruct", "verify",
	"assumption", "hypothesis", "compare and contrast", "weigh the options",
	"recommend one", "given these constraints", "under these constraints",
}

// allReasoningKeywords is the combined list used for dimension scoring
var allReasoningKeywords = append(append([]string{}, strongReasoningKeywords...), weakReasoningKeywords...)

// TechnicalTerms: architecture/distributed/security/infrastructure signals
var technicalKeywords = []string{
	"architecture", "distributed", "encryption", "authentication", "scalability",
	"microservices", "kubernetes", "infrastructure", "protocol", "latency",
	"throughput", "concurrency", "optimization", "load balancer", "caching",
	"sharding", "replication", "consensus", "mutex", "deadlock",
	"race condition", "api gateway", "terraform", "observability",
	"access token", "refresh token", "rbac", "sso", "oidc", "saml",
	"tenant", "multi-tenant", "audit log", "failover", "idempotency",
	"zero downtime", "incident", "outage", "postmortem", "root cause",
	"telemetry", "metrics", "configmap", "connection pool", "payment processing",
	"saas", "feature flag", "operational risk", "vendor lock-in",
	"s3 bucket", "misconfiguration", "remediation", "oltp", "olap",
	"ledger", "metering", "aggregation", "proration", "credits", "dunning",
	"invoice", "invoice generation", "double-entry", "reconciliation",
	"chart of accounts", "hipaa", "quarantine workflow", "retention policy",
	"audit trail", "pre-signed url", "entitlements", "seat limits",
	"usage quotas", "deprovisioning", "permission drift", "role mapping",
	"fraud detection", "manual review", "feedback loop",
	"model serving", "a/b testing", "identity resolution",
	"deterministic replay", "tamper evidence", "hash chain",
	"approval workflow", "vpc", "soc 2", "data residency",
	"disaster recovery", "data race", "struct copy", "hybrid search",
}

// SimpleIndicators: signals for trivial/greeting-type requests
var simpleKeywords = []string{
	"what is", "define", "hello", "hi", "thanks", "how do i spell",
	"translate", "what does", "who is", "when was", "tell me about",
	"good morning", "good night", "how are you", "simple", "brief",
	"short", "quick", "beginner", "basic", "concise",
}

// --- Output complexity keywords ---

var enumTriggers = []string{
	"list every", "list all", "enumerate all", "all possible",
	"every single", "show all", "name all", "give me all",
}

var comprehensivenessMarkers = []string{
	"comprehensive", "exhaustive", "complete list", "full list",
	"in detail", "detailed breakdown", "thorough", "in-depth",
}

var elaborationMarkers = []string{
	"and what it does", "explain each", "describe each", "for each",
	"with examples", "with descriptions", "along with",
}

var limitingQualifiers = []string{
	"briefly", "top 3", "top 5", "top 10", "in one sentence",
	"quickly", "summarize", "just the", "only the", "keep it short",
	"tl;dr", "tldr",
}

// --- Scoring functions ---

// scoreKeywordDimension counts keyword matches and returns a normalized score and match count.
// Multi-word phrases are matched as substrings. Single words use word-boundary matching.
func scoreKeywordDimension(text string, keywords []string, capAt int) (float64, int) {
	count := countPhraseMatches(text, keywords)
	score := math.Min(1.0, float64(count)/float64(capAt))
	return score, count
}

// countPhraseMatches counts how many keywords/phrases appear in the text.
// Multi-word phrases use substring matching. Single words use word-boundary matching.
func countPhraseMatches(text string, keywords []string) int {
	count := 0
	for _, kw := range keywords {
		if strings.Contains(kw, " ") {
			// Multi-word phrase: substring match
			if strings.Contains(text, kw) {
				count++
			}
		} else {
			// Single word: word-boundary match
			if containsWord(text, kw) {
				count++
			}
		}
	}
	return count
}

// containsWord checks if a word appears in text delimited by non-alphanumeric boundaries.
func containsWord(text, word string) bool {
	idx := 0
	for {
		pos := strings.Index(text[idx:], word)
		if pos == -1 {
			return false
		}
		start := idx + pos
		end := start + len(word)

		startOk := start == 0 || !isWordChar(rune(text[start-1]))
		endOk := end == len(text) || !isWordChar(rune(text[end]))

		if startOk && endOk {
			return true
		}
		idx = start + 1
		if idx >= len(text) {
			return false
		}
	}
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// scoreTokenCount scores based on word count of the text.
func scoreTokenCount(text string) float64 {
	words := len(strings.Fields(text))
	switch {
	case words < 15:
		return float64(words) / 15.0 * 0.3
	case words <= 400:
		return 0.3 + float64(words-15)/385.0*0.4
	default:
		extra := math.Min(0.3, float64(words-400)/600.0*0.3)
		return 0.7 + extra
	}
}

// scoreOutputComplexity scores based on explicit enumeration/comprehensiveness signals.
// Absence of limiting qualifiers does NOT boost score — only explicit phrases contribute.
func scoreOutputComplexity(text string) (float64, int) {
	enumCount := countPhraseMatches(text, enumTriggers)
	compCount := countPhraseMatches(text, comprehensivenessMarkers)
	elabCount := countPhraseMatches(text, elaborationMarkers)

	totalCount := enumCount + compCount + elabCount
	if totalCount == 0 {
		return 0.0, 0
	}

	enumScore := math.Min(1.0, float64(enumCount))
	compScore := math.Min(1.0, float64(compCount))
	elabScore := math.Min(1.0, float64(elabCount))

	rawScore := (enumScore * 0.4) + (compScore * 0.3) + (elabScore * 0.3)

	// Limiting qualifiers dampen the score
	if countPhraseMatches(text, limitingQualifiers) > 0 {
		rawScore *= 0.3
	}

	return math.Min(1.0, rawScore), totalCount
}

// scoreConversationContext scores prior user messages using the same keyword approach.
// Only user turns are scored; assistant turns are excluded to prevent upward drift.
func scoreConversationContext(priorUserTexts []string) float64 {
	if len(priorUserTexts) == 0 {
		return 0.0
	}

	// Cap at 10 most recent
	texts := priorUserTexts
	if len(texts) > 10 {
		texts = texts[len(texts)-10:]
	}

	var totalScore float64
	for _, text := range texts {
		lower := strings.ToLower(text)
		code, _ := scoreKeywordDimension(lower, codeKeywords, 3)
		tech, _ := scoreKeywordDimension(lower, technicalKeywords, 3)
		reasoning, _ := scoreKeywordDimension(lower, allReasoningKeywords, 2)
		// Average of the three main signal dimensions for context
		msgScore := (code*codeWeight + tech*technicalWeight + reasoning*reasoningWeight) /
			(codeWeight + technicalWeight + reasoningWeight)
		totalScore += msgScore
	}

	return math.Min(1.0, totalScore/float64(len(texts)))
}

func clamp(val, min, max float64) float64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}
