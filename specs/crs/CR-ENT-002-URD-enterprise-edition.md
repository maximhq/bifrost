# Change Request: Enterprise Edition — URD Revision
## CR-ENT-002: User Requirements for Large-Scale Enterprise Deployment

**CR ID:** CR-ENT-002  
**Type:** Feature Addition  
**Author:** Product Owner (Enterprise)  
**Date:** 2026-04-08  
**Status:** Proposed  
**Target Version:** v2.0-enterprise  
**References:**  
- [URD.md](../URD.md) — baseline document  
- [CR-ENT-001-PRD-enterprise-edition.md](CR-ENT-001-PRD-enterprise-edition.md) — PRD CR  
- [enterprise-missing-features.md](../bugs/enterprise-missing-features.md) — gap analysis  

---

## 1. Tóm Tắt Thay Đổi

CR này bổ sung và cập nhật **User Requirements Document** để phản ánh nhu cầu của **tổ chức 10,000+ nhân viên** với đầy đủ enterprise controls. Cụ thể:

1. **Thêm 3 User Class mới**: Security Officer, Compliance Auditor, IdP Administrator
2. **Thêm Section 10 (Security Officer Requirements)**: RBAC, Audit Logs, Guardrails, PII, SSO
3. **Thêm Section 11 (Cluster Operator Requirements)**: HA Clustering, Alert Channels, Adaptive Routing
4. **Thêm Section 12 (Data & Compliance Requirements)**: Data Connectors, Compliance Reports
5. **Cập nhật User Class table** để phản ánh phân quyền rõ ràng hơn
6. **Cập nhật Section 4** (Platform Admin) với Vault và License management
7. **Cập nhật Section 9** (Cross-role) với enterprise session requirements

---

## 2. Thay Đổi Section 2 — User Classes

### 2.1 Thay Thế Bảng User Classes Hiện Tại

| Class | Ai Họ Là | Mục Tiêu Chính | Mới? |
|-------|----------|---------------|------|
| **API Consumer** | Application code, AI frameworks | Gọi bất kỳ LLM qua 1 endpoint ổn định | Giữ |
| **Platform Admin** | DevOps/ML Platform engineer | Cấu hình providers, governance, plugins | Giữ |
| **Developer / Team Lead** | Developer xây dựng feature AI | Xem usage, quản lý key, debug lỗi | Giữ |
| **Go SDK Integrator** | Go developer nhúng Bifrost | Routing/failover/caching trong Go app | Giữ |
| **DevOps / Infra Operator** | Engineer vận hành Bifrost production | Deploy, scale, observe, maintain | Giữ |
| **Viewer** | Stakeholder cần visibility | Monitor chi phí, usage, health | Giữ |
| **Security Officer** | CISO, Security Engineer, Compliance Lead | Kiểm soát truy cập, audit, PII, guardrails | **Mới** |
| **Compliance Auditor** | Internal/External Auditor | Query audit logs, export báo cáo | **Mới** |
| **IdP Administrator** | IT Identity team | Cấu hình SCIM/SSO, quản lý user lifecycle | **Mới** |

---

## 3. Thêm Section 10 — Security Officer Requirements

*Đây là nhu cầu của CISO và Security Engineering team tại tổ chức 10,000+ người.*

---

### 10.1 Role-Based Access Control (RBAC)

**Là** Security Officer,  
**Tôi muốn** định nghĩa và gán vai trò với quyền hạn cụ thể cho từng người dùng,  
**để** chỉ những người có thẩm quyền mới thực hiện được các thao tác nhạy cảm.

**Tiêu chí chấp nhận:**

- Hệ thống hỗ trợ tối thiểu 5 vai trò:

| Vai trò | Quyền hạn |
|---------|----------|
| **Owner** | Toàn quyền, kể cả quản lý license và xóa data |
| **Admin** | Cấu hình providers, governance, plugins, RBAC, SSO |
| **Operator** | Tạo/xóa virtual keys, routing rules, MCP clients |
| **Developer** | Xem logs, tạo VK trong budget được cấp phép |
| **Viewer** | Read-only dashboard, log search |

