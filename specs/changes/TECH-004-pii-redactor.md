# TECH-004 — PII Detection & Redaction

**Feature ID:** PII  
**SRS Reference:** §3.15 (PII-01 → PII-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement a PII detection and redaction pipeline that identifies and masks sensitive personal data in both LLM request inputs and responses. The plugin runs as an `LLMPlugin` and must execute **before the logging plugin** to ensure PII never reaches the log store.

**Supported PII entity types (SRS PII-01):**
- `PERSON_NAME`, `EMAIL`, `PHONE`, `SSN`, `CREDIT_CARD`, `IP_ADDRESS`
- `DATE_OF_BIRTH`, `ADDRESS`, `PASSPORT_NUMBER`, `BANK_ACCOUNT`
- `CUSTOM` — org-defined regex patterns

**Redaction modes (SRS PII-02):**
- `mask` — replace with `[REDACTED_<TYPE>]`
- `hash` — replace with `sha256(value)[:12]`
- `tokenize` — replace with reversible token, store mapping in KVStore

---

## 2. Architecture Mapping

```
plugins/piiredactor/           (NEW independent Go module)
├── go.mod
├── plugin.go                  PIIRedactorPlugin struct
├── detector.go                PIIDetector — entity detection
├── redactor.go                PIIRedactor — apply redaction mode
├── detectors/
│   ├── regex.go               Built-in regex patterns for each entity type
│   ├── ner.go                 NER-based detection (optional LLM call)
│   └── luhn.go                Luhn algorithm for credit card validation
├── tokenstore.go              Reversible token mapping (via KVStore)
└── config.go                  PIIRedactorConfig

framework/kvstore/
└── kvstore.go                 (EXISTING) Used for tokenize-mode token storage
```

---

## 3. Configuration Schema

```go
// plugins/piiredactor/config.go

type PIIRedactorConfig struct {
    Enabled         bool
    Scope           []RedactionScope  // "request", "response"
    Mode            RedactionMode     // default mode
    EntityTypes     []EntityTypeConfig
    CustomPatterns  []CustomPattern
    NERConfig       *NERConfig        // optional AI-based NER
    TokenStoreTTL   time.Duration     // for tokenize mode (default: 24h)
    LogViolations   bool              // log PII detection events (without content)
}

type RedactionScope string
const (
    ScopeRequest  RedactionScope = "request"
    ScopeResponse RedactionScope = "response"
)

type RedactionMode string
const (
    ModeMask      RedactionMode = "mask"
    ModeHash      RedactionMode = "hash"
    ModeTokenize  RedactionMode = "tokenize"
)

type EntityTypeConfig struct {
    Type    PIIEntityType
    Mode    RedactionMode  // override default mode
    Enabled bool
}

type PIIEntityType string
const (
    EntityPersonName    PIIEntityType = "PERSON_NAME"
    EntityEmail         PIIEntityType = "EMAIL"
    EntityPhone         PIIEntityType = "PHONE"
    EntitySSN           PIIEntityType = "SSN"
    EntityCreditCard    PIIEntityType = "CREDIT_CARD"
    EntityIPAddress     PIIEntityType = "IP_ADDRESS"
    EntityDOB           PIIEntityType = "DATE_OF_BIRTH"
    EntityAddress       PIIEntityType = "ADDRESS"
    EntityPassport      PIIEntityType = "PASSPORT_NUMBER"
    EntityBankAccount   PIIEntityType = "BANK_ACCOUNT"
    EntityCustom        PIIEntityType = "CUSTOM"
)

type CustomPattern struct {
    Name    string
    Pattern string  // PCRE regex
    Mode    RedactionMode
}

type NERConfig struct {
    Provider  string   // bifrost provider name
    Model     string
    APIKey    string
    Threshold float64
}
```

---

## 4. Plugin Implementation

```go
// plugins/piiredactor/plugin.go

type PIIRedactorPlugin struct {
    config     PIIRedactorConfig
    detector   *PIIDetector
    redactor   *PIIRedactor
    tokenStore schemas.KVStore
    mu         sync.RWMutex
}

func (p *PIIRedactorPlugin) GetName() string { return "piiredactor" }

func (p *PIIRedactorPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (
    *schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
    
    if !p.config.Enabled || !containsScope(p.config.Scope, ScopeRequest) {
        return req, nil, nil
    }
    
    // Redact all message content fields
    redactedReq, entities, err := p.redactRequest(ctx, req)
    if err != nil {
        return req, nil, nil  // fail open
    }
    
    // Store detected entity map in context for de-tokenization in PostHook
    if len(entities) > 0 {
        ctx.SetValue(piiEntitiesKey, entities)
    }
    
    return redactedReq, nil, nil
}

func (p *PIIRedactorPlugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (
    *schemas.BifrostResponse, *schemas.BifrostError, error) {
    
    if !p.config.Enabled || resp == nil {
        return resp, bifrostErr, nil
    }
    
    if containsScope(p.config.Scope, ScopeResponse) {
        redactedResp, _, err := p.redactResponse(ctx, resp)
        if err != nil {
            return resp, bifrostErr, nil
        }
        return redactedResp, bifrostErr, nil
    }
    
    return resp, bifrostErr, nil
}
```

---

## 5. Detector

```go
// plugins/piiredactor/detector.go

type DetectedEntity struct {
    Type    PIIEntityType
    Value   string
    Start   int
    End     int
}

type PIIDetector struct {
    patterns map[PIIEntityType]*regexp.Regexp
    custom   []*customPattern
    ner      *NERDetector  // optional
}

func NewPIIDetector(config PIIRedactorConfig) *PIIDetector {
    p := &PIIDetector{
        patterns: builtinPatterns(),
    }
    for _, cp := range config.CustomPatterns {
        p.custom = append(p.custom, &customPattern{
            name:    cp.Name,
            re:      regexp.MustCompile(cp.Pattern),
            mode:    cp.Mode,
        })
    }
    if config.NERConfig != nil {
        p.ner = newNERDetector(*config.NERConfig)
    }
    return p
}

// builtinPatterns returns compiled regex for all built-in PII types
func builtinPatterns() map[PIIEntityType]*regexp.Regexp {
    return map[PIIEntityType]*regexp.Regexp{
        EntityEmail:      regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
        EntityPhone:      regexp.MustCompile(`(\+?1[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}`),
        EntitySSN:        regexp.MustCompile(`\b\d{3}[-\s]?\d{2}[-\s]?\d{4}\b`),
        EntityCreditCard: regexp.MustCompile(`\b(?:4[0-9]{12}(?:[0-9]{3})?|5[1-5][0-9]{14}|3[47][0-9]{13}|6011[0-9]{12})\b`),
        EntityIPAddress:  regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`),
        EntityDOB:        regexp.MustCompile(`\b(?:0[1-9]|1[0-2])[/\-](?:0[1-9]|[12]\d|3[01])[/\-](?:19|20)\d{2}\b`),
        // ... more patterns
    }
}

