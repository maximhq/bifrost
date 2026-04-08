# TASK-005 — SSO / SCIM

**Feature:** SSO (OIDC + SAML) and SCIM 2.0 Provisioning  
**TECH Spec:** [TECH-005-sso-scim.md](../TECH-005-sso-scim.md)  
**Phase:** 1 (Security Core)  
**Depends on:** TASK-014 (license), TASK-001 (RBAC — role assignment for SSO users)  
**Estimate:** 7 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

SSO enables enterprise users to authenticate via their corporate identity provider.  
SCIM automates user/group provisioning from the IdP (Okta, Azure AD, etc.).  
Both features are gated by `sso_oidc`, `sso_saml`, and `scim` license features.

---

## Tasks

### TASK-005-01 — Database schema + GORM migration

**Files to create:**
- `framework/configstore/tables/sso.go` — `SSOProviderTable`, `ExternalUserTable`, `SSOSessionTable`
- Migration file

**Schema:**
```go
type SSOProviderTable struct {
    ID              string    `gorm:"primaryKey;type:text"`
    Name            string    `gorm:"uniqueIndex;not null"`
    Type            string    `gorm:"index"` // "oidc"|"saml"
    Enabled         bool      `gorm:"default:true"`
    // OIDC fields
    IssuerURL       string
    ClientID        string
    ClientSecret    string  // encrypted at rest
    Scopes          string  // JSON array: ["openid","email","profile"]
    // SAML fields
    EntityID        string
    SSOURL          string
    Certificate     string `gorm:"type:text"` // IdP certificate PEM
    // Role mapping
    DefaultRole     string  // default RBAC role for new SSO users
    RoleMappingJSON string  `gorm:"type:text"` // JSON: {group_name → role}
    // SCIM
    SCIMEnabled     bool
    SCIMToken       string  // bearer token for SCIM requests (hashed)
    CreatedAt       time.Time
    UpdatedAt       time.Time
}

type ExternalUserTable struct {
    ID          string    `gorm:"primaryKey;type:text"`
    ExternalID  string    `gorm:"uniqueIndex;not null"` // IdP subject
    ProviderID  string    `gorm:"index;not null"`
    Email       string    `gorm:"index;not null"`
    DisplayName string
    Active      bool      `gorm:"default:true"`
    LastLoginAt *time.Time
    SCIMVersion int64     // SCIM etag
    CreatedAt   time.Time
    UpdatedAt   time.Time
}

type SSOSessionTable struct {
    ID          string    `gorm:"primaryKey;type:text"` // session token
    UserID      string    `gorm:"index;not null"`
    ProviderID  string    `gorm:"index"`
    ExpiresAt   time.Time `gorm:"index"`
    IP          string
    UserAgent   string
    CreatedAt   time.Time
}
```

**Acceptance criteria:**
- [ ] Migration runs cleanly; idempotent
- [ ] `ClientSecret` and `SCIMToken` stored encrypted

---

### TASK-005-02 — OIDC authentication flow

**Files to create:**
- `framework/sso/oidc.go` — `OIDCProvider`: discovery, authorization URL, token exchange, userinfo
- `transports/bifrost-http/handlers/sso.go` — `SSOHandler`

**Flow:**
```
GET /auth/sso/{provider_id}/login
  → Redirect to IdP authorization URL with state + PKCE

GET /auth/sso/{provider_id}/callback?code=&state=
  → Exchange code for tokens
  → Fetch userinfo
  → Upsert ExternalUserTable
  → Assign default role if new user
  → Create SSOSessionTable entry
  → Set session cookie
  → Redirect to UI

GET /auth/sso/logout
  → Invalidate session
  → Redirect to IdP logout (if configured)
```

**Acceptance criteria:**
- [ ] PKCE (S256) implemented for authorization code flow
- [ ] `state` parameter validated (CSRF protection)
- [ ] New users get `DefaultRole` assigned (audit-logged)
- [ ] Existing users: email updated from userinfo on each login
- [ ] Session cookie: `HttpOnly`, `SameSite=Lax`, `Secure` (in production)
- [ ] Session expiry from IdP token TTL or config max (whichever is less)

---

### TASK-005-03 — SAML 2.0 authentication flow

**Files to create:**
- `framework/sso/saml.go` — `SAMLProvider`: metadata parsing, AuthnRequest generation, assertion validation

**Flow:**
```
GET /auth/saml/{provider_id}/login
  → Generate AuthnRequest (signed with Bifrost SP private key)
  → POST binding redirect to IdP

POST /auth/saml/{provider_id}/acs
  → Receive SAMLResponse
  → Validate signature against IdP certificate
  → Parse NameID and attributes
  → Upsert ExternalUserTable
  → Create SSOSessionTable entry
  → Set session cookie
  → Redirect to UI

GET /auth/saml/{provider_id}/metadata
  → Return SP metadata XML (for IdP configuration)
```