- Mỗi API endpoint (`POST /api/*`, `PUT /api/*`, `DELETE /api/*`) trả HTTP 403 nếu caller không đủ quyền
- Role assignment có hiệu lực ngay lập tức (không cần logout/login)
- Không có tài khoản nào hoạt động mà không có ít nhất 1 role
- UI hiển thị danh sách người dùng và vai trò của họ, có thể filter và search
- Thao tác xóa role của user cuối cùng có role Owner phải được chặn (tránh lock-out)

---

### 10.2 Immutable Audit Logs

**Là** Security Officer,  
**Tôi muốn** có nhật ký bất biến của mọi thay đổi cấu hình,  
**để** có bằng chứng đầy đủ cho kiểm toán SOC 2 và điều tra sự cố.

**Tiêu chí chấp nhận:**

- Mọi hành động sau đây tạo audit record ngay lập tức:
  - Tạo/sửa/xóa: Provider, Virtual Key, Team, Customer, Routing Rule, Plugin config
  - Tạo/xóa: User, Role assignment
  - Tạo/sửa/xóa: Guardrail rule, PII rule, Alert channel
  - Thay đổi SSO config, license key
  - Login/Logout (bao gồm SSO)
- Mỗi record chứa: `timestamp`, `actor_email`, `actor_ip`, `action`, `resource_type`, `resource_id`, `before` (JSON), `after` (JSON)
- Không có API endpoint nào cho phép sửa hoặc xóa audit records
- Truy vấn audit logs hỗ trợ filter: actor, action, resource_type, date range, resource_id
- Kết quả query có thể export ra CSV
- Audit logs được lưu tối thiểu 2 năm theo mặc định (configurable)
- Khi storage gần đầy (>80%), cảnh báo admin — không bao giờ tự xóa log

---

### 10.3 Content Guardrails

**Là** Security Officer,  
**Tôi muốn** định nghĩa các quy tắc kiểm soát nội dung được áp dụng tự động vào mọi LLM request/response,  
**để** ngăn chặn nội dung độc hại và vi phạm chính sách doanh nghiệp.

**Tiêu chí chấp nhận:**

- Hỗ trợ 3 loại rule:
  - **Keyword**: chứa từ cụ thể (case-insensitive, wildcard)
  - **Regex**: biểu thức chính quy
  - **AI Classifier**: gọi LLM model phụ để phân loại (NSFW, jailbreak, competitive mention...)
- Mỗi rule có `scope`: `request`, `response`, hoặc `both`
- Mỗi rule có `action`: `block` (trả lỗi), `warn` (log cảnh báo), `redact` (thay thế bằng `[REDACTED]`)
- Rules có thể áp dụng `globally` hoặc `per-provider`
- Mặc định mới: action = `warn` (không phá vỡ traffic khi mới bật)
- Guardrail violation được log riêng với `violation_type`, `matched_rule_id`, `matched_content_snippet`
- UI cung cấp form test rule với input thử nghiệm — hiển thị kết quả ngay lập tức
- Tổng latency thêm vào do guardrails ≤ 5ms cho keyword/regex rules

---

### 10.4 PII Detection & Redaction

**Là** Security Officer,  
**Tôi muốn** cấu hình hệ thống tự động phát hiện và che dấu PII trong request/response trước khi lưu log,  
**để** đảm bảo tuân thủ GDPR, CCPA, và PDPA mà không cần thay đổi ứng dụng client.

**Tiêu chí chấp nhận:**

- Hỗ trợ phát hiện tối thiểu các entity PII sau (out-of-the-box):
  - Email address
  - Số điện thoại (Việt Nam, quốc tế)
  - Số CMND/CCCD/Passport
  - Số thẻ tín dụng (Luhn validation)
  - Họ tên (NER-based, tùy ngôn ngữ)
  - Địa chỉ
  - Ngày sinh
- 3 chế độ redaction per entity type: `mask` (***), `hash` (SHA-256 truncated), `remove`
- Redaction xảy ra **trước** khi plugin logging ghi vào storage — PII không bao giờ persist
- Custom regex rules để thêm entity type theo đặc thù doanh nghiệp (VD: mã nhân viên)
- Cấu hình per-provider: enable/disable PII redaction theo từng provider
- Dashboard hiển thị: tổng số PII events/ngày, top entity types detected
- False positive rate < 2% trên test corpus tiếng Anh + tiếng Việt

