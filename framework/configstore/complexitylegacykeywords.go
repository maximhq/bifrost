package configstore

// Frozen snapshot of the keyword lists the pre-semantic complexity analyzer
// shipped, copied from plugins/governance/complexity/keywords.go as of the last
// release that used them.
//
// These are written into the persisted analyzer row alongside the canonical
// three-tier lists so that a Bifrost old enough to predate the semantic router
// can still read that row, pass its own validation (which requires all four
// legacy lists to be non-empty), and score with sane keywords rather than with
// semantic exemplars, which are whole sentences and match almost nothing.
//
// All four lists are written, and none of them is shared with the canonical
// exemplar lists: the two shapes live in separate governance_config rows.
//
// This is a snapshot, not a mirror. It should track what older binaries expect,
// which is what the last pre-semantic release shipped -- not whatever the
// governance plugin's lists look like today. Do not "fix" drift from that file.

// legacyCodeKeywords mirrors codeKeywords (64 entries).
var legacyCodeKeywords = []string{
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

// legacyReasoningKeywords mirrors strongReasoningKeywords (15 entries).
var legacyReasoningKeywords = []string{
	"step by step", "think through", "tradeoffs", "pros and cons",
	"justify", "critique", "implications", "explain why",
	"root cause analysis", "reconstruct the sequence",
	"reconstruct the most likely sequence", "what should have happened instead",
	"explain your reasoning", "weigh the tradeoffs", "recommend a design",
}

// legacyTechnicalKeywords mirrors technicalKeywords (93 entries).
var legacyTechnicalKeywords = []string{
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

// legacySimpleKeywords mirrors simpleKeywords (21 entries).
var legacySimpleKeywords = []string{
	"what is", "define", "hello", "hi", "thanks", "how do i spell",
	"translate", "what does", "who is", "when was", "tell me about",
	"good morning", "good night", "how are you", "simple", "brief",
	"short", "quick", "beginner", "basic", "concise",
}
