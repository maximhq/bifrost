# Change Request: Enterprise Edition — PRD Revision
## CR-ENT-001: Bifrost Enterprise for Large-Scale Organizations

**CR ID:** CR-ENT-001  
**Type:** Feature Addition  
**Author:** Product Owner (Enterprise)  
**Date:** 2026-04-08  
**Status:** Proposed  
**Target Version:** v2.0-enterprise  
**References:**  
- [PRD.md](../PRD.md) — baseline document  
- [enterprise-missing-features.md](../bugs/enterprise-missing-features.md) — gap analysis  
- [URD.md](../URD.md) — user requirements baseline  

---

## 1. Business Context & Motivation

### 1.1 Tại Sao Cần Thay Đổi

Tổ chức với hơn **10,000 nhân viên** đang triển khai AI ở quy mô toàn cầu. Bifrost OSS giải quyết tốt bài toán kỹ thuật (unified API, failover, caching), nhưng **hoàn toàn thiếu** các kiểm soát bắt buộc cho môi trường doanh nghiệp quy mô lớn:

| Yêu cầu doanh nghiệp | Bifrost OSS | Khoảng cách |
|----------------------|-------------|-------------|
| Kiểm soát truy cập theo vai trò | Không có | Bất kỳ ai có tài khoản đều làm được mọi thứ |
| Nhật ký kiểm toán bất biến | Không có | Không đáp ứng SOC 2 / ISO 27001 |
| Phát hiện và che dấu PII | Không có | Vi phạm GDPR / PDPA khi log |
| Phát hiện nội dung an toàn | Không có | Không có guardrails cho 100+ team |
| Cấp phát người dùng từ Okta/AD | Không có | Quản lý thủ công không khả thi ở 10K người |
| Cụm đa nút (HA) | Không có | Single point of failure |
| Cảnh báo chủ động | Không có | Phát hiện sự cố chậm |
| Tích hợp HashiCorp Vault | Không có | Keys lưu raw trong DB |
| Xuất log ra BigQuery/Datadog | Không có | BI/SIEM hiện tại không có dữ liệu AI |

### 1.2 Cơ Hội Kinh Doanh

Triển khai thành công Bifrost Enterprise giúp:
- **Centralize** 150+ team LLM calls vào 1 gateway → giảm 70% chi phí vận hành API key
- **Automate** onboarding người dùng mới qua SCIM (hiện mất 2 tuần thủ công/người)
- **Enforce** chính sách PII/guardrails trước khi log → đáp ứng kiểm toán GDPR
- **Cluster** 5-node HA → SLA 99.99% thay vì 99.9% hiện tại

---

## 2. Đề Xuất Thay Đổi PRD

### 2.1 Bổ Sung Persona Mới — "The Enterprise Security Officer"

**Thêm vào Section 4 — User Personas:**

```
### Persona E: "The Enterprise Security Officer" (mới — Enterprise gating)

**Name:** Taylor, CISO  
**Company:** Tập đoàn tài chính 10,000+ nhân viên, 150+ team AI  
**Situation:** 3 sự cố rò rỉ dữ liệu khách hàng qua LLM prompt trong 6 tháng.  
  Legal yêu cầu: (1) kiểm toán mọi thay đổi cấu hình AI gateway,  
  (2) PII không được lưu trong logs, (3) mọi truy cập phải qua SSO corporate.  
**Goal:** Triển khai Bifrost với toàn bộ enterprise controls trước khi  
  cho phép 150 team dùng trong môi trường production customer-facing.  
**Success:** Vượt qua AI Governance audit nội bộ. Zero PII trong logs.  
  Mọi người dùng đều được provisioned/deprovisioned tự động qua Okta.
```

### 2.2 Bổ Sung Persona Mới — "The ML Platform Lead (Large Scale)"

