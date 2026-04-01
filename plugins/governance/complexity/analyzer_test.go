package complexity

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnalyze_Simple(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "What is 2+2?",
	})

	if result.Tier != "SIMPLE" {
		t.Errorf("expected SIMPLE tier for 'What is 2+2?', got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_Hello(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Hello, how are you?",
	})

	if result.Tier != "SIMPLE" {
		t.Errorf("expected SIMPLE tier for greeting, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_CodeRequest(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Write a Python quicksort function that handles arrays with duplicate elements",
	})

	if result.Tier != "MEDIUM" && result.Tier != "COMPLEX" {
		t.Errorf("expected MEDIUM or COMPLEX tier for code request, got %s (score=%.3f)", result.Tier, result.Score)
	}
	if result.CodePresence <= 0 {
		t.Errorf("expected positive CodePresence score, got %.3f", result.CodePresence)
	}
}

func TestAnalyze_Complex(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Design a distributed authentication system using Kubernetes with encryption and load balancer",
	})

	if result.Tier != "COMPLEX" && result.Tier != "REASONING" {
		t.Errorf("expected COMPLEX or REASONING tier for architecture request, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_Reasoning(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Think step by step through the tradeoffs of this ML architecture and explain why one approach is better",
	})

	if result.Tier != "REASONING" {
		t.Errorf("expected REASONING tier for deep reasoning request, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_OutputComplexity(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "List every AWS service and explain each one with examples",
	})

	if result.OutputComplexity <= 0 {
		t.Errorf("expected positive OutputComplexity score, got %.3f", result.OutputComplexity)
	}
	if result.Tier == "SIMPLE" {
		t.Errorf("expected non-SIMPLE tier for output-heavy request, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_OutputComplexityWithLimiter(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	full := a.Analyze(ComplexityInput{
		LastUserText: "List every programming language and explain each",
	})
	limited := a.Analyze(ComplexityInput{
		LastUserText: "Briefly list every programming language",
	})

	if limited.OutputComplexity >= full.OutputComplexity {
		t.Errorf("expected limiting qualifier to reduce output complexity: full=%.3f, limited=%.3f",
			full.OutputComplexity, limited.OutputComplexity)
	}
}

func TestAnalyze_ContributionFieldsStayInSync(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Write a Python function that compares mutexes and channels in Go step by step",
	})

	if result.Contributions.Code != result.CodePresence*codeWeight {
		t.Fatalf("expected code contribution %.6f, got %.6f", result.CodePresence*codeWeight, result.Contributions.Code)
	}
	if result.Contributions.Reasoning != result.ReasoningMarkers*reasoningWeight {
		t.Fatalf("expected reasoning contribution %.6f, got %.6f", result.ReasoningMarkers*reasoningWeight, result.Contributions.Reasoning)
	}
	if result.Contributions.Technical != result.TechnicalTerms*technicalWeight {
		t.Fatalf("expected technical contribution %.6f, got %.6f", result.TechnicalTerms*technicalWeight, result.Contributions.Technical)
	}
	if result.Contributions.SimplePenalty != -(result.SimpleIndicators * result.SimpleWeightApplied) {
		t.Fatalf("expected simple penalty %.6f, got %.6f", -(result.SimpleIndicators * result.SimpleWeightApplied), result.Contributions.SimplePenalty)
	}
	if result.Contributions.TokenCount != result.TokenCount*tokenCountWeight {
		t.Fatalf("expected token contribution %.6f, got %.6f", result.TokenCount*tokenCountWeight, result.Contributions.TokenCount)
	}
}

func TestAnalyze_ConversationContext(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	// Short follow-up with no context → SIMPLE
	noCtx := a.Analyze(ComplexityInput{
		LastUserText: "Why?",
	})

	// Same follow-up with technical conversation history → higher score
	withCtx := a.Analyze(ComplexityInput{
		LastUserText: "Why?",
		PriorUserTexts: []string{
			"How does the distributed authentication system handle encryption?",
			"What about the kubernetes infrastructure for microservices?",
			"Can you explain the concurrency model and mutex usage?",
		},
	})

	if withCtx.Score <= noCtx.Score {
		t.Errorf("expected conversation context to raise score: noCtx=%.3f, withCtx=%.3f",
			noCtx.Score, withCtx.Score)
	}
}