func (d *PIIDetector) Detect(text string, enabledTypes []PIIEntityType) []DetectedEntity {
    var entities []DetectedEntity
    for _, t := range enabledTypes {
        re, ok := d.patterns[t]
        if !ok {
            continue
        }
        matches := re.FindAllStringIndex(text, -1)
        for _, m := range matches {
            val := text[m[0]:m[1]]
            // Extra validation for credit cards: Luhn check
            if t == EntityCreditCard && !luhn.Check(val) {
                continue
            }
            entities = append(entities, DetectedEntity{Type: t, Value: val, Start: m[0], End: m[1]})
        }
    }
    // Also run custom patterns
    for _, cp := range d.custom {
        matches := cp.re.FindAllStringIndex(text, -1)
        for _, m := range matches {
            entities = append(entities, DetectedEntity{Type: EntityCustom, Value: text[m[0]:m[1]], Start: m[0], End: m[1]})
        }
    }
    return entities
}
```

---

## 6. Redactor

```go
// plugins/piiredactor/redactor.go

type PIIRedactor struct {
    config     PIIRedactorConfig
    tokenStore schemas.KVStore
}

// RedactText applies redaction to detected entities, returns redacted text + token map
func (r *PIIRedactor) RedactText(original string, entities []DetectedEntity) (string, map[string]string, error) {
    // Sort by start position descending to replace from end → start (preserve indices)
    sort.Slice(entities, func(i, j int) bool {
        return entities[i].Start > entities[j].Start
    })
    
    tokenMap := make(map[string]string)  // token → original value (for tokenize mode)
    result := []byte(original)
    
    for _, entity := range entities {
        mode := r.modeForType(entity.Type)
        replacement := r.computeReplacement(entity, mode, tokenMap)
        result = append(result[:entity.Start], append([]byte(replacement), result[entity.End:]...)...)
    }
    
    return string(result), tokenMap, nil
}

