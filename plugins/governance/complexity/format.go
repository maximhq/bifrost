package complexity

import (
	"encoding/json"
	"fmt"
	"strings"
)

// FormatLog renders a human-readable, multi-line summary of the complexity
// analysis for the routing engine log. The primary line carries
// tier/score/word-count/non-zero match counts; conditional annotation lines
// are appended only when they actually apply (tier override, output floor,
// referential followup, conversation blending). The full scoring internals
// (dimensions, contributions, dampener) are emitted separately by FormatDebug
// for engineer-level debug logs.
func FormatLog(result *ComplexityResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Complexity: tier=%s score=%.2f words=%d matches=[%s]",
		result.Tier, result.Score, result.WordCount,
		formatMatchCounts(result))

	if result.TierOverrideReason != "" {
		fmt.Fprintf(&b, "\n  └─ tier override: %s (%d reasoning keyword match%s → promoted to REASONING)",
			result.TierOverrideReason, result.ReasoningMatchCount,
			pluralS(result.ReasoningMatchCount))
	}
	if result.OutputFloorApplied {
		fmt.Fprintf(&b, "\n  └─ output-floor applied: strong output signals set minimum score %.2f",
			result.OutputFloorMinScore)
	}
	if result.ReferentialFollowup {
		fmt.Fprintf(&b, "\n  └─ referential-followup: short ask interpreted as continuation of prior turn")
	}
	if result.ConversationBlend > result.LastMessageScore+0.02 {
		fmt.Fprintf(&b, "\n  └─ conversation-blended: last-message %.2f → %.2f (prior turns pulled score up)",
			result.LastMessageScore, result.ConversationBlend)
	}
	return b.String()
}

// FormatDebug renders the full analysis payload as JSON for server-side
// debug logs. Engineers retain full visibility via this format without
// leaking internals into the user-facing routing-engine log drawer.
func FormatDebug(result *ComplexityResult) string {
	payload := struct {
		Score      float64 `json:"score"`
		Tier       string  `json:"tier"`
		Dimensions struct {
			CodePresence       float64 `json:"code_presence"`
			ReasoningMarkers   float64 `json:"reasoning_markers"`
			TechnicalTerms     float64 `json:"technical_terms"`
			SimpleIndicators   float64 `json:"simple_indicators"`
			TokenCount         float64 `json:"token_count"`
			ConversationCtx    float64 `json:"conversation_context"`
			SystemPromptSignal float64 `json:"system_prompt_signal"`
			OutputComplexity   float64 `json:"output_complexity"`
		} `json:"dimensions"`
		Contribs struct {
			Code          float64 `json:"code"`
			Reasoning     float64 `json:"reasoning"`
			Technical     float64 `json:"technical"`
			SimplePenalty float64 `json:"simple_penalty"`
			TokenCount    float64 `json:"token_count"`
		} `json:"contributions"`
		MatchCounts struct {
			Code      int `json:"code"`
			Reasoning int `json:"reasoning"`
			Technical int `json:"technical"`
			Simple    int `json:"simple"`
			Output    int `json:"output"`
		} `json:"match_counts"`
		Debug struct {
			WordCount           int     `json:"word_count"`
			LastMessageScore    float64 `json:"last_message_score"`
			BlendedScore        float64 `json:"blended_score"`
			ReferentialFollowup bool    `json:"referential_followup"`
			SimpleWeightApplied float64 `json:"simple_weight_applied"`
			OutputFloorMinScore float64 `json:"output_floor_min_score"`
			OutputFloorApplied  bool    `json:"output_floor_applied"`
			TierOverrideReason  string  `json:"tier_override_reason,omitempty"`
		} `json:"debug"`
	}{
		Score: result.Score,
		Tier:  result.Tier,
	}
	payload.Dimensions.CodePresence = result.CodePresence
	payload.Dimensions.ReasoningMarkers = result.ReasoningMarkers
	payload.Dimensions.TechnicalTerms = result.TechnicalTerms
	payload.Dimensions.SimpleIndicators = result.SimpleIndicators
	payload.Dimensions.TokenCount = result.TokenCount
	payload.Dimensions.ConversationCtx = result.ConversationCtx
	payload.Dimensions.SystemPromptSignal = result.SystemPromptSignal
	payload.Dimensions.OutputComplexity = result.OutputComplexity

	payload.Contribs.Code = result.Contributions.Code
	payload.Contribs.Reasoning = result.Contributions.Reasoning
	payload.Contribs.Technical = result.Contributions.Technical
	payload.Contribs.SimplePenalty = result.Contributions.SimplePenalty
	payload.Contribs.TokenCount = result.Contributions.TokenCount

	payload.MatchCounts.Code = result.CodeMatchCount
	payload.MatchCounts.Reasoning = result.ReasoningMatchCount
	payload.MatchCounts.Technical = result.TechnicalMatchCount
	payload.MatchCounts.Simple = result.SimpleMatchCount
	payload.MatchCounts.Output = result.OutputMatchCount

	payload.Debug.WordCount = result.WordCount
	payload.Debug.LastMessageScore = result.LastMessageScore
	payload.Debug.BlendedScore = result.ConversationBlend
	payload.Debug.ReferentialFollowup = result.ReferentialFollowup
	payload.Debug.SimpleWeightApplied = result.SimpleWeightApplied
	payload.Debug.OutputFloorMinScore = result.OutputFloorMinScore
	payload.Debug.OutputFloorApplied = result.OutputFloorApplied
	payload.Debug.TierOverrideReason = result.TierOverrideReason

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("marshal error=%v", err)
	}
	return string(encoded)
}

// formatMatchCounts renders the non-zero keyword match counts for the primary
// log line. Zero-valued dimensions are omitted to reduce noise; when every
// count is zero we emit "none" so the reader can tell the analyzer ran and
// found nothing rather than being silently skipped.
func formatMatchCounts(result *ComplexityResult) string {
	parts := make([]string, 0, 5)
	if result.CodeMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("code:%d", result.CodeMatchCount))
	}
	if result.ReasoningMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("reasoning:%d", result.ReasoningMatchCount))
	}
	if result.TechnicalMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("technical:%d", result.TechnicalMatchCount))
	}
	if result.SimpleMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("simple:%d", result.SimpleMatchCount))
	}
	if result.OutputMatchCount > 0 {
		parts = append(parts, fmt.Sprintf("output:%d", result.OutputMatchCount))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}