**Acceptance criteria:**
- [ ] SP metadata endpoint returns valid XML with `AssertionConsumerService` URL
- [ ] AuthnRequest signed with configured SP private key
- [ ] Assertion signature validated against stored IdP certificate
- [ ] Replay attack protection via `InResponseTo` + assertion ID cache (5-minute window)

---

### TASK-005-04 — SCIM 2.0 user provisioning

**Files to create:**
- `framework/scim/provisioner.go` — `SCIMProvisioner`
- `transports/bifrost-http/handlers/scim.go` — SCIM 2.0 endpoint handler

**Endpoints (SCIM 2.0 spec):**
```
GET    /scim/v2/Users              — list users (filter supported)
POST   /scim/v2/Users              — create user
GET    /scim/v2/Users/{id}         — get user
PUT    /scim/v2/Users/{id}         — full replace
PATCH  /scim/v2/Users/{id}         — partial update
DELETE /scim/v2/Users/{id}         — deprovision (soft-delete: active=false)

GET    /scim/v2/Groups             — list groups
POST   /scim/v2/Groups             — create group (maps to UserGroupTable)
GET    /scim/v2/Groups/{id}        — get group
PUT    /scim/v2/Groups/{id}        — update group + members
DELETE /scim/v2/Groups/{id}        — deprovision group

GET    /scim/v2/ServiceProviderConfig
GET    /scim/v2/ResourceTypes
GET    /scim/v2/Schemas
```

**Acceptance criteria:**
- [ ] SCIM bearer token authentication (checked against hashed `SCIMToken`)
- [ ] Filter support: `filter=userName eq "foo@example.com"`
- [ ] PATCH operations support `add`, `remove`, `replace` operations
- [ ] User deprovision (DELETE) sets `active=false` in `ExternalUserTable` (never hard deletes)
- [ ] Group sync creates/updates `UserGroupTable` entries (TECH-012 integration)
- [ ] SCIM requests are audit-logged

---

### TASK-005-05 — SSO provider management API

**Files to create:**
- `transports/bifrost-http/handlers/sso_mgmt.go`

**Endpoints:**
```
GET    /api/sso/providers           — list SSO providers (admin+)
POST   /api/sso/providers           — create provider (super_admin)
GET    /api/sso/providers/{id}      — get provider (secrets masked)
PUT    /api/sso/providers/{id}      — update provider (super_admin)
DELETE /api/sso/providers/{id}      — delete provider (super_admin)
POST   /api/sso/providers/{id}/test — test OIDC discovery / SAML metadata fetch

GET    /api/sso/users               — list external users (admin+)
GET    /api/sso/users/{id}          — get user detail
POST   /api/sso/users/{id}/deactivate  — deactivate user (admin+)
POST   /api/sso/users/{id}/activate    — reactivate user (admin+)
```

**Acceptance criteria:**
- [ ] `ClientSecret` and certificates never returned in GET responses
- [ ] Provider type (`oidc`/`saml`) returns `402` if corresponding license feature disabled
- [ ] OIDC discovery test validates `/.well-known/openid-configuration` is reachable

---

### TASK-005-06 — UI: SSO configuration

**Files to create:**
- `ui/app/enterprise/sso/page.tsx` — provider list
- `ui/app/enterprise/sso/new/page.tsx` — new provider wizard
- `ui/app/enterprise/sso/[id]/page.tsx` — provider detail + test
- `ui/app/enterprise/sso/components/OIDCConfigForm.tsx`
- `ui/app/enterprise/sso/components/SAMLConfigForm.tsx`
- `ui/app/enterprise/sso/components/SCIMSetup.tsx` — token generation + endpoint URL
- `ui/app/enterprise/sso/components/UserList.tsx`

**Acceptance criteria:**
- [ ] Provider type selector (OIDC / SAML) shows correct form fields
- [ ] SCIM setup shows the SCIM endpoint URL and "Generate Token" button
- [ ] "Test Connection" button calls test endpoint and shows result inline
- [ ] All pages inside `<EnterpriseGate feature="sso_oidc">` or `"sso_saml"`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Integration test: OIDC login flow with mock IdP (using `go-oidc` test server)
- [ ] Integration test: SCIM `POST /scim/v2/Users` creates `ExternalUserTable` entry and assigns default role
- [ ] Integration test: SCIM `DELETE /scim/v2/Users/{id}` sets `active=false`
- [ ] Security test: tampered SAML assertion rejected (signature validation)
- [ ] Security test: CSRF attack on OIDC callback rejected (state mismatch)
- [ ] `make build` passes
