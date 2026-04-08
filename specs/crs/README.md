# Change Request Index
## Bifrost Enterprise Edition — v2.0

**Author:** Product Owner (Enterprise)  
**Date:** 2026-04-08  
**Status:** Proposed  

---

## Danh Sách Change Requests

| CR ID | Tài Liệu Ảnh Hưởng | Tiêu Đề | Trạng Thái |
|-------|-------------------|---------|-----------|
| [CR-ENT-001](CR-ENT-001-PRD-enterprise-edition.md) | PRD.md | Enterprise Edition — PRD Revision | 🟡 Proposed |
| [CR-ENT-002](CR-ENT-002-URD-enterprise-edition.md) | URD.md | Enterprise Edition — URD Revision | 🟡 Proposed |

---

## Tóm Tắt Phạm Vi

Hai CR này được đề xuất bởi Product Owner để đưa Bifrost trở thành giải pháp AI Gateway đầy đủ cho **tổ chức 10,000+ nhân viên**, dựa trên gap analysis trong [`enterprise-missing-features.md`](../bugs/enterprise-missing-features.md).

### CR-ENT-001 (PRD)
- Thêm 2 Persona mới: Security Officer (E) và ML Platform Lead Large Scale (F)
- Cập nhật toàn bộ danh sách P2 Enterprise features với mô tả chi tiết
- Thêm P3 backlog: Showback Dashboard, Compliance Reports, Anomaly Detection
- Cập nhật Feature Matrix với 14 enterprise features mới
- Thêm Enterprise success metrics
- Cập nhật Release Criteria 10.2 với 30+ acceptance criteria mới

### CR-ENT-002 (URD)
- Thêm 3 User Class mới: Security Officer, Compliance Auditor, IdP Administrator
- **Section 10 (mới):** 6 user stories từ góc nhìn Security Officer (RBAC, Audit Log, Guardrails, PII, Vault, SSO)
- **Section 11 (mới):** 4 user stories từ góc nhìn Cluster Operator (HA Clustering, Adaptive Routing, Alert Channels, Large Payload)
- **Section 12 (mới):** 5 user stories từ góc nhìn Data/Compliance (SCIM, Data Connectors, Reports, MCP Groups, User Groups)
- Cập nhật Section 4 (Admin): License Management, Vault provider config
- Cập nhật Section 9 (Cross-role): Session Security, Enterprise Feature Transparency
- Bổ sung Performance SLA và Scalability Targets

---

## Features Được Đề Xuất Theo Nhóm

### Nhóm 1 — Security & Compliance (CISO-driven)
| Feature | CR | Section |
|---------|----|---------| 
| RBAC (5 roles) | CR-001 §5.3, CR-002 §10.1 | P2 |
| Audit Logs (immutable) | CR-001 §5.3, CR-002 §10.2 | P2 |
| Guardrails (keyword/regex/AI) | CR-001 §5.3, CR-002 §10.3 | P2 |
| PII Redactor | CR-001 §5.3, CR-002 §10.4 | P2 |
| HashiCorp Vault | CR-001 §5.3, CR-002 §10.5 | P2 |
| SSO / SAML / OIDC | CR-001 §5.3, CR-002 §10.6 | P2 |

### Nhóm 2 — Operations & Scale (Platform Lead-driven)
| Feature | CR | Section |
|---------|----|---------| 
| Multi-node Clustering | CR-001 §5.3, CR-002 §11.1 | P2 |
| Adaptive Routing | CR-001 §5.3, CR-002 §11.2 | P2 |
| Alert Channels | CR-001 §5.3, CR-002 §11.3 | P2 |
| Large Payload Optimization | CR-001 §5.3, CR-002 §11.4 | P2 |

### Nhóm 3 — Data & Governance (Data/Compliance-driven)
| Feature | CR | Section |
|---------|----|---------| 
| SCIM 2.0 Provisioning | CR-001 §5.3, CR-002 §12.1 | P2 |
| Data Connectors (BQ/DD/S3) | CR-001 §5.3, CR-002 §12.2 | P2 |
| Compliance Report Export | CR-001 §5.4, CR-002 §12.3 | P3 |
| MCP Tool Groups | CR-001 §6.2, CR-002 §12.4 | Matrix |
| User Groups | CR-001 §6.2, CR-002 §12.5 | Matrix |

### Nhóm 4 — Platform Infrastructure (Foundation)
| Feature | CR | Section |
|---------|----|---------| 
| License Enforcement | CR-001 §5.3, CR-002 §6.1 | Required |

---

## Suggested Review Process

```
Product Owner (đề xuất)
    ↓
CTO Review (kiến trúc, build vs buy)
    ↓
CISO Review (security model, compliance gaps)
    ↓
Head of ML Platform (operational fit)
    ↓
Legal Review (GDPR, PDPA clauses)
    ↓
Estimate & Prioritization Planning
    ↓
Sprint Planning (S1 kickoff)
```

**Deadline đề xuất cho review:** 2 tuần từ ngày tạo CR  
**Target pilot:** 3 internal teams sau S1-S6 (RBAC + SCIM + Audit Logs + PII)