func TestAnalyze_ConversationContextDoesNotDiluteStrongLastMessage(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	lastTurnOnly := a.Analyze(ComplexityInput{
		LastUserText: "Design the target architecture for migrating our monolith checkout service to an event-driven system. Cover the event schema, consumer topology, idempotency strategy, and a phased data migration plan that maintains zero downtime.",
	})

	withCtx := a.Analyze(ComplexityInput{
		LastUserText: "Design the target architecture for migrating our monolith checkout service to an event-driven system. Cover the event schema, consumer topology, idempotency strategy, and a phased data migration plan that maintains zero downtime.",
		PriorUserTexts: []string{
			"We're hitting scaling limits with our monolithic checkout service.",
			"Current throughput is 500 TPS but we need 5,000 TPS by Q3.",
			"We're considering event sourcing but worried about operational complexity.",
		},
	})

	if withCtx.Score < lastTurnOnly.Score {
		t.Errorf("expected context-aware score to preserve or raise final score: lastOnly=%.3f, withCtx=%.3f",
			lastTurnOnly.Score, withCtx.Score)
	}
	if withCtx.ConversationBlend < withCtx.LastMessageScore {
		t.Errorf("expected conversation blend to be non-dilutive: lastMsg=%.3f, blended=%.3f",
			withCtx.LastMessageScore, withCtx.ConversationBlend)
	}
}

func TestAnalyze_ReferentialFollowupLiftsShortTechnicalContinuation(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "do it",
		PriorUserTexts: []string{
			"We need to refactor the retry middleware so only 429 and 408 retry.",
			"Move fallback selection after request classification and keep the behavior change explicit in the PR.",
			"Update the Go tests for the CEL routing rules and the governance plugin.",
		},
	})

	if !result.ReferentialFollowup {
		t.Fatalf("expected referential path to trigger for short technical continuation")
	}
	if result.Tier == "SIMPLE" {
		t.Fatalf("expected short referential follow-up to lift above SIMPLE, got %s (score=%.3f)", result.Tier, result.Score)
	}
	if result.Score < DefaultTierBoundaries().SimpleMedium {
		t.Fatalf("expected score above SIMPLE threshold, got %.3f", result.Score)
	}
}

func TestAnalyze_ReferentialFollowupRequiresRealContext(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "do it",
	})

	if result.ReferentialFollowup {
		t.Fatalf("expected no referential path without prior context")
	}
	if result.Tier != "SIMPLE" {
		t.Fatalf("expected SIMPLE tier without prior context, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_TaskShiftFollowupDoesNotUseReferentialLift(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "translate this to spanish",
		PriorUserTexts: []string{
			"We need to debug the Kubernetes deployment and fix the authentication middleware.",
			"The RBAC mapping for SAML tenants is failing after the migration.",
		},
	})

	if result.ReferentialFollowup {
		t.Fatalf("expected task-shift request not to take referential path")
	}
	if result.Score >= DefaultTierBoundaries().MediumComplex {
		t.Fatalf("expected task-shift request to stay below COMPLEX threshold, got %.3f", result.Score)
	}
}

func TestAnalyze_LimitingTaskShiftDoesNotUseReferentialLift(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "summarize it in one sentence",
		PriorUserTexts: []string{
			"Design a multi-tenant billing ledger with metering, proration, credits, and invoice generation.",
			"Include the data model and monthly aggregation flow.",
		},
	})

	if result.ReferentialFollowup {
		t.Fatalf("expected limiting summary request not to take referential path")
	}
	if result.Score >= DefaultTierBoundaries().MediumComplex {
		t.Fatalf("expected limiting summary request to stay below COMPLEX threshold, got %.3f", result.Score)
	}
}