---

### 10.5 HashiCorp Vault Integration

**Là** Security Officer,  
**Tôi muốn** Bifrost lấy API keys từ HashiCorp Vault tại runtime thay vì lưu trong database,  
**để** API keys không bao giờ được persist ở bất kỳ storage nào ngoài Vault.

**Tiêu chí chấp nhận:**

- Hỗ trợ 3 phương thức Vault authentication:
  - **Token**: static token (cho dev/test)
  - **AppRole**: role_id + secret_id (cho production)
  - **Kubernetes**: ServiceAccount JWT (cho K8s deployment)
- Cú pháp reference trong provider key config: `vault://secret/path/to/key#field_name`
- Bifrost resolve secret VÀO THỜI ĐIỂM STARTUP; không gọi Vault per-request (performance)
- Dynamic secrets: Bifrost tự renew lease trước khi hết hạn (configurable buffer: 20% of TTL)
- Nếu Vault unreachable khi startup: service KHÔNG khởi động và log lỗi rõ ràng
- Nếu Vault unreachable sau startup (renewal fail): log cảnh báo, tiếp tục dùng cached secret đến hết TTL
- Vault connection config: `address`, `namespace` (Vault Enterprise), `tls_skip_verify`, `ca_cert_path`
- `GET /api/system/vault/status` trả về: `connected`, `last_renewal`, `secrets_count`

---

### 10.6 SSO / SAML / OIDC Integration

**Là** Security Officer,  
**Tôi muốn** tất cả người dùng Bifrost phải đăng nhập qua corporate SSO,  
**để** không có local password nào tồn tại và deprovisioning xảy ra ngay lập tức.

**Tiêu chí chấp nhận:**

- Hỗ trợ OIDC 1.0 với: Google Workspace, Azure AD, Okta, Keycloak
- Hỗ trợ SAML 2.0 SP-initiated flow với: Okta, ADFS, Azure AD, OneLogin
- JIT (Just-in-Time) provisioning: user được tạo tự động khi lần đầu đăng nhập SSO
- Role mapping: IdP group → Bifrost role (configurable trong UI)
- Local password login có thể bị **vô hiệu hóa hoàn toàn** khi SSO được bật
- Button "Login with SSO" là link nổi bật trên login page
- Phiên SSO tôn trọng IdP session timeout — logout từ IdP sẽ invalidate Bifrost session trong < 5 phút
- `GET /api/system/sso/status` trả về: `enabled`, `provider_type`, `last_user_sync`

---

## 4. Thêm Section 11 — Cluster Operator Requirements

*Đây là nhu cầu của ML Platform Lead và DevOps Engineer vận hành Bifrost ở quy mô lớn.*

---

### 11.1 Multi-Node High Availability

**Là** Cluster Operator,  
**Tôi muốn** chạy Bifrost trên 3-5 node với shared state,  
**để** dịch vụ vẫn hoạt động khi có 1-2 node fail.

**Tiêu chí chấp nhận:**

- Cluster mode được kích hoạt bằng `clustering.enabled: true` trong config
- Yêu cầu PostgreSQL (không hỗ trợ SQLite ở cluster mode) — documented rõ
- Yêu cầu Redis cho pub/sub cache invalidation (optional fallback: PostgreSQL LISTEN/NOTIFY)
- Mỗi node đăng ký vào bảng `cluster_nodes` với heartbeat mỗi 10 giây
- Node bị coi là dead sau 30 giây không heartbeat
- Khi Virtual Key hoặc Routing Rule thay đổi trên bất kỳ node nào:
  - Event được publish qua Redis/PG pub/sub
  - Tất cả node invalidate in-memory cache trong < 500ms
- Budget và rate-limit counters sử dụng PostgreSQL atomic operations (không có local cache cho counters)
- `GET /api/cluster/nodes` trả về: danh sách nodes, heartbeat, version, request count/min
- `GET /api/cluster/nodes/{id}/health` trả về: memory, goroutines, queue depths
- UI Cluster page hiển thị topology với node status real-time

