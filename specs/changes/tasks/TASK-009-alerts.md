# TASK-009 — Alert Channels

**Feature:** Threshold-Based Alerting  
**TECH Spec:** [TECH-009-alerts.md](../TECH-009-alerts.md)  
**Phase:** 4 (Intelligence)  
**Depends on:** TASK-014 (license), TASK-007 (Clustering — shared metrics state)  
**Estimate:** 5 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

The alerting system monitors real-time metrics and delivers notifications through configurable channels (Slack, PagerDuty, webhook, email). An `AlertEvaluator` runs every 30 seconds checking all enabled rules and transitions state through a state machine (inactive → firing → resolved).

**Alert types:**
- `budget_threshold` — VK/team budget reaches X% of limit
- `rate_limit_threshold` — request/token rate reaches X% of limit
- `error_rate` — provider error rate exceeds threshold within time window
- `latency_p95` — P95 latency exceeds threshold
- `cost_spike` — cost per hour exceeds rolling average by X%
- `provider_down` — error rate > 50% for > 5 consecutive minutes
- `guardrail_violation_spike` — violations exceed N per minute

---

## Tasks

### TASK-009-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/alerting.go` — `AlertRuleTable`, `AlertChannelTable`, `AlertStateTable`, `AlertHistoryTable`
- Migration file

**Acceptance criteria:**
- [ ] Migration runs cleanly; idempotent
- [ ] `AlertHistoryTable.Timestamp` indexed for range queries

---

### TASK-009-02 — `plugins/alerting` Go module scaffold

**Files to create:**
- `plugins/alerting/go.mod`
- `plugins/alerting/plugin.go` — `AlertingPlugin` (ObservabilityPlugin + LLMPlugin)
- `plugins/alerting/config.go` — `AlertingConfig`
- `plugins/alerting/evaluator.go` — `AlertEvaluator` (30s tick loop)
- `plugins/alerting/state.go` — `AlertStateManager`, state machine
- `plugins/alerting/ratelimiter.go` — dedup + repeat interval enforcement
- `plugins/alerting/metrics_source.go` — `MetricsSource` interface + DB-backed implementation

**Acceptance criteria:**
- [ ] `AlertEvaluator.Start()` runs evaluation goroutine every 30 seconds
- [ ] `AlertEvaluator.Stop()` drains and exits cleanly
- [ ] Plugin also implements `ObservabilityPlugin.Inject()` to feed per-trace metrics into in-memory aggregator
- [ ] Plugin registered in `go.work`

---

### TASK-009-03 — Metrics source

**File:** `plugins/alerting/metrics_source.go`

**`MetricsSource` implementation must provide:**
```go
GetProviderErrorRate(provider string, window time.Duration) (float64, error)
GetBudgetUsagePct(vkID, teamID string) (float64, error)
GetP95Latency(provider, model string, window time.Duration) (float64, error)
GetCostPerHour() (float64, error)
GetGuardrailViolationRate(window time.Duration) (int64, error)
```

**Sources:**
- Error rate + latency: from `ProviderMetricsTable` (TECH-006) or in-memory ring buffer
- Budget usage: from governance plugin shared state (Redis or DB)  
- Guardrail violations: from `GuardrailViolationTable` (TECH-003)

**Acceptance criteria:**
- [ ] All metrics available via both DB query path (cold start) and in-memory path (steady state)
- [ ] Redis unavailable → fallback to DB query with warn log
- [ ] Mock `MetricsSource` provided for unit testing evaluator

---

### TASK-009-04 — Alert state machine

**File:** `plugins/alerting/state.go`

**State transitions:**
```
inactive → firing   (threshold crossed)
firing → resolved   (threshold no longer crossed)
firing → silenced   (admin silenced)
silenced → firing   (silence expired or unsilenced)
```

**Acceptance criteria:**
- [ ] `Transition()` correctly fires notifications only on state changes (not every evaluation)
- [ ] Re-notify while in `firing` state only if `RepeatInterval` has elapsed since last notification
- [ ] `StateFiring` with `renotify`: notification sent with "repeating" event type
- [ ] `StateResolved` notification sent when firing → resolved transition
- [ ] State persisted to `AlertStateTable` after every transition for crash recovery

