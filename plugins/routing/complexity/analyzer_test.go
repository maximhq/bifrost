package complexity

import (
	"strings"
	"testing"
)

func TestAnalyze_Simple(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "What is 2+2?",
	})

	if result.Tier != "SIMPLE" {
		t.Errorf("expected SIMPLE tier for 'What is 2+2?', got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_CustomTierBoundaries(t *testing.T) {
	defaultAnalyzer := NewComplexityAnalyzer()
	cfg := DefaultAnalyzerConfig()
	cfg.TierBoundaries = TierBoundaries{
		SimpleMedium:     0.05,
		MediumComplex:    0.10,
		ComplexReasoning: 0.20,
	}
	customAnalyzer := NewComplexityAnalyzerWithConfig(&cfg)

	if got := defaultAnalyzer.classifyTier(0.18); got != TierMedium {
		t.Fatalf("default boundary classified 0.18 as %s, want %s", got, TierMedium)
	}
	if got := customAnalyzer.classifyTier(0.18); got != TierComplex {
		t.Fatalf("custom boundary classified 0.18 as %s, want %s", got, TierComplex)
	}
}

func TestAnalyze_CustomReasoningKeywordsAffectOverride(t *testing.T) {
	cfg := DefaultAnalyzerConfig()
	cfg.Keywords.ReasoningKeywords = []string{"deepmagic"}
	a := NewComplexityAnalyzerWithConfig(&cfg)

	result := a.Analyze(ComplexityInput{
		LastUserText: "deepmagic api function",
	})

	if result.Tier != TierReasoning {
		t.Fatalf("expected custom reasoning keyword to promote tier to %s, got %s (score=%.3f)", TierReasoning, result.Tier, result.Score)
	}
}

func TestAnalyze_Hello(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Hello, how are you?",
	})

	if result.Tier != "SIMPLE" {
		t.Errorf("expected SIMPLE tier for greeting, got %s (score=%.3f)", result.Tier, result.Score)
	}
	if result.Score != 0.0 {
		t.Errorf("expected simple-only greeting to clamp to 0.0, got %.3f", result.Score)
	}
}

func TestAnalyze_NoSignalFallsBackButSimpleSignalClassifies(t *testing.T) {
	a := NewComplexityAnalyzer()

	noSignal := a.Analyze(ComplexityInput{
		LastUserText: "2+3",
	})
	if noSignal != nil {
		t.Fatalf("expected no-signal arithmetic prompt to be unclassified, got %s (score=%.3f)", noSignal.Tier, noSignal.Score)
	}

	simpleSignal := a.Analyze(ComplexityInput{
		LastUserText: "translate this to spanish",
	})
	if simpleSignal == nil {
		t.Fatalf("expected simple keyword prompt to classify")
	}
	if simpleSignal.Tier != TierSimple || simpleSignal.Score != 0.0 {
		t.Fatalf("expected simple keyword prompt to classify as SIMPLE with 0.0 score, got %s (score=%.3f)",
			simpleSignal.Tier, simpleSignal.Score)
	}
}