```
### Persona F: "The ML Platform Lead — Large Scale" (mới)

**Name:** River, Head of ML Platform  
**Company:** 10,000 nhân viên, 150 team product  
**Situation:** Mỗi tuần có 5-10 team mới muốn dùng LLM. Hiện tại phải cấp key thủ công,  
  không biết team nào đang tiêu bao nhiêu, không có cơ chế giới hạn.  
  Khi budget vượt $50K/tháng, chỉ biết sau khi nhận hóa đơn.  
**Goal:** Tự động hóa toàn bộ vòng đời team: onboard → phân allocation →  
  monitor → alert → offboard. Chạy 5-node cluster để không có SPOF.  
**Success:** 150 team tự phục vụ. Budget alert trước khi vượt 80%.  
  Zero downtime trong 12 tháng.
```

---

### 2.3 Cập Nhật Section 5.3 — P2 Enterprise Enablement

**Thay thế toàn bộ Section 5.3 hiện tại bằng:**

#### 5.3 P2 — Enterprise Enablement (Required for Production at Scale)

| Feature | Mô Tả Chi Tiết | Persona |
|---------|---------------|---------|
| **RBAC** | Phân quyền 5 vai trò: Owner, Admin, Operator, Developer, Viewer. Áp dụng cho cả UI và API. Mỗi endpoint được map với quyền tối thiểu được phép. | E, F |
| **Audit Logs** | Nhật ký bất biến mọi thay đổi cấu hình: ai làm gì, khi nào, trước/sau giá trị nào. Queryable. Không có API xóa audit log. | E |
| **Guardrails** | Rule engine (keyword / regex / AI classifier) scan request+response. Actions: block, warn, redact. Áp dụng toàn cục hoặc per-provider. | E, F |
| **PII Redactor** | Detect và redact PII (email, phone, CCID, SSN, tên người) trước khi lưu vào log. Hỗ trợ regex và ML engine. Per-provider config. | E |
| **SCIM / SSO** | SCIM 2.0 cho Okta/Azure AD. SAML 2.0 và OIDC SSO. JIT provisioning. Role mapping từ IdP groups. | E, F |
| **Adaptive Routing** | Routing động dựa trên p95 latency, error rate, và cost/token thu thập real-time từ các provider. Configurable weights. | F |
| **Multi-node Clustering** | Triển khai 3-5 node với shared state qua PostgreSQL + Redis pub/sub. In-memory cache invalidation cross-node. Distributed budget/rate-limit counters. | F |
| **Alert Channels** | Gửi cảnh báo qua Webhook, Slack, PagerDuty, Email khi: budget vượt 80%/100%, error rate tăng đột biến, guardrail vi phạm, node down. | E, F |
| **Vault Support** | Resolve API keys từ HashiCorp Vault tại runtime. Hỗ trợ Token, AppRole, K8s auth. Auto-renew dynamic secrets. | E |
| **Large Payload Optimization** | Streaming multipart cho audio/video files >50MB. Backpressure-aware SSE. Per-provider `stream_threshold_bytes`. | F |
| **MCP Tool Groups** | Nhóm MCP tools thành tập có thể tái sử dụng và gán cho nhiều Virtual Key. Đơn giản hóa quản lý khi có 50+ MCP clients. | F |
| **User Groups** | Nhóm người dùng để gán RBAC roles hàng loạt. Sync với IdP groups qua SCIM. | E, F |
| **Data Connectors** | Export inference logs định kỳ ra BigQuery, Datadog, S3, Elasticsearch. Credential lưu mã hóa. Sync status monitoring. | E, F |
| **License Enforcement** | Validate enterprise license JWT tại startup. Feature-gate runtime. Grace period 7 ngày khi license sắp hết. API trả về licensed features. | E, F |

---

### 2.4 Cập Nhật Section 5.4 — P3 Nice-to-Have (Bổ sung mục mới)

**Thêm vào bảng P3:**

