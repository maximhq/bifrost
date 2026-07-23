package complexity

// --- Dimension weights ---

// The positive weights (code + reasoning + technical + token count) sum to
// 1.00 so a prompt maxing every signal reaches the full score range. The
// simple weight is a penalty and deliberately not part of that budget.
const (
	codeWeight                         = 0.34
	reasoningWeight                    = 0.28
	technicalWeight                    = 0.28
	simpleWeight                       = 0.05
	tokenCountWeight                   = 0.10
	systemPromptAssistFactor           = 0.25
	defaultLastMessageBlendWeight      = 0.60
	defaultConversationBlendWeight     = 0.40
	referentialLastMessageBlendWeight  = 0.35
	referentialConversationBlendWeight = 0.65
	referentialMinContextScore         = 0.20
	referentialShortMessageMaxWords    = 12
	wordPresenceSetMinBytes            = 8 * 1024
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

var strongReasoningKeywords = []string{
	"step by step", "think through", "tradeoffs", "pros and cons",
	"justify", "critique", "implications", "explain why",
	"root cause analysis", "reconstruct the sequence",
	"reconstruct the most likely sequence", "what should have happened instead",
	"explain your reasoning", "weigh the tradeoffs", "recommend a design",
}

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

var continuationPhrases = []string{
	"do it", "try again", "continue", "go ahead", "proceed",
	"that one", "this one", "same thing", "again",
	"yes do that", "go with that", "use option 1", "use option 2", "use option 3",
	"now write it",
}
