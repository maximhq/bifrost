# TECH-003 — Content Guardrails

**Feature ID:** GUARD  
**SRS Reference:** §3.14 (GUARD-01 → GUARD-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement a multi-layer content moderation pipeline that can block, flag, or transform LLM requests/responses based on keyword lists, regex patterns, and AI-based safety classifiers. Guardrails run as an `LLMPlugin` in the pre/post hook pipeline.

**Policy types (SRS GUARD-01):**
- `keyword_block` — exact/wildcard keyword matching
- `regex_filter` — PCRE regex patterns
- `topic_block` — coarse-grained topic categories (violence, adult, etc.)
- `ai_classifier` — call external safety API (OpenAI Moderation, custom model)
- `custom_prompt` — prompt injection: prepend system prompt with safety instructions

---

## 2. Architecture Mapping

```
plugins/guardrails/           (NEW independent Go module)
├── go.mod                    module github.com/maximhq/bifrost/plugins/guardrails
├── plugin.go                 GuardrailsPlugin struct, Init(), GetName(), Cleanup()
├── policy.go                 Policy types, PolicyEngine
├── checks/
│   ├── keyword.go            KeywordCheck
│   ├── regex.go              RegexCheck
│   ├── topic.go              TopicCheck (keyword-based topic taxonomy)
│   └── ai_classifier.go      AIClassifierCheck (HTTP call to moderation API)
├── action.go                 GuardrailAction (block, flag, transform, allow)
├── config.go                 GuardrailsConfig
└── tables.go                 PolicyTable, ViolationLogTable GORM models

framework/configstore/
└── guardrails.go             (NEW) ConfigStore GORM operations for policies

transports/bifrost-http/
└── handlers/guardrails.go    (NEW) CRUD API for policies
```

---

## 3. Configuration Schema

```go
// plugins/guardrails/config.go

type GuardrailsConfig struct {
    Enabled         bool
    DefaultAction   GuardrailAction  // action when no policy matches: "allow"
    Policies        []Policy
    ViolationLog    ViolationLogConfig
    AIClassifier    *AIClassifierConfig
}

type Policy struct {
    ID          string
    Name        string
    Enabled     bool
    Priority    int                 // lower = evaluated first
    Scope       []PolicyScope       // "request", "response", or both
    Conditions  []PolicyCondition   // AND logic
    Action      GuardrailAction
    ActionConfig ActionConfig       // per-action parameters
}

type PolicyScope string
const (
    ScopeRequest  PolicyScope = "request"
    ScopeResponse PolicyScope = "response"
)

type PolicyCondition struct {
    Type    CheckType  // "keyword", "regex", "topic", "ai_classifier"
    Config  map[string]any
}

type CheckType string
const (
    CheckKeyword      CheckType = "keyword"
    CheckRegex        CheckType = "regex"
    CheckTopic        CheckType = "topic"
    CheckAIClassifier CheckType = "ai_classifier"
)

type GuardrailAction string
const (
    ActionBlock     GuardrailAction = "block"      // return 451 / error response
    ActionFlag      GuardrailAction = "flag"        // pass through, log violation
    ActionTransform GuardrailAction = "transform"   // redact matched content
    ActionAllow     GuardrailAction = "allow"
)

type ActionConfig struct {
    BlockMessage    string          // message returned to caller on block
    RedactWith      string          // substitution string for transform action
    FlagSeverity    string          // "low", "medium", "high", "critical"
}

type AIClassifierConfig struct {
    Provider    string             // "openai" | "custom"
    Endpoint    string             // custom endpoint URL
    APIKey      string             // or env.VAR_NAME
    Model       string             // e.g., "text-moderation-latest"
    Threshold   float64            // score threshold (0.0–1.0)
    Categories  []string           // categories to check
}
```

---

## 4. Plugin Implementation

```go
// plugins/guardrails/plugin.go

type GuardrailsPlugin struct {
    config   GuardrailsConfig
    engine   *PolicyEngine
    logStore ViolationLogger
    mu       sync.RWMutex
}

func (p *GuardrailsPlugin) GetName() string { return "guardrails" }

func (p *GuardrailsPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (
    *schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
    
    if !p.config.Enabled {
        return req, nil, nil
    }
    
    text := extractRequestText(req)  // concatenate all message content
    result, err := p.engine.Evaluate(ctx, text, ScopeRequest)
    if err != nil {
        // Log warning — never block on evaluation error
        return req, nil, nil
    }
    
    return p.applyAction(ctx, req, nil, result)
}

func (p *GuardrailsPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (
    *schemas.BifrostResponse, *schemas.BifrostError, error) {
    
    if !p.config.Enabled || resp == nil {
        return resp, bifrostErr, nil
    }
    
    text := extractResponseText(resp)
    result, err := p.engine.Evaluate(ctx, text, ScopeResponse)
    if err != nil {
        return resp, bifrostErr, nil
    }
    
    newReq := reconstructRequest(ctx)  // needed for action logging
    _, sc, applyErr := p.applyAction(ctx, newReq, resp, result)
    if sc != nil {
        // Block: return error instead of response
        allowFallbacks := false
        return nil, &schemas.BifrostError{
            Type:           "guardrail_violation",
            Message:        p.config.Policies[result.PolicyIdx].ActionConfig.BlockMessage,
            AllowFallbacks: &allowFallbacks,
        }, applyErr
    }
    return resp, bifrostErr, applyErr
}
```

---

## 5. Policy Engine

```go
// plugins/guardrails/policy.go

type PolicyEngine struct {
    policies       []Policy
    checks         map[CheckType]Checker
    httpClient     *fasthttp.Client
}

type EvaluationResult struct {
    Matched     bool
    PolicyIdx   int
    MatchedText []string    // captured substrings
    Score       float64     // for AI classifier
}

type Checker interface {
    Check(ctx context.Context, text string, config map[string]any) (bool, []string, error)
}

func (e *PolicyEngine) Evaluate(ctx *schemas.BifrostContext, text string, scope PolicyScope) (*EvaluationResult, error) {
    // Sort policies by priority
    for i, policy := range e.policies {
        if !policy.Enabled || !containsScope(policy.Scope, scope) {
            continue
        }
        matched := true
        var matchedText []string
        for _, cond := range policy.Conditions {
            checker := e.checks[cond.Type]
            ok, snippets, err := checker.Check(ctx, text, cond.Config)
            if err != nil || !ok {
                matched = false
                break
            }
            matchedText = append(matchedText, snippets...)
        }
        if matched {
            return &EvaluationResult{Matched: true, PolicyIdx: i, MatchedText: matchedText}, nil
        }
    }
    return &EvaluationResult{Matched: false}, nil
}
```

---

## 6. Individual Checkers

### 6.1 Keyword Check

```go
// plugins/guardrails/checks/keyword.go

type KeywordCheck struct{}

func (c *KeywordCheck) Check(_ context.Context, text string, config map[string]any) (bool, []string, error) {
    keywords := toStringSlice(config["keywords"])
    caseSensitive := toBool(config["case_sensitive"])
    
    if !caseSensitive {
        text = strings.ToLower(text)
    }
    
    var matched []string
    for _, kw := range keywords {
        needle := kw
        if !caseSensitive {
            needle = strings.ToLower(kw)
        }
        if strings.Contains(text, needle) {
            matched = append(matched, kw)
        }
    }
    return len(matched) > 0, matched, nil
}
```

### 6.2 Regex Check

```go
// plugins/guardrails/checks/regex.go

type RegexCheck struct {
    cache sync.Map  // pattern → *regexp.Regexp
}

func (c *RegexCheck) Check(_ context.Context, text string, config map[string]any) (bool, []string, error) {
    patterns := toStringSlice(config["patterns"])
    var matched []string
    for _, pat := range patterns {
        re, err := c.compileOrLoad(pat)
        if err != nil {
            return false, nil, fmt.Errorf("invalid regex %q: %w", pat, err)
        }
        if m := re.FindAllString(text, -1); len(m) > 0 {
            matched = append(matched, m...)
        }
    }
    return len(matched) > 0, matched, nil
}
```

### 6.3 AI Classifier Check

```go
// plugins/guardrails/checks/ai_classifier.go
// Calls OpenAI moderation API or custom endpoint

type AIClassifierCheck struct {
    config AIClassifierConfig
    client *fasthttp.Client
}

func (c *AIClassifierCheck) Check(ctx context.Context, text string, config map[string]any) (bool, []string, error) {
    // POST to moderation endpoint
    reqBody := map[string]any{"input": text}
    respBody, err := c.callAPI(ctx, reqBody)
    // Parse response: check any category score >= threshold
    threshold := c.config.Threshold
    var flagged []string
    for cat, score := range respBody.Results[0].CategoryScores {
        if score >= threshold {
            flagged = append(flagged, cat)
        }
    }
    return len(flagged) > 0, flagged, nil
}
```

---

## 7. Violation Logging

```go
// framework/configstore/tables/guardrails.go

type GuardrailViolationTable struct {
    ID           string    `gorm:"primaryKey"`
    Timestamp    time.Time `gorm:"index"`
    RequestID    string    `gorm:"index"`
    PolicyID     string    `gorm:"index"`
    PolicyName   string
    Scope        string    // "request" | "response"
    Action       string
    MatchedText  string    `gorm:"type:text"`  // JSON array of matched snippets
    VirtualKeyID string    `gorm:"index"`
    Provider     string
    Model        string
    Severity     string    `gorm:"index"`      // "low","medium","high","critical"
    Resolved     bool      `gorm:"default:false"`
}
```

---

## 8. Management API

```go
// transports/bifrost-http/handlers/guardrails.go

// GET    /api/guardrails/policies
// POST   /api/guardrails/policies
// GET    /api/guardrails/policies/{id}
// PUT    /api/guardrails/policies/{id}
// DELETE /api/guardrails/policies/{id}
// POST   /api/guardrails/policies/{id}/enable
// POST   /api/guardrails/policies/{id}/disable
// GET    /api/guardrails/violations?policy_id=&severity=&start_time=&end_time=
// POST   /api/guardrails/test  — dry-run evaluation on sample text
```

---

## 9. Plugin Registration Order

Guardrails must run **before** the logging plugin so violations are captured:

```
1. litellmcompat
2. mocker
3. governance
4. guardrails   ← NEW: after governance (VK validated), before cache
5. piiredactor  ← NEW: after guardrails
6. semanticcache
7. logging
8. telemetry
9. otel
```

---

## 10. UI Components

```
ui/app/enterprise/guardrails/
├── page.tsx                — Policy list with enable/disable toggles
├── [id]/page.tsx           — Policy editor (conditions, actions)
├── violations/page.tsx     — Violation log browser
└── components/
    ├── PolicyBuilder.tsx   — Drag-drop condition builder
    ├── TestPanel.tsx       — Live dry-run testing panel
    └── ViolationChart.tsx  — Severity distribution over time
```

---

## 11. Performance Considerations

- Keyword and regex checks: O(n×m) but negligible vs. LLM latency. Pre-compile all regex on plugin init.
- AI classifier calls: async where `scope=response` (does not block streaming). For `scope=request`, it IS blocking — document latency impact.
- Policy engine short-circuits on first match per priority order.
- Hot-reload: policies are stored in `atomic.Pointer[[]Policy]` — updates swap the pointer without lock.