---

### 11.2 Adaptive Routing

**Là** Cluster Operator,  
**Tôi muốn** Bifrost tự động điều chỉnh routing dựa trên hiệu suất thực tế của mỗi provider,  
**để** traffic luôn được đến provider khỏe mạnh nhất, kể cả khi tôi không theo dõi liên tục.

**Tiêu chí chấp nhận:**

- Bifrost thu thập metrics per-provider theo sliding window (configurable: 1m/5m/15m):
  - p50/p95/p99 latency
  - Error rate (5xx / timeout)
  - Cost per 1K token
- Adaptive routing engine tính score = `latency_weight * norm_latency + error_weight * error_rate + cost_weight * norm_cost`
- Weights configurable qua `PUT /api/adaptive-routing/config` (mặc định: latency 0.4, error 0.4, cost 0.2)
- Score được tính lại mỗi 30 giây
- Provider bị đánh dấu "degraded" khi error_rate > threshold (mặc định 20%) → ưu tiên thấp nhất
- Adaptive routing chỉ applicable khi routing rule không pin cứng provider
- `GET /api/adaptive-routing/stats` trả về live scores và metrics per-provider
- Routing decisions có thể được log (opt-in, debug mode) để audit
- Latency overhead của score calculation < 1ms per request

---

### 11.3 Alert Channels

**Là** Cluster Operator,  
**Tôi muốn** nhận thông báo chủ động qua Slack và PagerDuty khi có sự kiện quan trọng,  
**để** phản ứng trước khi người dùng bị ảnh hưởng.

**Tiêu chí chấp nhận:**

- Hỗ trợ 4 loại channel: `webhook` (generic), `slack` (native formatting), `pagerduty` (severity-mapped), `email`
- Hỗ trợ 6 loại event:
  - `BUDGET_WARNING`: budget vượt ngưỡng cảnh báo (default 80%, configurable)
  - `BUDGET_EXCEEDED`: budget vượt 100%
  - `RATE_LIMIT_HIT`: rate limit bị trigger
  - `PROVIDER_DEGRADED`: error rate > threshold
  - `GUARDRAIL_VIOLATION`: content rule bị vi phạm
  - `CLUSTER_NODE_DOWN`: node không heartbeat
- Mỗi channel có thể subscribe vào subset của events
- Alert dispatch là async — không block request path
- Alert latency (từ event → notification nhận được): < 60 giây
- Cooldown per-event per-channel: configurable (default 15 phút) — tránh alert storm
- `POST /api/alert-channels/{id}/test` gửi test notification ngay lập tức
- Slack message chứa: event type, resource, current value, threshold, link đến UI

---

### 11.4 Large Payload Handling

**Là** Cluster Operator,  
**Tôi muốn** hệ thống xử lý audio/video files lớn mà không gây OOM,  
**để** teams dùng Whisper transcription và video understanding yên tâm với files > 100MB.

**Tiêu chí chấp nhận:**

- Upload audio/video file > 50MB không bị OOM khi có 10 concurrent uploads
- Body không được load toàn bộ vào RAM — phải stream tới provider
- Configurable `max_body_size` per provider trong NetworkConfig (default: không giới hạn cho enterprise)
- SSE response cho các model sinh response dài không accumulate toàn bộ vào memory trừ khi cần thiết (post-hook)
- Test suite bao gồm: 200MB audio file upload và transcription pipeline end-to-end

---

## 5. Thêm Section 12 — Data & Compliance Requirements

*Đây là nhu cầu của Compliance Team và Data Engineering.*

---

### 12.1 SCIM 2.0 Provisioning

**Là** IdP Administrator,  
**Tôi muốn** Okta/Azure AD tự động tạo, cập nhật, và vô hiệu hóa tài khoản Bifrost,  
**để** không có tài khoản orphan nào tồn tại sau khi nhân viên rời công ty.

**Tiêu chí chấp nhận:**

- Bifrost expose SCIM 2.0 endpoints:
  - `GET/POST /scim/v2/Users` — list và create users
  - `GET/PUT/PATCH/DELETE /scim/v2/Users/{id}` — manage individual users
  - `GET/POST /scim/v2/Groups` — sync IdP groups → Bifrost User Groups