| Feature | Mô Tả |
|---------|--------|
| AI Usage Showback Dashboard | Dashboard per-team hiển thị chi phí, token, và latency để chargeback nội bộ |
| Compliance Report Export | Export PDF/CSV báo cáo compliance định kỳ (SOC 2, GDPR, ISO 27001 format) |
| Anomaly Detection | ML-based phát hiện traffic pattern bất thường (prompt injection, data exfiltration) |
| Multi-tenant Namespace | Isolate config/data giữa các business unit trong cùng cluster |
| Fine-grained Audit Log Retention | Configurable retention policy (30 ngày / 1 năm / vĩnh viễn) |
| Custom Guardrail Classifiers | Upload custom ML model làm guardrail classifier |

---

### 2.5 Cập Nhật Section 6.2 — Feature Matrix

**Bổ sung cột và hàng mới vào Feature Matrix:**

| Feature | OSS | Enterprise | Ghi chú |
|---------|-----|------------|---------|
| Tất cả inference endpoints | ✅ | ✅ | |
| Provider management | ✅ | ✅ | |
| Virtual keys + basic budgets | ✅ | ✅ | |
| Routing rules (CEL) | ✅ | ✅ | |
| Request logging | ✅ | ✅ | |
| Prometheus metrics | ✅ | ✅ | |
| OpenTelemetry | ✅ | ✅ | |
| Semantic caching | ✅ | ✅ | |
| Plugin system | ✅ | ✅ | |
| MCP gateway | ✅ | ✅ | |
| Async inference | ✅ | ✅ | |
| SQLite persistence | ✅ | ✅ | |
| PostgreSQL persistence | ✅ | ✅ | |
| Helm chart | ✅ | ✅ | |
| Governance hierarchy (team/customer) | ✅ | ✅ | |
| **RBAC (5 roles)** | ❌ | ✅ | **Mới** |
| **Audit logs (immutable)** | ❌ | ✅ | **Mới** |
| **Guardrails (keyword/regex/AI)** | ❌ | ✅ | **Mới** |
| **PII redactor** | ❌ | ✅ | **Mới** |
| **SCIM 2.0 / SAML / OIDC SSO** | ❌ | ✅ | **Mới** |
| **Adaptive routing (latency/cost/error-aware)** | ❌ | ✅ | **Mới** |
| **Multi-node clustering (3-5 nodes)** | ❌ | ✅ | **Mới** |
| **Alert channels (Slack/PD/Webhook/Email)** | ❌ | ✅ | **Mới** |
| **HashiCorp Vault integration** | ❌ | ✅ | **Mới** |
| **Large payload optimization (>50MB)** | ❌ | ✅ | **Mới** |
| **MCP tool groups** | ❌ | ✅ | **Mới** |
| **User groups** | ❌ | ✅ | **Mới** |
| **Data connectors (BigQuery/Datadog/S3)** | ❌ | ✅ | **Mới** |
| **License enforcement** | ❌ | ✅ | **Mới** |
| **AI Usage Showback Dashboard** | ❌ | ✅ | **Mới (P3)** |
| **Compliance report export** | ❌ | ✅ | **Mới (P3)** |

---

### 2.6 Cập Nhật Section 3.1 — Goals & Success Metrics

**Bổ sung metrics Enterprise:**

| Mục tiêu | Metric | Target |
|---------|--------|--------|
| RBAC coverage | Tỷ lệ API endpoints có permission check | 100% |
| Audit trail completeness | Tỉ lệ admin action được log | 100% |
| PII redaction effectiveness | PII còn xuất hiện trong logs | 0 |
| SSO adoption | % người dùng đăng nhập qua SSO | > 95% |
| Cluster HA | Uptime với 1 node fail | > 99.99% |
| Alert latency | Thời gian từ vi phạm budget → nhận alert | < 60 giây |
| Data connector lag | Độ trễ log xuất hiện trong BigQuery | < 5 phút |

---

### 2.7 Cập Nhật Section 10.2 — Enterprise v1.0 Release Criteria

**Thay thế checklist Enterprise v1.0:**