func TestAnalyze_CodeRequest(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Write a Python quicksort function that handles arrays with duplicate elements",
	})

	if result.Tier != "MEDIUM" && result.Tier != "COMPLEX" {
		t.Errorf("expected MEDIUM or COMPLEX tier for code request, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_Complex(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Design a distributed authentication architecture using Kubernetes, encryption, load balancer, failover, RBAC, OIDC, audit log, and connection pool idempotency.",
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected MEDIUM or higher tier for architecture request, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_Reasoning(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Think step by step through the tradeoffs of this ML architecture and explain why one approach is better",
	})

	if result.Tier != "REASONING" {
		t.Errorf("expected REASONING tier for deep reasoning request, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_OutputComplexityRequiresVisibleSignal(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "List every AWS service and explain each one with examples",
	})

	if result != nil {
		t.Errorf("expected output-heavy request without visible signals to be unclassified, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_ConversationContextDoesNotClassifyNoSignalLatestTurn(t *testing.T) {
	a := NewComplexityAnalyzer()

	noCtx := a.Analyze(ComplexityInput{
		LastUserText: "Why?",
	})
	if noCtx != nil {
		t.Fatalf("expected no-signal latest turn without context to be unclassified, got %s (score=%.3f)", noCtx.Tier, noCtx.Score)
	}

	withCtx := a.Analyze(ComplexityInput{
		LastUserText: "Why?",
		PriorUserTexts: []string{
			"How does the distributed authentication system handle encryption?",
			"What about the kubernetes infrastructure for microservices?",
			"Can you explain the concurrency model and mutex usage?",
		},
	})

	if withCtx != nil {
		t.Errorf("expected complex history not to classify a no-signal latest turn, got %s (score=%.3f)", withCtx.Tier, withCtx.Score)
	}
}

func TestAnalyze_ConversationContextDoesNotDiluteStrongLastMessage(t *testing.T) {
	a := NewComplexityAnalyzer()

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
}

func TestAnalyze_ContinuationPhraseLiftsTechnicalContinuation(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "do it",
		PriorUserTexts: []string{
			"We need to refactor the retry middleware so only 429 and 408 retry.",
			"Move fallback selection after request classification and keep the behavior change explicit in the PR.",
			"Update the Go tests for the CEL routing rules and the governance plugin.",
		},
	})

	if result == nil {
		t.Fatalf("expected continuation phrase with prior context to classify")
	}
	if result.Tier == "SIMPLE" {
		t.Fatalf("expected continuation to lift above SIMPLE, got %s (score=%.3f)", result.Tier, result.Score)
	}
	if result.Score < simpleMediumBoundary {
		t.Fatalf("expected score above SIMPLE threshold, got %.3f", result.Score)
	}
}

func TestAnalyze_ContinuationPhraseRequiresRealContext(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "do it",
	})

	if result != nil {
		t.Fatalf("expected continuation phrase without prior context to be unclassified, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_SimpleKeywordFollowupDoesNotUseContext(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "translate this to spanish",
		PriorUserTexts: []string{
			"We need to debug the Kubernetes deployment and fix the authentication middleware.",
			"The RBAC mapping for SAML tenants is failing after the migration.",
		},
	})

	if result == nil {
		t.Fatalf("expected simple keyword follow-up to classify")
	}
	if result.Score >= mediumComplexBoundary {
		t.Fatalf("expected simple keyword follow-up to stay below COMPLEX threshold, got %.3f", result.Score)
	}
}

