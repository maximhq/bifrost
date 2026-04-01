package complexity

import (
	"testing"
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

func TestAnalyze_CustomTierBoundaries(t *testing.T) {
	a := NewComplexityAnalyzer(&TierBoundaries{
		SimpleMedium:     0.10,
		MediumComplex:    0.30,
		ComplexReasoning: 0.50,
	})

	result := a.Analyze(ComplexityInput{
		LastUserText: "Write a function to sort an array",
	})

	// With lower boundaries, this should classify higher
	if result.Score > 0 && result.Tier == "SIMPLE" && result.Score >= 0.10 {
		t.Errorf("expected non-SIMPLE tier with lower boundaries at score=%.3f", result.Score)
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
		{"", "test", false},
	}

	for _, tt := range tests {
		got := containsWord(tt.text, tt.word)
		if got != tt.expected {
			t.Errorf("containsWord(%q, %q) = %v, want %v", tt.text, tt.word, got, tt.expected)
		}
	}
}
