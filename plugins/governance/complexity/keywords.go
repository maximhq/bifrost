package complexity

// --- Dimension weights ---

const (
	codeWeight                         = 0.30
	reasoningWeight                    = 0.25
	technicalWeight                    = 0.25
	simpleWeight                       = 0.05 // dampener, subtracted
	tokenCountWeight                   = 0.10
	systemPromptAssistFactor           = 0.25
	defaultLastMessageBlendWeight      = 0.60
	defaultConversationBlendWeight     = 0.40
	referentialLastMessageBlendWeight  = 0.35
	referentialConversationBlendWeight = 0.65
	referentialMaxStandaloneScore      = 0.15
	referentialMaxWordCount            = 6
	referentialMinContextScore         = 0.20
	wordPresenceSetMinBytes            = 8 * 1024
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

var referentialPhrases = []string{
	"do it", "try again", "continue", "go ahead", "proceed",
	"that one", "this one", "same thing", "again", "retry",
	"yes do that", "go with that", "use option 1", "use option 2", "use option 3",
	"now write it",
}

var referentialReferenceWords = []string{
	"it", "this", "that", "same", "previous", "earlier",
}

var referentialActionWords = []string{
	"do", "retry", "continue", "proceed", "use", "fix",
	"rewrite", "shorten", "clean", "adjust", "make", "give", "answer",
}

var taskShiftPhrases = []string{
	"translate", "summarize", "in one sentence", "one sentence",
	"in spanish", "in french", "in german", "more politely", "more polite",
}
