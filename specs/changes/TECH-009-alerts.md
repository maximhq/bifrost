# TECH-009 — Alert Channels

**Feature ID:** ALERT  
**SRS Reference:** §3.19 (ALERT-01 → ALERT-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement a threshold-based alerting system that monitors real-time metrics (cost, token usage, error rates, latency) and delivers notifications through configurable channels (Slack, PagerDuty, webhook, email).

**Alert trigger types (SRS ALERT-01):**
- `budget_threshold` — virtual key / team / customer budget reaches X% of limit
- `rate_limit_threshold` — request/token rate reaches X% of limit
- `error_rate` — provider error rate exceeds threshold within time window
- `latency_p95` — P95 latency exceeds threshold
- `cost_spike` — cost per hour exceeds rolling average by X%
- `provider_down` — provider error rate > 50% for > 5 consecutive minutes
- `guardrail_violation_spike` — guardrail violations exceed N per minute

---

## 2. Architecture Mapping

```
plugins/alerting/                  (NEW independent Go module)
├── go.mod
├── plugin.go                      AlertingPlugin (LLMPlugin + ObservabilityPlugin)
├── evaluator.go                   AlertEvaluator — threshold evaluation loop
├── channels/
│   ├── channel.go                 AlertChannel interface
│   ├── slack.go                   SlackChannel
│   ├── pagerduty.go               PagerDutyChannel
│   ├── webhook.go                 WebhookChannel
│   └── email.go                   EmailChannel (SMTP)
├── state.go                       Alert state machine (firing/resolved/silenced)
├── ratelimiter.go                 Alert dedup + rate limiting
└── config.go                      AlertingConfig

framework/configstore/tables/
└── alerting.go                    AlertRuleTable, AlertStateTable, AlertHistoryTable

transports/bifrost-http/
└── handlers/alerting.go           (NEW) /api/alerts/* CRUD
```

---

## 3. Configuration Schema

```go
// plugins/alerting/config.go

type AlertingConfig struct {
    Enabled   bool
    Rules     []AlertRule
    Channels  []ChannelConfig
}

type AlertRule struct {
    ID            string
    Name          string
    Enabled       bool
    Type          AlertType
    Condition     AlertCondition
    Channels      []string        // channel IDs
    Severity      AlertSeverity
    RepeatInterval time.Duration  // re-alert interval while firing (default: 1h)
    ResolveAfter  time.Duration   // consider resolved after N minutes
}

type AlertType string
const (
    AlertBudgetThreshold       AlertType = "budget_threshold"
    AlertRateLimitThreshold    AlertType = "rate_limit_threshold"
    AlertErrorRate             AlertType = "error_rate"
    AlertLatencyP95            AlertType = "latency_p95"
    AlertCostSpike             AlertType = "cost_spike"
    AlertProviderDown          AlertType = "provider_down"
    AlertGuardrailSpike        AlertType = "guardrail_violation_spike"
)

type AlertCondition struct {
    // Budget / rate limit threshold
    ThresholdPct  float64       // % of limit (e.g., 80.0 = alert at 80%)
    VirtualKeyID  string        // empty = all VKs
    TeamID        string
    
    // Error rate
    ErrorRatePct  float64       // e.g., 20.0 = 20% error rate
    Provider      string        // empty = any provider
    Window        time.Duration // measurement window (default: 5m)
    
    // Latency
    LatencyMs     float64       // P95 threshold in ms
    
    // Cost spike
    SpikePct      float64       // % above rolling average
    SpikeWindow   time.Duration // comparison window
    
    // Count-based
    CountPer      time.Duration // per time unit
    Count         int64
}

type AlertSeverity string
const (
    SeverityCritical AlertSeverity = "critical"
    SeverityHigh     AlertSeverity = "high"
    SeverityMedium   AlertSeverity = "medium"
    SeverityLow      AlertSeverity = "low"
)

type ChannelConfig struct {
    ID   string
    Type ChannelType
    // Slack
    SlackWebhookURL string
    SlackChannel    string
    // PagerDuty
    PagerDutyKey    string
    PagerDutySeverity string
    // Generic Webhook
    WebhookURL     string
    WebhookHeaders map[string]string
    WebhookMethod  string   // "POST" | "PUT"
    // Email
    SMTPHost       string
    SMTPPort       int
    SMTPUser       string
    SMTPPassword   string
    EmailFrom      string
    EmailTo        []string
}

type ChannelType string
const (
    ChannelSlack     ChannelType = "slack"
    ChannelPagerDuty ChannelType = "pagerduty"
    ChannelWebhook   ChannelType = "webhook"
    ChannelEmail     ChannelType = "email"
)
```

---

## 4. Alert Evaluator

```go
// plugins/alerting/evaluator.go

type AlertEvaluator struct {
    config      AlertingConfig
    channels    map[string]AlertChannel
    state       *AlertStateManager
    metricsSource MetricsSource    // reads from logstore + prometheus
    ticker      *time.Ticker
    stop        chan struct{}
}

type MetricsSource interface {
    GetProviderErrorRate(provider string, window time.Duration) (float64, error)
    GetBudgetUsagePct(vkID, teamID string) (float64, error)
    GetP95Latency(provider, model string, window time.Duration) (float64, error)
    GetCostPerHour() (float64, error)
    GetGuardrailViolationRate(window time.Duration) (int64, error)
}

func (e *AlertEvaluator) Start() {
    e.ticker = time.NewTicker(30 * time.Second)  // evaluation interval
    go func() {
        for {
            select {
            case <-e.ticker.C:
                e.evaluateAll()
            case <-e.stop:
                return
            }
        }
    }()
}

func (e *AlertEvaluator) evaluateAll() {
    for _, rule := range e.config.Rules {
        if !rule.Enabled { continue }
        firing, value, err := e.evaluateRule(rule)
        if err != nil {
            logger.Warn("alert evaluation failed", "rule", rule.Name, "error", err)
            continue
        }
        e.state.Transition(rule, firing, value)
    }
}

func (e *AlertEvaluator) evaluateRule(rule AlertRule) (bool, float64, error) {
    switch rule.Type {
    case AlertErrorRate:
        rate, err := e.metricsSource.GetProviderErrorRate(
            rule.Condition.Provider, rule.Condition.Window)
        if err != nil { return false, 0, err }
        return rate >= rule.Condition.ErrorRatePct/100.0, rate, nil
    
    case AlertBudgetThreshold:
        pct, err := e.metricsSource.GetBudgetUsagePct(
            rule.Condition.VirtualKeyID, rule.Condition.TeamID)
        if err != nil { return false, 0, err }
        return pct >= rule.Condition.ThresholdPct, pct, nil
    
    case AlertLatencyP95:
        lat, err := e.metricsSource.GetP95Latency(
            rule.Condition.Provider, "", rule.Condition.Window)
        if err != nil { return false, 0, err }
        return lat >= rule.Condition.LatencyMs, lat, nil
    // ... other types
    }
    return false, 0, fmt.Errorf("unknown alert type: %s", rule.Type)
}
```

---

## 5. Alert State Machine

```go
// plugins/alerting/state.go

type AlertState string
const (
    StateInactive AlertState = "inactive"
    StatePending  AlertState = "pending"    // threshold crossed, waiting for confirm
    StateFiring   AlertState = "firing"
    StateSilenced AlertState = "silenced"
    StateResolved AlertState = "resolved"
)

type AlertStateEntry struct {
    RuleID      string
    State       AlertState
    FiredAt     *time.Time
    ResolvedAt  *time.Time
    LastNotify  *time.Time
    Value       float64     // current metric value
}

type AlertStateManager struct {
    states  sync.Map     // ruleID → *AlertStateEntry
    notifier *AlertNotifier
}

func (m *AlertStateManager) Transition(rule AlertRule, firing bool, value float64) {
    entry, _ := m.states.LoadOrStore(rule.ID, &AlertStateEntry{RuleID: rule.ID, State: StateInactive})
    state := entry.(*AlertStateEntry)
    
    switch {
    case firing && state.State == StateInactive:
        state.State = StateFiring
        now := time.Now()
        state.FiredAt = &now
        state.Value = value
        m.notifier.Send(rule, state, AlertEventFired)
    
    case firing && state.State == StateFiring:
        state.Value = value
        // Re-notify if RepeatInterval elapsed
        if state.LastNotify == nil || time.Since(*state.LastNotify) >= rule.RepeatInterval {
            m.notifier.Send(rule, state, AlertEventRepeating)
        }
    
    case !firing && state.State == StateFiring:
        state.State = StateResolved
        now := time.Now()
        state.ResolvedAt = &now
        m.notifier.Send(rule, state, AlertEventResolved)
    }
}
```

---

## 6. Channel Implementations

### 6.1 Slack

```go
// plugins/alerting/channels/slack.go

type SlackChannel struct {
    webhookURL string
    channel    string
    httpClient *fasthttp.Client
}

func (c *SlackChannel) Send(ctx context.Context, alert AlertNotification) error {
    color := map[AlertSeverity]string{
        SeverityCritical: "#FF0000",
        SeverityHigh:     "#FF6600",
        SeverityMedium:   "#FFCC00",
        SeverityLow:      "#0099FF",
    }[alert.Severity]
    
    payload := map[string]any{
        "channel": c.channel,
        "attachments": []map[string]any{{
            "color":     color,
            "title":     fmt.Sprintf("[%s] %s", alert.Severity, alert.RuleName),
            "text":      alert.Message,
            "fields": []map[string]any{
                {"title": "Current Value", "value": fmt.Sprintf("%.2f", alert.Value), "short": true},
                {"title": "Status",        "value": string(alert.Event), "short": true},
                {"title": "Time",          "value": alert.Timestamp.Format(time.RFC3339), "short": true},
            },
            "footer": "Bifrost AI Gateway",
        }},
    }
    // POST to webhook URL
    return c.post(ctx, c.webhookURL, payload)
}
```

### 6.2 PagerDuty

```go
// plugins/alerting/channels/pagerduty.go
// Uses PagerDuty Events API v2

func (c *PagerDutyChannel) Send(ctx context.Context, alert AlertNotification) error {
    eventAction := "trigger"
    if alert.Event == AlertEventResolved {
        eventAction = "resolve"
    }
    
    payload := map[string]any{
        "routing_key":  c.routingKey,
        "event_action": eventAction,
        "dedup_key":    alert.RuleID,  // ensures resolve matches trigger
        "payload": map[string]any{
            "summary":   alert.Message,
            "severity":  c.severity,
            "source":    "bifrost-ai-gateway",
            "timestamp": alert.Timestamp.Format(time.RFC3339),
            "custom_details": map[string]any{
                "rule_id":  alert.RuleID,
                "value":    alert.Value,
            },
        },
    }
    return c.post(ctx, "https://events.pagerduty.com/v2/enqueue", payload)
}
```

### 6.3 Generic Webhook

```go
// plugins/alerting/channels/webhook.go

func (c *WebhookChannel) Send(ctx context.Context, alert AlertNotification) error {
    // Standard alert payload
    payload := map[string]any{
        "rule_id":   alert.RuleID,
        "rule_name": alert.RuleName,
        "severity":  alert.Severity,
        "event":     alert.Event,    // "fired" | "resolved" | "repeating"
        "value":     alert.Value,
        "message":   alert.Message,
        "timestamp": alert.Timestamp,
    }
    // Add custom headers from config
    // POST/PUT to webhook URL
    return c.send(ctx, c.method, c.url, c.headers, payload)
}
```

---

## 7. Alert History Persistence

```go
// framework/configstore/tables/alerting.go

type AlertRuleTable struct {
    ID              string    `gorm:"primaryKey"`
    Name            string    `gorm:"not null"`
    Type            string
    ConditionJSON   string    `gorm:"type:text"`
    ChannelIDs      string    `gorm:"type:text"`  // JSON array
    Severity        string
    Enabled         bool
    RepeatInterval  int64     // seconds
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type AlertStateTable struct {
    RuleID      string    `gorm:"primaryKey"`
    State       string    `gorm:"not null"`
    FiredAt     *time.Time
    ResolvedAt  *time.Time
    LastNotify  *time.Time
    Value       float64
    UpdatedAt   time.Time
}

type AlertHistoryTable struct {
    ID          string    `gorm:"primaryKey"`
    RuleID      string    `gorm:"index;not null"`
    RuleName    string
    Event       string    // "fired" | "resolved" | "repeating"
    State       string
    Value       float64
    Message     string
    Severity    string    `gorm:"index"`
    Timestamp   time.Time `gorm:"index"`
    NotifiedChannels string  // JSON array of channel IDs that were notified
}
```

---

## 8. Management API

```go
// transports/bifrost-http/handlers/alerting.go

// GET    /api/alerts/rules             — list alert rules
// POST   /api/alerts/rules             — create alert rule (admin+)
// GET    /api/alerts/rules/{id}        — get rule
// PUT    /api/alerts/rules/{id}        — update rule (admin+)
// DELETE /api/alerts/rules/{id}        — delete rule (admin+)
// POST   /api/alerts/rules/{id}/test   — send test notification
// POST   /api/alerts/rules/{id}/silence — silence for duration
// POST   /api/alerts/rules/{id}/unsilence

// GET    /api/alerts/channels          — list channels
// POST   /api/alerts/channels          — create channel
// PUT    /api/alerts/channels/{id}     — update channel
// DELETE /api/alerts/channels/{id}     — delete channel
// POST   /api/alerts/channels/{id}/test — send test message

// GET    /api/alerts/state             — current firing/resolved state for all rules
// GET    /api/alerts/history?rule_id=&severity=&start=&end=  — alert history
```

---

## 9. Plugin as ObservabilityPlugin

The alerting plugin also implements `ObservabilityPlugin` to receive completed traces and feed them into the metrics source:

```go
func (p *AlertingPlugin) Inject(ctx context.Context, trace *schemas.Trace) error {
    // Extract latency, error status, provider, cost from trace
    // Feed into in-memory metrics aggregator for evaluator
    p.metricsAggregator.Record(trace)
    return nil
}
```

---

## 10. UI Components

```
ui/app/enterprise/alerts/
├── page.tsx                    — Alert rule list + current firing state
├── [id]/page.tsx               — Rule editor
├── history/page.tsx            — Alert history timeline
└── components/
    ├── AlertRuleForm.tsx       — Rule type selector + condition builder
    ├── ChannelManager.tsx      — Channel list + test buttons
    ├── FiringAlertBanner.tsx   — Top-of-page banner for active critical alerts
    ├── AlertTimeline.tsx       — Fired/resolved history chart
    └── SilenceModal.tsx        — Silence duration picker
```