- SCIM bearer token được generate trong UI và revoke-able
- Khi user bị deactivate trong Okta → Bifrost session bị invalidate, API key bị suspended trong < 30 giây
- SCIM group sync: IdP group "bifrost-admin" → role Admin; "bifrost-viewer" → role Viewer (configurable mapping)
- `GET /api/system/scim/status` trả về: `enabled`, `last_sync`, `users_synced_count`
- Sync log hiển thị lịch sử provisioning events

---

### 12.2 Data Connectors

**Là** Data Engineering Lead,  
**Tôi muốn** stream inference logs tự động sang BigQuery và Datadog,  
**để** SIEM và BI platform có dữ liệu AI usage cho phân tích bảo mật và kinh doanh.

**Tiêu chí chấp nhận:**

- Hỗ trợ 4 connector types:
  - **BigQuery**: stream vào table được cấu hình, schema tự động tạo, partition by date
  - **Datadog Logs**: ship qua Datadog Logs Intake API với custom tags
  - **S3 / GCS**: batch export mỗi N phút, Parquet hoặc JSONL format
  - **Elasticsearch**: index inference logs với tên configurable
- Credential lưu mã hóa trong database (AES-256-GCM, giống API keys)
- Mỗi connector có `sync_interval` (1-60 phút), `enabled` toggle, và `filter_expr` (CEL expression để filter records trước khi export)
- Connector status hiển thị: `last_sync`, `records_exported`, `errors_count`, `lag_seconds`
- `POST /api/data-connectors/{id}/test` validate credentials và ghi 1 test record
- Export không block request path — async batch processor
- Nếu connector fail, retry với exponential backoff (max 4 giờ), alert qua alert channel nếu config

---

### 12.3 Compliance Report Export

**Là** Compliance Auditor,  
**Tôi muốn** export báo cáo tuân thủ từ Bifrost theo định kỳ,  
**để** cung cấp bằng chứng cho kiểm toán viên mà không cần truy cập trực tiếp vào server.

**Tiêu chí chấp nhận:**

- `GET /api/audit-logs/export?format=csv&from=&to=` export audit logs ra CSV
- `GET /api/reports/pii-summary?period=30d` trả về tổng hợp PII events
- `GET /api/reports/guardrail-violations?period=30d` trả về tổng hợp guardrail violations
- Báo cáo chứa đủ thông tin cho SOC 2 Type II evidence collection
- Báo cáo được ký số (optional) để đảm bảo tính toàn vẹn

---

### 12.4 MCP Tool Groups

**Là** Platform Admin với 50+ MCP clients,  
**Tôi muốn** nhóm MCP tools thành tập có thể tái sử dụng,  
**để** tránh phải cấu hình lại danh sách tools mỗi lần tạo Virtual Key mới.

**Tiêu chí chấp nhận:**

- Tạo "Tool Group" với tên, mô tả, và danh sách tool names từ một MCP client
- Gán Tool Group cho Virtual Key thay vì liệt kê individual tools
- Thay đổi Tool Group (thêm/bớt tool) áp dụng ngay cho tất cả VK đang dùng group đó
- `GET /api/mcp/tool-groups` trả về danh sách groups và số VK đang dùng mỗi group
- Tool Group xóa được chặn nếu vẫn còn VK đang dùng

---

### 12.5 User Groups

**Là** IdP Administrator,  
**Tôi muốn** nhóm người dùng và gán role hàng loạt,  
**để** onboard một team mới (10-20 người) chỉ mất vài phút thay vì cấu hình từng người.

**Tiêu chí chấp nhận:**

- Tạo User Group với tên và danh sách role assignments
- Thêm users vào group → họ inherit tất cả roles của group
- Sync từ SCIM: IdP group → Bifrost User Group (nếu tên match)
- User thuộc nhiều group thì được union của tất cả roles
- Xóa user khỏi group → revoke roles inherited từ group đó (effective permissions được recompute ngay)
- UI hiển thị effective permissions của từng user (merge từ direct roles + group roles)