func TestAnalyze_UnmatchedFollowupDoesNotUseContext(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "summarize it in one sentence",
		PriorUserTexts: []string{
			"Design a multi-tenant billing ledger with metering, proration, credits, and invoice generation.",
			"Include the data model and monthly aggregation flow.",
		},
	})

	if result != nil {
		t.Fatalf("expected unmatched follow-up not to use context, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_RecentContextOutweighsOlderContext(t *testing.T) {
	a := NewComplexityAnalyzer()

	recentTech := a.Analyze(ComplexityInput{
		LastUserText: "do it",
		PriorUserTexts: []string{
			"Hello there.",
			"Design a distributed authentication system with RBAC, OIDC, and regional failover.",
			"Debug the API gateway encryption middleware and Kubernetes connection pool behavior.",
		},
	})

	olderTech := a.Analyze(ComplexityInput{
		LastUserText: "do it",
		PriorUserTexts: []string{
			"Design a distributed authentication system with RBAC, OIDC, and regional failover.",
			"Debug the API gateway encryption middleware and Kubernetes connection pool behavior.",
			"Thanks.",
		},
	})

	if recentTech == nil || olderTech == nil {
		t.Fatalf("expected both continuation cases to classify, got recent=%v older=%v", recentTech, olderTech)
	}
	if recentTech.Score <= olderTech.Score {
		t.Fatalf("expected more recent technical context to matter more: recent=%.3f older=%.3f",
			recentTech.Score, olderTech.Score)
	}
}

func TestAnalyze_SystemPromptBoost(t *testing.T) {
	a := NewComplexityAnalyzer()

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

func TestAnalyze_SystemPromptSimpleSignalsIgnored(t *testing.T) {
	a := NewComplexityAnalyzer()

	base := a.Analyze(ComplexityInput{
		LastUserText: "Explain how database code works",
	})

	withSimpleSystemPrompt := a.Analyze(ComplexityInput{
		LastUserText: "Explain how database code works",
		SystemText:   "You are a beginner tutor. Keep answers simple, brief, and concise.",
	})

	if withSimpleSystemPrompt.Score != base.Score {
		t.Errorf("expected simple system-prompt terms to be ignored: base=%.3f, withSimpleSystemPrompt=%.3f",
			base.Score, withSimpleSystemPrompt.Score)
	}
}

func TestAnalyze_SystemPromptLexicalAssistDoesNotOverPromoteSimpleCodeDefinition(t *testing.T) {
	a := NewComplexityAnalyzer()

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
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{})

	if result != nil {
		t.Errorf("expected empty input to be unclassified, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_ReasoningOverrideNotTooEager(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Why does React re-render, and what if I use useMemo?",
	})

	if result != nil {
		t.Errorf("expected removed broad reasoning markers to be unclassified, got %s (score=%.3f)", result.Tier, result.Score)
	}
}

func TestAnalyze_SimpleKeywordDoesNotSuppressTechnicalSignals(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "What is eventual consistency in distributed systems with sharding?",
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected non-SIMPLE tier for technical 'what is' question, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_AccessVsRefreshTokens(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Explain the difference between an access token and a refresh token. When would you use short-lived vs long-lived tokens?",
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected MEDIUM or higher tier for token lifecycle question, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_OutageCustomerCommunication(t *testing.T) {
	a := NewComplexityAnalyzer()

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
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Design a multi-tenant authentication service for a SaaS platform on Kubernetes. Requirements: RBAC with custom roles per tenant, audit logging for all auth events, regional failover across two AWS regions, and support for both SAML 2.0 and OIDC enterprise SSO. Include the data model and the request flow for a login.",
	})

	if result.Tier == "SIMPLE" {
		t.Errorf("expected MEDIUM or higher tier for multi-tenant SSO architecture prompt, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_PostIncidentReconstruction(t *testing.T) {
	a := NewComplexityAnalyzer()

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
	a := NewComplexityAnalyzer()

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
	a := NewComplexityAnalyzer()

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
	a := NewComplexityAnalyzer()

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
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "Compare self-hosted Qdrant vs managed Pinecone for a hybrid search system serving 1,000 QPS with 50M vectors. We're in a regulated industry - no data can leave our VPC, and we need SOC 2 attestation for all data stores. Weigh the tradeoffs around data residency compliance, operational burden for a 4-person infra team, query latency at scale, cost scaling characteristics, and disaster recovery options. Recommend one and explain your reasoning.",
	})

	if result.Tier != "REASONING" {
		t.Errorf("expected REASONING tier for vector database tradeoff recommendation, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestIsContinuationFollowup_GuardBranches(t *testing.T) {
	tests := []struct {
		name      string
		lastText  string
		convScore float64
		expected  bool
	}{
		{"phrase_match_ok", "do it", 0.30, true},
		{"phrase_match_longer_text", "do it now please right away ok", 0.30, true},
		{"no_phrase", "", 0.30, false},
		{"phrase_match_conv_just_below_threshold", "do it", 0.199, false},
		{"phrase_match_conv_at_threshold", "do it", 0.20, true},
		{"explicit_use_option", "use option 2", 0.30, true},
		{"retry_is_code_not_continuation", "retry", 0.30, false},
		{"former_inferred_fix_it", "fix it", 0.30, false},
		{"former_inferred_make_it_shorter", "make it shorter", 0.30, false},
		{"former_inferred_answer_previous", "answer the previous question", 0.30, false},
		{"unrelated_short_text", "hello there friend", 0.30, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matcher := newCompiledKeywordMatcher(defaultFullKeywordConfig())
			signals := matcher.analyzeText(tt.lastText, lastTextBaseScanMask)
			got := isContinuationFollowup(signals, tt.convScore)
			if got != tt.expected {
				t.Errorf("isContinuationFollowup(%q, conv=%.3f) = %v, want %v",
					tt.lastText, tt.convScore, got, tt.expected)
			}
		})
	}
}

func TestAnalyze_ExplicitContinuationPhrasesUseContext(t *testing.T) {
	a := NewComplexityAnalyzer()

	techPriors := []string{
		"We need to refactor the retry middleware so only 429 and 408 retry.",
		"Move fallback selection after request classification and keep the behavior change explicit in the PR.",
		"Update the Go tests for the CEL routing rules and the governance plugin.",
	}

	tests := []struct {
		name     string
		lastText string
	}{
		{"do_it", "do it"},
		{"try_again", "try again"},
		{"go_ahead", "go ahead"},
		{"same_thing", "same thing"},
		{"use_option", "use option 2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := a.Analyze(ComplexityInput{
				LastUserText:   tt.lastText,
				PriorUserTexts: techPriors,
			})
			if result.Tier == "SIMPLE" {
				t.Fatalf("expected lift above SIMPLE for %q, got %s (score=%.3f)",
					tt.lastText, result.Tier, result.Score)
			}
		})
	}
}