```
### 10.2 Enterprise v1.0 Readiness

Ngoài tiêu chí OSS:

Infrastructure:
- [ ] 5-node cluster test với PostgreSQL shared state — không mất request khi 2 node down
- [ ] Cross-node cache invalidation latency < 500ms
- [ ] Zero distributed lock deadlock trong 72h stress test

Security & Compliance:
- [ ] RBAC enforced trên 100% API endpoints — pentest xác nhận
- [ ] Audit log không thể xóa/sửa ngay cả khi có DB admin access
- [ ] PII redaction: 0 PII còn trong logs sau 10,000 synthetic test requests
- [ ] Guardrails block 100% test cases với known-bad patterns
- [ ] HashiCorp Vault key resolution hoạt động với Token, AppRole, K8s auth

Identity & Access:
- [ ] SCIM provisioning tested với Okta Workforce Identity và Azure AD
- [ ] SAML 2.0 SSO tested với Okta và ADFS
- [ ] OIDC SSO tested với Google Workspace và Azure AD
- [ ] JIT provisioning tạo user và assign role đúng từ IdP group
- [ ] User deprovisioned trong < 30 giây sau khi bị xóa trong IdP

Alerting:
- [ ] Budget alert gửi trong < 60 giây sau khi vượt ngưỡng 80%/100%
- [ ] Slack, Webhook, PagerDuty, Email đều tested end-to-end
- [ ] Guardrail violation alert hoạt động trong < 10 giây

Data & Observability:
- [ ] BigQuery connector export lag < 5 phút với 10K requests/min
- [ ] Datadog connector hiện đúng log attributes
- [ ] Adaptive routing convergence < 60 giây sau khi provider degraded

Licensing:
- [ ] License enforcement: enterprise features trả 403 không có valid license
- [ ] License expiry warning 30 ngày trước khi hết hạn
- [ ] Grace period 7 ngày sau expiry với warning nhưng vẫn hoạt động
```

---

## 3. Đề Xuất Thay Đổi Constraints & Non-Goals

### 3.1 Bổ Sung Constraints Mới (Section 7.1)

| Constraint | Lý Do |
|-----------|-------|
| Audit logs phải append-only tại DB layer | Không được có `UPDATE`/`DELETE` trên bảng audit_logs — yêu cầu SOC 2 |
| PII redaction phải xảy ra TRƯỚC khi ghi log | Đảm bảo PII không bao giờ persist vào storage |
| Cluster state sync phải dùng PostgreSQL LISTEN/NOTIFY hoặc Redis pub/sub | Không được dùng polling — latency yêu cầu < 500ms |
| License key không được lưu trong database | Chỉ config file hoặc environment variable |
| Enterprise features phải gracefully degrade | Không có license → 403 với upgrade message, không crash |

### 3.2 Điều Chỉnh Non-Goals

**Loại ra khỏi Non-Goals (v2.0 sẽ hỗ trợ):**
- ~~Multi-tenant SaaS~~ → **Thay bằng:** Multi-tenant namespace trong self-hosted cluster
- ~~GraphQL API~~ → Vẫn là non-goal

**Giữ nguyên Non-Goals:**
- Training / fine-tuning workflows
- Built-in vector database
- Model serving

---

## 4. Phân Tích Tác Động

### 4.1 Components Cần Thêm Mới