---

### TASK-009-05 — Channel implementations

**Files to create:**
- `plugins/alerting/channels/channel.go` — `AlertChannel` interface
- `plugins/alerting/channels/slack.go` — Slack Incoming Webhooks
- `plugins/alerting/channels/pagerduty.go` — PagerDuty Events API v2
- `plugins/alerting/channels/webhook.go` — Generic HTTP webhook
- `plugins/alerting/channels/email.go` — SMTP email

**Acceptance criteria per channel:**

| Channel | Fired | Resolved | Retry | Timeout |
|---------|-------|----------|-------|---------|
| Slack | Color-coded attachment | Green "resolved" msg | 3 retries, exp backoff | 10s |
| PagerDuty | `event_action: trigger` | `event_action: resolve` with `dedup_key` | 3 retries | 10s |
| Webhook | Standard JSON payload | Same payload with `event: resolved` | 3 retries | 30s |
| Email | HTML email with severity | Plain text resolved email | 1 retry | 30s |

- [ ] Each channel logs delivery failure as warning (never crashes evaluator)
- [ ] `Send()` is non-blocking from evaluator perspective (channel sends in goroutine with timeout)

---

### TASK-009-06 — Alert rule management API

**Files to create:**
- `transports/bifrost-http/handlers/alerting.go`

**Endpoints:**
```
GET    /api/alerts/rules             — list rules (admin+)
POST   /api/alerts/rules             — create rule (admin+)
GET    /api/alerts/rules/{id}        — get rule
PUT    /api/alerts/rules/{id}        — update rule (admin+)
DELETE /api/alerts/rules/{id}        — delete rule (admin+)
POST   /api/alerts/rules/{id}/test   — send test notification to all configured channels
POST   /api/alerts/rules/{id}/silence?duration=1h  — silence rule
POST   /api/alerts/rules/{id}/unsilence

GET    /api/alerts/channels          — list channels (admin+)
POST   /api/alerts/channels          — create channel (admin+)
PUT    /api/alerts/channels/{id}     — update channel
DELETE /api/alerts/channels/{id}     — delete channel
POST   /api/alerts/channels/{id}/test — send test message

GET    /api/alerts/state             — current alert states for all rules
GET    /api/alerts/history?rule_id=&severity=&start=&end=   — alert history
```

**Acceptance criteria:**
- [ ] All endpoints require `alerts` feature enabled
- [ ] `/api/alerts/rules/{id}/test` sends a real notification to all configured channels
- [ ] Channel credentials (SMTP password, PagerDuty key) masked in GET responses

---

### TASK-009-07 — UI: alert dashboard

**Files to create:**
- `ui/app/enterprise/alerts/page.tsx`
- `ui/app/enterprise/alerts/[id]/page.tsx`
- `ui/app/enterprise/alerts/history/page.tsx`
- `ui/app/enterprise/alerts/components/AlertRuleForm.tsx`
- `ui/app/enterprise/alerts/components/ChannelManager.tsx`
- `ui/app/enterprise/alerts/components/FiringAlertBanner.tsx`
- `ui/app/enterprise/alerts/components/AlertTimeline.tsx`
- `ui/app/enterprise/alerts/components/SilenceModal.tsx`

**Acceptance criteria:**
- [ ] `FiringAlertBanner` appears at top of every page when ≥1 critical alert is firing
- [ ] Alert rule form has condition builder: alert type selector → dynamic condition fields
- [ ] Channel manager: add/remove channels with "Send Test" button per channel
- [ ] Alert timeline: fired/resolved events on a horizontal timeline chart
- [ ] Page inside `<EnterpriseGate feature="alerts">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Unit test: state machine transitions (all 5 states × events)
- [ ] Unit test: Slack payload format matches expected JSON structure
- [ ] Unit test: PagerDuty dedup key matches rule ID for both trigger and resolve
- [ ] Integration test: mock metrics source triggers `budget_threshold` alert → Slack webhook called
- [ ] Integration test: metric recovers → resolve notification sent
- [ ] Integration test: silenced alert → no notification sent during silence period
- [ ] `make build` passes