func TestAnalyze_ContinuationPhraseDoesNotHijackStrongAsk(t *testing.T) {
	a := NewComplexityAnalyzer()

	result := a.Analyze(ComplexityInput{
		LastUserText: "use option 2 to design the distributed consensus algorithm with kubernetes and rbac",
		PriorUserTexts: []string{
			"We need to refactor the retry middleware so only 429 and 408 retry.",
		},
	})

	if result.Tier == "SIMPLE" {
		t.Fatalf("expected high-signal message to stay above SIMPLE despite continuation phrase, got %s (score=%.3f)",
			result.Tier, result.Score)
	}
}

func TestAnalyze_RegressionAnchors(t *testing.T) {
	a := NewComplexityAnalyzer()

	techPriors := []string{
		"We need to refactor the retry middleware so only 429 and 408 retry.",
		"Move fallback selection after request classification and keep the behavior change explicit in the PR.",
		"Update the Go tests for the CEL routing rules and the governance plugin.",
	}

	tests := []struct {
		name              string
		lastText          string
		priors            []string
		expectNil         bool
		minTier           string // tier must be at least this rank (or empty for "any")
		maxTier           string // tier must be at most this rank (or empty for "any")
		mustNotEqualTiers []string
	}{
		{
			name:              "do_it_after_tech_thread_lifts",
			lastText:          "do it",
			priors:            techPriors,
			mustNotEqualTiers: []string{"SIMPLE"},
		},
		{
			name:              "try_again_after_tech_thread_lifts",
			lastText:          "try again",
			priors:            techPriors,
			mustNotEqualTiers: []string{"SIMPLE"},
		},
		{
			name:     "translate_after_tech_thread_stays_simple",
			lastText: "translate this to spanish",
			priors:   techPriors,
			maxTier:  "MEDIUM",
		},
		{
			name:      "summarize_after_tech_thread_is_unclassified",
			lastText:  "summarize it in one sentence",
			priors:    techPriors,
			expectNil: true,
		},
		{
			name:      "do_it_with_empty_priors_is_unclassified",
			lastText:  "do it",
			priors:    nil,
			expectNil: true,
		},
		{
			name:     "strong_arch_ask_with_smalltalk_priors_stays_strong",
			lastText: "Design a fault-tolerant distributed consensus algorithm with leader election, log replication, and snapshotting; weigh the tradeoffs between Raft and Paxos and recommend a design under the constraint of WAN replication.",
			priors:   []string{"hi", "thanks", "ok"},
			minTier:  "COMPLEX",
		},
		{
			name:     "translate_no_priors_stays_simple",
			lastText: "translate this to spanish",
			priors:   nil,
			maxTier:  "SIMPLE",
		},
	}

	tierRank := map[string]int{"SIMPLE": 0, "MEDIUM": 1, "COMPLEX": 2, "REASONING": 3}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := a.Analyze(ComplexityInput{
				LastUserText:   tt.lastText,
				PriorUserTexts: tt.priors,
			})

			if tt.expectNil {
				if result != nil {
					t.Fatalf("expected unclassified result, got tier=%s score=%.3f", result.Tier, result.Score)
				}
				return
			}
			if result == nil {
				t.Fatalf("expected classified result")
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
	a := NewComplexityAnalyzer()

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
		// Space-separating scripts keep strict whole-word matching: a keyword
		// embedded in a longer Latin word is not a match.
		{"decode this please", "code", false},
		{"encoded payload", "code", false},
		{"please refactor this code now", "code", true},
		{"the code, and more", "code", true},
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
	text := "la sécurité du réseau protège les données"
	words := buildWordPresenceSet(text, countWordsNoAlloc(text))

	if _, ok := words["sécurité"]; !ok {
		t.Fatalf("expected unicode word to be preserved in presence set")
	}
	if _, ok := words["réseau"]; !ok {
		t.Fatalf("expected second unicode word to be preserved in presence set")
	}

	// Letters of a script that does not separate words with spaces end a token
	// instead of extending one, so the run contributes no token of its own and
	// a keyword in another script embedded in it becomes a token.
	japanese := "このコードを直して"
	if words := buildWordPresenceSet(japanese, countWordsNoAlloc(japanese)); len(words) != 0 {
		t.Fatalf("expected %q to yield no tokens of its own, got %v", japanese, words)
	}

	embedded := "このapiを直して"
	words = buildWordPresenceSet(embedded, countWordsNoAlloc(embedded))
	if _, ok := words["api"]; !ok {
		t.Fatalf("expected %q to yield the token \"api\", got %v", embedded, words)
	}
}

func TestAnalyze_PunctuatedKeywordStillMatches(t *testing.T) {
	a := NewComplexityAnalyzer()

	signals := a.matcher.analyzeText("Please review our CI/CD pipeline and retry middleware behavior.", lastTextBaseScanMask)
	if signals.codeCount == 0 {
		t.Fatalf("expected punctuated keyword path to match code signals")
	}
}

// TestContainsWordInUnsegmentedScripts covers keywords written in scripts that
// do not separate words with spaces. There is no whitespace or punctuation
// around a word in these scripts, so the neighbouring rune is another letter
// and a strict whole-word rule would reject every occurrence.
func TestContainsWordInUnsegmentedScripts(t *testing.T) {
	tests := []struct {
		name string
		text string
		word string
	}{
		{"japanese katakana keyword", "このコードをリファクタリングして", "コード"},
		{"japanese kanji keyword", "日本語の文章を要約してください", "要約"},
		{"japanese hiragana keyword", "ふりがなをつけてください", "ふりがな"},
		{"chinese keyword", "帮我写一段代码来实现快速排序算法", "代码"},
		{"chinese multi character keyword", "分析这个架构设计的死锁问题", "架构设计"},
		{"korean noun followed by a particle", "이 코드를 리팩토링 해줘", "코드"},
		{"thai keyword", "ช่วยเขียนโค้ดให้หน่อย", "โค้ด"},

		// A keyword that forms part of a longer compound must still match.
		// Compounds are written as an unbroken run in these scripts, so
		// refusing to match inside one would miss the majority of real usage.
		{"korean compound", "원자력발전소 설계 문제", "원자"},
		{"japanese compound", "原子力発電所の設計", "原子"},
		{"chinese compound", "原子能发电站的设计", "原子"},

		// Latin technical terms are routinely embedded in unsegmented text
		// without surrounding spaces. The boundary rule looks at the
		// neighbouring rune, not at the script of the keyword, so these match
		// even though the keyword itself is Latin.
		{"latin keyword inside japanese text", "このapiを直して", "api"},
		{"latin keyword inside japanese text with kana suffix", "このcodeをレビューして", "code"},
		{"latin keyword at the start of japanese text", "bugを修正してください", "bug"},
		{"latin keyword inside chinese text", "帮我看看这个api的问题", "api"},
		{"latin keyword inside korean text", "이 api를 고쳐줘", "api"},

		// Japanese letters whose Line_Break class is CJ or NS rather than ID.
		// The prolonged sound mark U+30FC is class CJ, and katakana loanwords
		// are commonly written both with and without it, so a keyword spelled
		// one way has to match text spelled the other.
		{"keyword before a prolonged sound mark", "サーバーの設定を確認して", "サーバ"},
		{"keyword before a prolonged sound mark in a loanword", "ユーザーを追加して", "ユーザ"},
		{"keyword before a prolonged sound mark mid word", "メモリーリークを調べて", "メモリ"},
		// U+3005 is the ideographic iteration mark, class NS. It stands in for
		// the letter before it, so it always sits inside an unsegmented run.
		{"keyword before an iteration mark", "時々エラーが出る", "時"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !containsWord(tt.text, tt.word) {
				t.Errorf("containsWord(%q, %q) = false, want true", tt.text, tt.word)
			}
		})
	}
}