---

## 6. Cập Nhật Section 4 — Platform Administrator Requirements

### 6.1 Bổ Sung 4.7 — License Management

**Là** Platform Admin,  
**Tôi muốn** quản lý license Bifrost Enterprise từ UI,  
**để** biết khi nào license sắp hết hạn và kích hoạt tính năng mới khi nâng cấp.

**Tiêu chí chấp nhận:**

- `GET /api/system/license` trả về: `issued_to`, `features[]`, `expires_at`, `node_limit`, `days_remaining`
- UI hiển thị badge license status ở sidebar: `Licensed`, `Expiring Soon` (< 30 ngày), `Expired`
- Warning banner xuất hiện khi license còn < 30 ngày
- Grace period: license hết hạn → enterprise features vẫn chạy thêm 7 ngày với warning
- Sau grace period: enterprise features trả 403 với message "License expired. Contact sales."
- License key được input qua config file hoặc environment variable `BIFROST_LICENSE_KEY` — không lưu DB

### 6.2 Cập Nhật 4.1 — Provider Configuration (Vault Reference)

**Bổ sung Acceptance Criteria:**
- API key có thể được input dưới dạng `vault://secret/path#field` — Bifrost resolve từ Vault tại startup
- UI hiển thị `[Vault Secret]` thay vì masked value khi key đến từ Vault
- Provider config form có toggle "Use Vault Secret" kèm field nhập Vault path

---

## 7. Cập Nhật Section 9 — Cross-Role Requirements

### 7.1 Bổ Sung 9.6 — Session Security (Enterprise)

**Là** bất kỳ người dùng enterprise,  
**Tôi muốn** phiên làm việc của tôi an toàn và tuân theo chính sách bảo mật của tổ chức,  
**để** tài khoản không bị chiếm quyền nếu tôi quên đăng xuất.

**Tiêu chí chấp nhận:**

- Session timeout configurable (default: 8 giờ, max: 30 ngày)
- Concurrent session limit per user configurable (default: 5)
- Session invalidated ngay khi admin revoke hoặc user bị xóa
- SSO session: Bifrost session không tồn tại lâu hơn IdP session
- `/api/auth/sessions` (admin only): list active sessions, revoke any session

### 7.2 Bổ Sung 9.7 — Enterprise Feature Transparency

**Là** bất kỳ người dùng trên enterprise license,  
**Tôi muốn** biết rõ tính năng nào đang được license và tính năng nào không,  
**để** không bị bất ngờ khi một tính năng ngừng hoạt động.

**Tiêu chí chấp nhận:**

- `GET /api/system/license` accessible bởi tất cả authenticated users (không chỉ Admin)
- UI sidebar hiển thị badge "Enterprise" bên cạnh các tính năng enterprise
- Sidebar badge đổi sang "⚠ Expiring" khi còn < 30 ngày
- Tính năng không được license hiển thị upgrade prompt (giống OSS) thay vì trả 500

---

## 8. Bổ Sung Acceptance Criteria Framework (Toàn Tài Liệu)

### 8.1 Performance SLA cho Enterprise Features

| Feature | Latency Added | Throughput Impact |
|---------|-------------|------------------|
| RBAC middleware check | < 1ms | < 0.1% |
| Guardrails (keyword/regex) | < 5ms | < 0.5% |
| Guardrails (AI classifier) | < 500ms (async) | Không block |
| PII Redactor | < 10ms | < 1% |
| Audit log write | < 2ms (async) | Không block request |
| Adaptive routing score | < 1ms | < 0.1% |
| Cache invalidation cross-node | < 500ms | Không block |

### 8.2 Scalability Targets

| Metric | Target |
|--------|--------|
| Max concurrent users quản lý Bifrost UI | 500 |
| Audit log records per day | 10 triệu |
| SCIM user sync throughput | 1,000 users/phút |
| Alert channels per installation | 50 |
| Data connector export throughput | 100,000 records/phút |
| RBAC enforcement TPS | Không giới hạn (cached) |

---

*CR này cần được review bởi: CTO, CISO, Head of Engineering, Head of ML Platform, IT Identity & Access Management team, Legal & Compliance.*