func (r *PIIRedactor) computeReplacement(e DetectedEntity, mode RedactionMode, tokenMap map[string]string) string {
    switch mode {
    case ModeMask:
        return fmt.Sprintf("[REDACTED_%s]", e.Type)
    case ModeHash:
        h := sha256.Sum256([]byte(e.Value))
        return fmt.Sprintf("[HASH_%s_%s]", e.Type, hex.EncodeToString(h[:])[:12])
    case ModeTokenize:
        token := fmt.Sprintf("PII_%s_%s", e.Type, uuid.New().String()[:8])
        tokenMap[token] = e.Value
        return token
    default:
        return fmt.Sprintf("[REDACTED_%s]", e.Type)
    }
}
```

---

## 7. Tokenize Mode — KVStore Integration

```go
// For tokenize mode, store token→value mapping in KVStore with TTL
// This enables de-tokenization in PostLLMHook or by the caller

func (p *PIIRedactorPlugin) storeTokens(ctx context.Context, tokenMap map[string]string) error {
    for token, val := range tokenMap {
        key := fmt.Sprintf("pii:token:%s", token)
        if err := p.tokenStore.Set(ctx, key, val, p.config.TokenStoreTTL); err != nil {
            return err
        }
    }
    return nil
}

// GET /api/pii/detokenize  — operator+ can retrieve original value by token
// This endpoint requires RBAC "pii" "admin" permission and is audit-logged
```

---

## 8. Plugin Execution Order Constraint

```
// CRITICAL: piiredactor MUST run before logging plugin
// So that log store never captures PII in request/response bodies

Recommended order:
1. litellmcompat
2. mocker
3. governance
4. guardrails
5. piiredactor   ← HERE (before semanticcache and logging)
6. semanticcache
7. logging       ← logging sees already-redacted content
8. telemetry
9. otel
```

---

## 9. Management API

```go
// transports/bifrost-http/handlers/piiredactor.go

// GET  /api/pii/config           — get current PII config
// PUT  /api/pii/config           — update PII config (admin+)
// POST /api/pii/test             — dry-run: detect & redact sample text
// GET  /api/pii/violations       — PII detection events log
// POST /api/pii/detokenize       — resolve token to original value (super_admin only, audit-logged)
// GET  /api/pii/stats            — detection count by entity type over time
```

---

## 10. UI Components

```
ui/app/enterprise/pii/
├── page.tsx                  — PII config dashboard
├── components/
│   ├── EntityTypeMatrix.tsx  — Toggle matrix for each entity type + mode
│   ├── CustomPatterns.tsx    — Regex pattern manager
│   ├── TestPanel.tsx         — Live detection + redaction preview
│   └── ViolationChart.tsx    — Detection frequency by entity type
```

---

## 11. Performance Notes

- Regex detection: O(n) per pattern, compiled once on init. For average chat message (<2KB), total detection time <1ms.
- NER-based detection: optional, adds ~200–500ms latency (async for response scope).
- Tokenize mode: one KVStore write per entity found. Use batch writes if available.
- All entity detection is purely in-process — no external calls for regex/Luhn modes.

---

## 12. Testing Strategy

- Unit: Each detector type with known PII fixtures (emails, credit cards with valid Luhn)
- Unit: Redactor produces correct replacement for each mode (mask, hash, tokenize)
- Integration: `PreLLMHook` redacts before log plugin captures content
- E2E: Submit chat with known PII patterns, verify log store shows `[REDACTED_EMAIL]`