```
plugins/
├── guardrails/          # LLMPlugin: content safety rules
├── piiredactor/         # LLMPlugin: PII detection + redaction
└── adaptiverouting/     # Governance extension: metrics-aware routing

transports/bifrost-http/handlers/
├── rbac.go              # Role management endpoints
├── auditlogs.go         # Audit log query endpoint
├── alertchannels.go     # Alert channel CRUD
├── dataconnectors.go    # Data connector CRUD
├── cluster.go           # Cluster node health endpoints
└── license.go           # License validation & status

framework/
├── configstore/tables/  # 10 new DB tables
├── logstore/bigquery/   # BigQuery connector
├── logstore/datadog/    # Datadog connector  
└── logstore/s3/         # S3/GCS connector

ui/app/enterprise/       # Enterprise UI module (currently absent)
├── components/
│   ├── rbac/
│   ├── audit-logs/
│   ├── guardrails/
│   ├── pii-redactor/
│   ├── scim/
│   ├── adaptive-routing/
│   ├── cluster/
│   ├── alert-channels/
│   ├── mcp-tool-groups/
│   ├── user-groups/
│   └── data-connectors/
└── lib/
    ├── schemas/         # Enterprise TypeScript types
    └── api/             # Enterprise API client
```

### 4.2 Components Cần Sửa Đổi

| Component | Thay Đổi |
|-----------|---------|
| `transports/bifrost-http/handlers/middlewares.go` | Thêm RBAC middleware cho mọi route |
| `transports/bifrost-http/handlers/governance.go` | Thêm audit log writer vào mọi POST/PUT/DELETE |
| `plugins/governance/tracker.go` | Emit alert events khi budget/rate-limit vi phạm |
| `plugins/governance/routing.go` | Tích hợp adaptive routing metrics |
| `plugins/logging/` | Thêm PII redaction pre-processor trước khi ghi |
| `framework/configstore/` | Thêm 10 bảng DB mới + migrations |
| `transports/config.schema.json` | Thêm `vault`, `clustering`, `license_key` blocks |
| `transports/bifrost-http/handlers/session.go` | Thêm SAML/OIDC SSO flow |

---

## 5. Rủi Ro & Giảm Thiểu

| Rủi Ro | Xác Suất | Tác Động | Giảm Thiểu |
|--------|---------|---------|-----------|
| SCIM spec phức tạp — delay integration | Cao | Trung bình | MVP: chỉ User provisioning, bỏ Group sync cho v2.0 |
| Vault auth edge cases (K8s IRSA, GCP WI) | Trung bình | Cao | Test 3 auth methods: Token, AppRole, K8s. IRSA là v2.1 |
| PII regex FP rate cao gây block hợp lệ | Cao | Cao | Mặc định: `warn` không phải `block`. Người dùng phải chủ động bật `block`. |
| Cluster state sync latency > 500ms | Thấp | Cao | Redis pub/sub < 10ms. Fallback: PostgreSQL LISTEN/NOTIFY (~100ms) |
| License enforcement phá vỡ OSS build | Thấp | Cao | Feature gate chỉ qua `lib.IsFeatureEnabled()` — OSS build luôn trả `false` gracefully |
| BigQuery connector billing surprise | Thấp | Trung bình | Document rõ: connector dùng Streaming API (có phí), có toggle opt-in |

---

## 6. Phương Thức Phát Triển & Timeline

### 6.1 Phân Chia Sprint

| Sprint | Focus | Deliverables |
|--------|-------|-------------|
| S1-S2 | Foundation | License enforcement, DB migrations, RBAC middleware skeleton |
| S3-S4 | Identity | SCIM 2.0, OIDC SSO, SAML 2.0, User Groups |
| S5-S6 | Compliance | Audit logs, PII Redactor plugin, Guardrails plugin |
| S7-S8 | Operations | Alert channels, Adaptive routing, Multi-node clustering |
| S9-S10 | Data & Secret | Data connectors (BQ, Datadog, S3), Vault support |
| S11-S12 | UI & Polish | Enterprise UI module, MCP Tool Groups, Performance testing |

### 6.2 MVP für Deployment Commitment (S1-S6)

**Tối thiểu cần có trước khi đưa vào pilot với 3 team nội bộ:**
1. RBAC (owner/admin/viewer minimum)
2. SCIM với Okta
3. Audit logs
4. PII Redactor (warn mode)
5. License enforcement

---

*Đề xuất này cần được review bởi: CTO, CISO, Head of ML Platform, Legal (GDPR), Security Engineering.*