// TestContainsWordOverMatchesInsideUnsegmentedRuns pins the accepted cost of
// treating unsegmented letters as word boundaries: a short keyword also matches
// when it happens to occur inside an unrelated longer run.
//
// These are not defects. Splitting an unsegmented run into words requires a
// dictionary or a statistical segmenter, which keyword matching deliberately
// does not carry. The alternative is the opposite failure, where no keyword in
// these scripts ever matches anything, so over-matching is the better trade for
// a routing signal that only feeds a score. Operators who hit a specific false
// positive can configure a longer, more specific keyword.
func TestContainsWordOverMatchesInsideUnsegmentedRuns(t *testing.T) {
	tests := []struct {
		name string
		text string
		word string
	}{
		// "レコード" is the loanword "record"; it contains "コード" ("code").
		{"japanese keyword inside a longer loanword", "レコードを更新して", "コード"},
		// Segmented as 重要|約束 ("important" + "promise"), so "要約"
		// ("summary") spans the boundary between two words.
		{"japanese keyword spanning two words", "重要約束を守る", "要約"},
		// "设计师" is "designer"; it contains "设计" ("design").
		{"chinese keyword inside a longer word", "帮我做一个设计师用的工具", "设计"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !containsWord(tt.text, tt.word) {
				t.Errorf("containsWord(%q, %q) = false, want true (accepted over-match)", tt.text, tt.word)
			}
		})
	}
}