func TestAnalyze_RecentContextOutweighsOlderContext(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	recentTech := a.Analyze(ComplexityInput{
		LastUserText: "do it",
		PriorUserTexts: []string{
			"Hello there.",
			"Thanks.",
			"Design a distributed authentication system with RBAC, OIDC, and regional failover.",
		},
	})

	olderTech := a.Analyze(ComplexityInput{
		LastUserText: "do it",
		PriorUserTexts: []string{
			"Design a distributed authentication system with RBAC, OIDC, and regional failover.",
			"Hello there.",
			"Thanks.",
		},
	})

	if recentTech.Score <= olderTech.Score {
		t.Fatalf("expected more recent technical context to matter more: recent=%.3f older=%.3f",
			recentTech.Score, olderTech.Score)
	}
}

func TestAnalyze_SystemPromptBoost(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	base := a.Analyze(ComplexityInput{
		LastUserText: "Review this code for issues",
	})

	boosted := a.Analyze(ComplexityInput{
		LastUserText: "Review this code for issues",
		SystemText:   "You are a security engineer responsible for RBAC, audit log reviews, and OIDC policy.",
	})

	if boosted.Score <= base.Score {
		t.Errorf("expected system prompt to boost score: base=%.3f, boosted=%.3f",
			base.Score, boosted.Score)
	}
}

func TestAnalyze_SystemPromptDampener(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	base := a.Analyze(ComplexityInput{
		LastUserText: "Explain how databases work",
	})

	dampened := a.Analyze(ComplexityInput{
		LastUserText: "Explain how databases work",
		SystemText:   "You are a beginner tutor. Keep answers simple, brief, and concise.",
	})

	if dampened.Score >= base.Score {
		t.Errorf("expected system prompt to dampen score: base=%.3f, dampened=%.3f",
			base.Score, dampened.Score)
	}
}