// TestDefaultKeywordsDoNotMatchOrdinaryEnglishPrompts guards the boundary check
// itself. The default lists contain very short entries such as "pr", "diff",
// "rest" and "hi", which occur as substrings of everyday English words. Each
// prompt below contains at least one such substring and no genuine keyword, so
// dropping the boundary check would light up the code and technical dimensions
// on plainly non-technical traffic.
func TestDefaultKeywordsDoNotMatchOrdinaryEnglishPrompts(t *testing.T) {
	prompts := []string{
		"Please provide a different approach to this problem", // provide, different, this
		"Can you summarize the restaurant menu for me",        // restaurant
		"I need help writing a thank you note",
		"Describe the difference between these two paintings", // difference
		"The history of the printing press is fascinating",    // history, printing
		"My grandmother restored an old wooden chest",         // restored
		"A lighthouse keeper prepared dinner for his guests",  // prepared, his
	}

	keywords := defaultFullKeywordConfig()
	allKeywords := make([]string, 0, len(keywords.CodeKeywords))
	for _, list := range [][]string{
		keywords.CodeKeywords,
		keywords.StrongReasoningKeywords,
		keywords.TechnicalKeywords,
		keywords.SimpleKeywords,
		keywords.ContinuationPhrases,
	} {
		allKeywords = append(allKeywords, list...)
	}

	// A lower bound rather than an exact count, so adding a keyword later does
	// not turn this into a false failure.
	const minDefaultKeywords = 100
	if len(allKeywords) < minDefaultKeywords {
		t.Fatalf("default keyword lists hold %d entries, expected at least %d; this test is no longer exercising the real lists", len(allKeywords), minDefaultKeywords)
	}

	naiveMatches := 0
	for _, prompt := range prompts {
		lowerPrompt := strings.ToLower(prompt)
		for _, keyword := range allKeywords {
			if containsWord(lowerPrompt, keyword) {
				t.Errorf("containsWord(%q, %q) = true, want false; ordinary English prompts must not match default keywords", lowerPrompt, keyword)
			}
			if strings.Contains(lowerPrompt, keyword) {
				naiveMatches++
			}
		}
	}

	// Proves the prompts really are the dangerous kind: without a boundary
	// check they would match. If this ever drops to zero the prompts have
	// stopped testing anything.
	if naiveMatches == 0 {
		t.Fatalf("expected the prompts to contain default keywords as bare substrings, got none; the prompts no longer exercise boundary checking")
	}
	t.Logf("%d substring occurrences of default keywords were correctly rejected by boundary checking", naiveMatches)
}