func TestAnalyze_SystemPromptLexicalAssistDoesNotOverPromoteSimpleWebhook(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "What is a webhook?",
		SystemText:   "You are responsible for RBAC, audit log controls, and OIDC integration policy.",
	})

	if result.Tier != "SIMPLE" {
		t.Errorf("expected SIMPLE tier for webhook definition with technical system prompt, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_EmptyInput(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{})

	if result.Tier != "SIMPLE" {
		t.Errorf("expected SIMPLE tier for empty input, got %s", result.Tier)
	}
	if result.Score != 0.0 {
		t.Errorf("expected 0.0 score for empty input, got %.3f", result.Score)
	}
}

func TestAnalyze_ReasoningOverrideNotTooEager(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	// Two weak reasoning markers should NOT force REASONING
	result := a.Analyze(ComplexityInput{
		LastUserText: "Why does React re-render, and what if I use useMemo?",
	})

	if result.Tier == "REASONING" {
		t.Errorf("expected non-REASONING tier for casual question with weak markers, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_SimpleDampenerConditional(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	// "What is" + technical term should not be over-dampened
	result := a.Analyze(ComplexityInput{
		LastUserText: "What is eventual consistency in distributed systems with sharding?",
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected non-SIMPLE tier for technical 'what is' question, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_AccessVsRefreshTokens(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Explain the difference between an access token and a refresh token. When would you use short-lived vs long-lived tokens?",
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected MEDIUM or higher tier for token lifecycle question, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_OutageCustomerCommunication(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Draft a short outage notification email for our enterprise customers. Our payment processing was down for 23 minutes this morning between 09:12 and 09:35 UTC. No transactions were lost but some were delayed.",
		SystemText:   "You are a customer success manager for a B2B SaaS platform. You help draft professional and empathetic communications to enterprise customers.",
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected MEDIUM or higher tier for outage communication prompt, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_MultiTenantSSOArchitecture(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Design a multi-tenant authentication service for a SaaS platform on Kubernetes. Requirements: RBAC with custom roles per tenant, audit logging for all auth events, regional failover across two AWS regions, and support for both SAML 2.0 and OIDC enterprise SSO. Include the data model and the request flow for a login.",
	})

	if result.Tier != "COMPLEX" && result.Tier != "REASONING" {
		t.Errorf("expected COMPLEX or REASONING tier for multi-tenant SSO architecture prompt, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_PostIncidentReconstruction(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Given this partial timeline with a 15-minute telemetry gap, reconstruct the most likely sequence of failures. Why did connection pool exhaustion happen? Why didn't the ConfigMap fix work, and what should the on-call have done instead? What might have happened during the metrics blackout that we can't directly observe? Identify the weakest assumptions in your reconstruction and flag what we'd need to verify.",
		PriorUserTexts: []string{
			"The outage lasted 47 minutes and affected all US-East customers. Revenue impact was approximately $180,000.",
			"Timeline: 14:03 - alerts fired for elevated 5xx rates on the API gateway. 14:15 - identified database connection pool exhaustion on the primary Postgres cluster.",
			"At 14:22 the on-call attempted to scale up the connection pool via a ConfigMap change, but the change didn't take effect because our pods require a restart to pick up ConfigMap changes.",
		},
		SystemText: "You are leading the post-incident review for a major production outage at a multi-region SaaS company.",
	})

	if result.Tier != "COMPLEX" && result.Tier != "REASONING" {
		t.Errorf("expected COMPLEX or REASONING tier for post-incident reconstruction, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_CodingFollowupsWithTechnicalContext(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	tests := []struct {
		name         string
		lastUserText string
		prior        []string
	}{
		{
			name:         "explain_changes_for_pr",
			lastUserText: "Can you explain the changes in plain English for the PR description and call out the behavior change?",
			prior: []string{
				"I'm working on a Go gateway and just changed our retry middleware so it stops retrying most 4xx responses.",
				"I added an allowlist so only 429 and 408 still retry, and I moved the fallback logic after the classification step.",
			},
		},
		{
			name:         "summarize_refactor",
			lastUserText: "Can you summarize the refactor for the PR in a few bullets and highlight the behavior changes?",
			prior: []string{
				"I split our request parsing code into a transport-specific extractor layer and a pure analyzer package so the heuristics don't depend on raw HTTP payload shapes.",
				"I also moved provider-shape branching into the governance plugin, added tests for OpenAI Responses input_text, and stopped unsupported requests from defaulting to SIMPLE.",
			},
		},
		{
			name:         "write_commit_message",
			lastUserText: "Can you write the commit message for this patch?",
			prior: []string{
				"I changed the retry middleware so it stops retrying most 4xx responses.",
				"I added an allowlist for retryable statuses and moved fallback selection after the classification step.",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := a.Analyze(ComplexityInput{
				LastUserText:   tt.lastUserText,
				PriorUserTexts: tt.prior,
			})

			if result.Tier == "SIMPLE" {
				t.Errorf("expected MEDIUM or higher tier for coding follow-up, got %s (score=%.3f)",
					result.Tier, result.Score)
			}
		})
	}
}

func TestAnalyze_GitHubActionsWorkflow(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Write a GitHub Actions workflow that detects which services changed in a PR and only runs the tests for those services.",
		PriorUserTexts: []string{
			"I'm setting up CI/CD for the first time for our monorepo.",
			"We use GitHub Actions and each service has its own go.mod and test suite.",
		},
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected MEDIUM or higher tier for GitHub Actions workflow request, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_BillingLedgerPipeline(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Design a usage-based billing pipeline covering metering, aggregation, proration, credits, dunning, and invoice generation. Include the data model for the ledger and the sequence flow for generating a monthly invoice.",
		SystemText:   "You are a staff engineer for a B2B SaaS billing platform.",
	})

	if result.Tier != "COMPLEX" && result.Tier != "REASONING" {
		t.Errorf("expected COMPLEX or REASONING tier for billing ledger pipeline prompt, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_VectorDatabaseTradeoffRecommendation(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Compare self-hosted Qdrant vs managed Pinecone for a hybrid search system serving 1,000 QPS with 50M vectors. We're in a regulated industry - no data can leave our VPC, and we need SOC 2 attestation for all data stores. Weigh the tradeoffs around data residency compliance, operational burden for a 4-person infra team, query latency at scale, cost scaling characteristics, and disaster recovery options. Recommend one and explain your reasoning.",
	})

	if result.Tier != "REASONING" {
		t.Errorf("expected REASONING tier for vector database tradeoff recommendation, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

// TestAnalyze_UserReasoningKeywordsTriggerOverride proves that reasoning_keywords
// from EditableKeywordConfig wire into StrongReasoningKeywords (the tier-override
// gate) — a user supplying "quantum teleportation" as a reasoning keyword must be
// able to force REASONING tier when that phrase appears twice in the input, even
// though the default analyzer has no opinion on it.
func TestAnalyze_UserReasoningKeywordsTriggerOverride(t *testing.T) {
	cfg := DefaultAnalyzerConfig()
	cfg.Keywords = EditableKeywordConfig{
		CodeKeywords:      []string{"function"},
		ReasoningKeywords: []string{"quantum teleportation", "bell inequality"},
		TechnicalKeywords: []string{"server"},
		SimpleKeywords:    []string{"what is"},
	}

	a := NewComplexityAnalyzerWithConfig(&cfg)

	// Two distinct user-supplied reasoning phrases must force REASONING, because
	// MergeOntoDefaults maps reasoning_keywords → StrongReasoningKeywords and the
	// strong-reasoning override fires at strongCount >= 2.
	result := a.Analyze(ComplexityInput{
		LastUserText: "Walk me through quantum teleportation and then relate it to the bell inequality.",
	})

	if result.Tier != "REASONING" {
		t.Errorf("expected REASONING tier from user-supplied reasoning keyword override, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_CustomTierBoundaries(t *testing.T) {
	input := ComplexityInput{
		LastUserText: "Write a function",
	}

	defaultAnalyzer := NewComplexityAnalyzer(nil)
	customAnalyzer := NewComplexityAnalyzer(&TierBoundaries{
		SimpleMedium:     0.10,
		MediumComplex:    0.30,
		ComplexReasoning: 0.50,
	})

	defaultResult := defaultAnalyzer.Analyze(input)
	customResult := customAnalyzer.Analyze(input)

	if defaultResult.Score < 0.10 || defaultResult.Score >= 0.15 {
		t.Fatalf("expected test fixture score to stay between default and custom simple/medium cutoffs, got %.3f", defaultResult.Score)
	}

	if defaultResult.Tier != "SIMPLE" {
		t.Fatalf("expected default analyzer to keep the fixture in SIMPLE, got %s (score=%.3f)", defaultResult.Tier, defaultResult.Score)
	}

	if customResult.Tier != "MEDIUM" {
		t.Fatalf("expected custom analyzer to reclassify the same fixture as MEDIUM, got %s (score=%.3f)", customResult.Tier, customResult.Score)
	}

	if defaultResult.Tier == customResult.Tier {
		t.Fatalf("expected custom tier boundaries to change classification for the same ComplexityInput; default=%s custom=%s score=%.3f", defaultResult.Tier, customResult.Tier, customResult.Score)
	}
}

func TestNewComplexityAnalyzer_InvalidBoundariesFallBackToDefaults(t *testing.T) {
	a := NewComplexityAnalyzer(&TierBoundaries{})

	result := a.Analyze(ComplexityInput{
		LastUserText: "What is 2+2?",
	})

	if result.Tier != "SIMPLE" {
		t.Fatalf("expected invalid constructor boundaries to fall back to defaults, got tier %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestNewComplexityAnalyzerWithConfig_InvalidConfigFallsBackToDefaults(t *testing.T) {
	cfg := DefaultAnalyzerConfig()
	cfg.TierBoundaries = TierBoundaries{
		SimpleMedium:     0.60,
		MediumComplex:    0.30,
		ComplexReasoning: 0.50,
	}

	a := NewComplexityAnalyzerWithConfig(&cfg)
	result := a.Analyze(ComplexityInput{
		LastUserText: "What is 2+2?",
	})

	if result.Tier != "SIMPLE" {
		t.Fatalf("expected invalid constructor config to fall back to defaults, got tier %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestIsReferentialFollowup_GuardBranches(t *testing.T) {
	tests := []struct {
		name         string
		lastText     string
		lastMsgScore float64
		convScore    float64
		wordCount    int
		expected     bool
	}{
		{"phrase_match_ok", "do it", 0.05, 0.30, 2, true},
		{"phrase_match_at_word_cap", "do it now please right away", 0.05, 0.30, 6, true},
		{"phrase_match_over_word_cap", "do it now please right away ok", 0.05, 0.30, 7, false},
		{"phrase_match_zero_words", "", 0.0, 0.30, 0, false},
		{"phrase_match_score_at_threshold", "do it", 0.15, 0.30, 2, false},
		{"phrase_match_score_just_below_threshold", "do it", 0.149, 0.30, 2, true},
		{"phrase_match_conv_just_below_threshold", "do it", 0.05, 0.199, 2, false},
		{"phrase_match_conv_at_threshold", "do it", 0.05, 0.20, 2, true},
		{"task_shift_blocks_phrase_match", "translate it", 0.05, 0.30, 2, false},
		{"task_shift_blocks_summarize", "summarize it", 0.05, 0.30, 2, false},
		{"task_shift_one_sentence_blocks", "rewrite it in one sentence", 0.05, 0.30, 5, false},
		{"multi_signal_fix_it", "fix it", 0.05, 0.30, 2, true},
		{"multi_signal_make_it_shorter", "make it shorter", 0.05, 0.30, 3, true},
		{"multi_signal_rewrite_it", "rewrite it", 0.05, 0.30, 2, true},
		{"multi_signal_use_that", "use that", 0.05, 0.30, 2, true},
		{"multi_signal_answer_previous", "answer the previous question", 0.05, 0.30, 4, true},
		{"action_only_no_deictic", "fix the race condition", 0.05, 0.30, 4, false},
		{"deictic_only_no_action", "this is great", 0.05, 0.30, 3, false},
		{"unrelated_short_text", "hello there friend", 0.05, 0.30, 3, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := newCompiledKeywordMatcher(defaultFullKeywordConfig())
			signals := matcher.analyzeText(tt.lastText, lastTextFullScanMask)
			got := isReferentialFollowup(signals, tt.lastMsgScore, tt.convScore, tt.wordCount)
			if got != tt.expected {
				t.Errorf("isReferentialFollowup(%q, last=%.3f, conv=%.3f, words=%d) = %v, want %v",
					tt.lastText, tt.lastMsgScore, tt.convScore, tt.wordCount, got, tt.expected)
			}
		})
	}
}

func TestAnalyze_ReferentialMultiSignalDetection(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	techPriors := []string{
		"We need to refactor the retry middleware so only 429 and 408 retry.",
		"Move fallback selection after request classification and keep the behavior change explicit in the PR.",
		"Update the Go tests for the CEL routing rules and the governance plugin.",
	}

	tests := []struct {
		name     string
		lastText string
	}{
		{"fix_it", "fix it"},
		{"make_it_shorter", "make it shorter"},
		{"rewrite_it", "rewrite it"},
		{"do_this", "do this"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := a.Analyze(ComplexityInput{
				LastUserText:   tt.lastText,
				PriorUserTexts: techPriors,
			})
			if !result.ReferentialFollowup {
				t.Fatalf("expected multi-signal detection to fire for %q (last=%.3f, conv=%.3f)",
					tt.lastText, result.LastMessageScore, result.ConversationCtx)
			}
			if result.Tier == "SIMPLE" {
				t.Fatalf("expected lift above SIMPLE for %q, got %s (score=%.3f)",
					tt.lastText, result.Tier, result.Score)
			}
		})
	}
}

func TestAnalyze_ReferentialPhraseDoesNotHijackStrongAsk(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "use option 2 to design the distributed consensus algorithm with kubernetes and rbac",
		PriorUserTexts: []string{
			"We need to refactor the retry middleware so only 429 and 408 retry.",
		},
	})

	if result.ReferentialFollowup {
		t.Fatalf("expected high-signal message to bypass referential path despite phrase match (last=%.3f)",
			result.LastMessageScore)
	}
}

func TestAnalyze_RegressionAnchors(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	techPriors := []string{
		"We need to refactor the retry middleware so only 429 and 408 retry.",
		"Move fallback selection after request classification and keep the behavior change explicit in the PR.",
		"Update the Go tests for the CEL routing rules and the governance plugin.",
	}

	tests := []struct {
		name              string
		lastText          string
		priors            []string
		wantReferential   bool
		minTier           string // tier must be at least this rank (or empty for "any")
		maxTier           string // tier must be at most this rank (or empty for "any")
		mustNotEqualTiers []string
	}{
		{
			name:              "do_it_after_tech_thread_lifts",
			lastText:          "do it",
			priors:            techPriors,
			wantReferential:   true,
			mustNotEqualTiers: []string{"SIMPLE"},
		},
		{
			name:              "try_again_after_tech_thread_lifts",
			lastText:          "try again",
			priors:            techPriors,
			wantReferential:   true,
			mustNotEqualTiers: []string{"SIMPLE"},
		},
		{
			name:            "translate_after_tech_thread_stays_simple",
			lastText:        "translate this to spanish",
			priors:          techPriors,
			wantReferential: false,
			maxTier:         "MEDIUM",
		},
		{
			name:            "summarize_after_tech_thread_stays_simple",
			lastText:        "summarize it in one sentence",
			priors:          techPriors,
			wantReferential: false,
			maxTier:         "MEDIUM",
		},
		{
			name:            "do_it_with_empty_priors_stays_simple",
			lastText:        "do it",
			priors:          nil,
			wantReferential: false,
			maxTier:         "SIMPLE",
		},
		{
			name:            "strong_arch_ask_with_smalltalk_priors_stays_strong",
			lastText:        "Design a fault-tolerant distributed consensus algorithm with leader election, log replication, and snapshotting; weigh the tradeoffs between Raft and Paxos and recommend a design under the constraint of WAN replication.",
			priors:          []string{"hi", "thanks", "ok"},
			wantReferential: false,
			minTier:         "COMPLEX",
		},
		{
			name:            "translate_no_priors_stays_simple",
			lastText:        "translate this to spanish",
			priors:          nil,
			wantReferential: false,
			maxTier:         "SIMPLE",
		},
	}

	tierRank := map[string]int{"SIMPLE": 0, "MEDIUM": 1, "COMPLEX": 2, "REASONING": 3}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := a.Analyze(ComplexityInput{
				LastUserText:   tt.lastText,
				PriorUserTexts: tt.priors,
			})

			if result.ReferentialFollowup != tt.wantReferential {
				t.Errorf("ReferentialFollowup = %v, want %v (tier=%s score=%.3f)",
					result.ReferentialFollowup, tt.wantReferential, result.Tier, result.Score)
			}
			if tt.minTier != "" && tierRank[result.Tier] < tierRank[tt.minTier] {
				t.Errorf("tier=%s, expected at least %s (score=%.3f)", result.Tier, tt.minTier, result.Score)
			}
			if tt.maxTier != "" && tierRank[result.Tier] > tierRank[tt.maxTier] {
				t.Errorf("tier=%s, expected at most %s (score=%.3f)", result.Tier, tt.maxTier, result.Score)
			}
			for _, banned := range tt.mustNotEqualTiers {
				if result.Tier == banned {
					t.Errorf("tier=%s, must not equal %s (score=%.3f)", result.Tier, banned, result.Score)
				}
			}
		})
	}
}

func TestScoreConversationContext_RecencyDecay(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	// Empty list returns 0 without dividing by zero.
	if got := a.scoreConversationContext(nil); got != 0.0 {
		t.Errorf("empty priors should return 0.0, got %.3f", got)
	}

	// Single prior message: lastIdx == 0, weight branch is the uniform fallback.
	// Should not panic, should return a positive score for technical content.
	single := a.scoreConversationContext([]string{
		"Design a distributed authentication system with kubernetes, rbac, and oidc.",
	})
	if single <= 0 {
		t.Errorf("expected positive score for single technical prior, got %.3f", single)
	}

	// Linear decay: a strong technical message at the END of the list should
	// produce a meaningfully higher score than the same message at the START.
	recent := a.scoreConversationContext([]string{
		"hello",
		"thanks",
		"Design a distributed authentication system with kubernetes, rbac, and oidc.",
	})
	older := a.scoreConversationContext([]string{
		"Design a distributed authentication system with kubernetes, rbac, and oidc.",
		"hello",
		"thanks",
	})
	if recent <= older {
		t.Errorf("expected recent strong message to score higher than older one: recent=%.3f older=%.3f",
			recent, older)
	}
}

func TestContainsWord(t *testing.T) {
	tests := []struct {
		text     string
		word     string
		expected bool
	}{
		{"write a function", "function", true},
		{"classification problem", "class", false}, // word boundary
		{"the class is good", "class", true},
		{"debug the code", "debug", true},
		{"debug", "debug", true},
		{"nodebug", "debug", false},
		{"la securite est importante", "securite", true},
		{"la sécurité est importante", "sécurité", true},
		{"sécuritétest", "sécurité", false},
		{"", "test", false},
		{"write a function", "", false},
	}

	for _, tt := range tests {
		got := containsWord(tt.text, tt.word)
		if got != tt.expected {
			t.Errorf("containsWord(%q, %q) = %v, want %v", tt.text, tt.word, got, tt.expected)
		}
	}
}

func TestCountWordsNoAllocMatchesStringsFields(t *testing.T) {
	tests := []string{
		"",
		"hello world",
		"  multiple   spaces here  ",
		"line one\nline two\tline three",
		"unicode\u00a0space separated words",
	}

	for _, text := range tests {
		got := countWordsNoAlloc(text)
		want := len(strings.Fields(text))
		if got != want {
			t.Errorf("countWordsNoAlloc(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestKeywordMatchModeFor(t *testing.T) {
	tests := []struct {
		keyword string
		want    keywordMatchMode
	}{
		{"function", matchModeWholeWord},
		{"sécurité", matchModeWholeWord},
		{"ci/cd", matchModeBoundarySubstring},
		{"root cause", matchModePlainSubstring},
	}

	for _, tt := range tests {
		if got := keywordMatchModeFor(tt.keyword); got != tt.want {
			t.Errorf("keywordMatchModeFor(%q) = %v, want %v", tt.keyword, got, tt.want)
		}
	}
}

func TestBuildWordPresenceSet_UnicodeWords(t *testing.T) {
	words := buildWordPresenceSet("la sécurité du réseau protège les données")

	if _, ok := words["sécurité"]; !ok {
		t.Fatalf("expected unicode word to be preserved in presence set")
	}
	if _, ok := words["réseau"]; !ok {
		t.Fatalf("expected second unicode word to be preserved in presence set")
	}
}

func TestAnalyze_CustomUnicodeKeywordMatches(t *testing.T) {
	cfg := DefaultAnalyzerConfig()
	cfg.Keywords.TechnicalKeywords = []string{"sécurité"}

	a := NewComplexityAnalyzerWithConfig(&cfg)
	result := a.Analyze(ComplexityInput{
		LastUserText: "Explique la sécurité du système distribué",
	})

	if result.TechnicalMatchCount == 0 {
		t.Fatalf("expected custom unicode technical keyword to match")
	}
	if result.TechnicalTerms <= 0 {
		t.Fatalf("expected positive technical score for unicode keyword match, got %.3f", result.TechnicalTerms)
	}
}

func TestAnalyze_PunctuatedKeywordStillMatches(t *testing.T) {
	a := NewComplexityAnalyzer(nil)

	result := a.Analyze(ComplexityInput{
		LastUserText: "Please review our CI/CD pipeline and retry middleware behavior.",
	})

	if result.CodeMatchCount == 0 {
		t.Fatalf("expected punctuated keyword path to match code signals")
	}
	if result.CodePresence <= 0 {
		t.Fatalf("expected positive code presence for punctuated keyword case, got %.3f", result.CodePresence)
	}
}

func TestCompiledKeywordMatcher_IgnoresBlankKeywords(t *testing.T) {
	matcher := newCompiledKeywordMatcher(KeywordConfig{
		CodeKeywords: []string{"", "   "},
	})

	signals := matcher.analyzeText("totally unrelated text", lastTextBaseScanMask)

	require.Equal(t, 0, signals.codeCount)
	require.Empty(t, matcher.wholeWordKeywords)
	require.Empty(t, matcher.boundarySubstringKeywords)
	require.Empty(t, matcher.plainSubstringKeywords)
}
