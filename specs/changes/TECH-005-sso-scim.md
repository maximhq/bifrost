# TECH-005 — SSO / SCIM 2.0 Identity Integration

**Feature ID:** SSO / SCIM  
**SRS Reference:** §3.16 (SSO-01→08 + SCIM-01→05)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement enterprise identity federation via:
- **SSO**: OIDC 1.0 (Google, Microsoft Entra, Okta, generic) + SAML 2.0 SP flow
- **SCIM 2.0**: Automated user provisioning/deprovisioning from the IdP

The existing session system (`SessionsTable`, `AuthMiddleware`) is extended rather than replaced.

---

## 2. Architecture Mapping

```
transports/bifrost-http/
├── handlers/
│   ├── sso.go            (NEW) OIDC + SAML HTTP handlers
│   └── scim.go           (NEW) SCIM 2.0 endpoint handlers
├── lib/
│   └── middleware.go     (MODIFY) AuthMiddleware accepts SSO tokens

framework/
├── oauth2/               (EXISTING — extend for OIDC)
│   ├── oidc.go           (NEW) OIDC discovery, code exchange, token validation
│   └── saml.go           (NEW) SAML SP metadata, ACS handler
├── configstore/
│   └── tables/
│       └── identity.go   (NEW) SSOConfigTable, SCIMTokenTable, ExternalUserTable
└── scim/                 (NEW) SCIM user/group sync logic
    ├── handler.go
    ├── provisioner.go
    └── types.go
```

---

## 3. Database Schema

```go
// framework/configstore/tables/identity.go

type SSOConfigTable struct {
    ID           string    `gorm:"primaryKey;type:text"`
    Protocol     string    `gorm:"not null"`  // "oidc" | "saml"
    Enabled      bool      `gorm:"default:false"`
    
    // OIDC fields
    OIDCIssuerURL     string
    OIDCClientID      string
    OIDCClientSecret  string  // encrypted at rest
    OIDCScopes        string  // JSON array e.g. ["openid","profile","email","groups"]
    OIDCRedirectURI   string  // https://bifrost.example.com/api/sso/callback
    
    // SAML fields
    SAMLEntityID      string
    SAMLIdPMetadataURL string
    SAMLIdPMetadata   string  `gorm:"type:text"`  // cached metadata XML
    SAMLACSPath       string  // "/api/sso/saml/acs"
    SAMLPrivateKey    string  `gorm:"type:text"`  // encrypted
    SAMLCertificate   string  `gorm:"type:text"`
    
    // Attribute mapping
    AttributeEmail    string  `gorm:"default:'email'"`
    AttributeName     string  `gorm:"default:'name'"`
    AttributeGroups   string  `gorm:"default:'groups'"`  // claim name for group membership
    
    // Group → Role mapping  (JSON: {"Engineering": "operator", "Platform": "admin"})
    GroupRoleMapping  string  `gorm:"type:text"`
    DefaultRole       string  `gorm:"default:'viewer'"`
    
    CreatedAt time.Time
    UpdatedAt time.Time
}

type ExternalUserTable struct {
    ID           string    `gorm:"primaryKey;type:text"`
    ExternalID   string    `gorm:"uniqueIndex"`  // subject from IdP
    Email        string    `gorm:"index;not null"`
    DisplayName  string
    Groups       string    `gorm:"type:text"`  // JSON array
    AssignedRole string
    LastLoginAt  *time.Time
    ProvisionedVia string  // "oidc" | "saml" | "scim"
    Active       bool      `gorm:"default:true"`
    CreatedAt    time.Time
    UpdatedAt    time.Time
}

type SCIMTokenTable struct {
    ID        string    `gorm:"primaryKey;type:text"`
    Token     string    `gorm:"uniqueIndex;not null"`  // Bearer token for SCIM calls
    Name      string
    ExpiresAt *time.Time
    CreatedAt time.Time
}
```

---

## 4. OIDC Flow

```
 Browser                  Bifrost                      IdP (e.g. Okta)
    │                        │                              │
    │ GET /api/sso/login      │                              │
    │ ────────────────────►   │                              │
    │                         │  Build authorization URL     │
    │                         │  (state + nonce → session)  │
    │◄──── 302 Redirect ──────│                              │
    │                                                        │
    │ GET /authorize?client_id=...&redirect_uri=...          │
    │ ──────────────────────────────────────────────────►    │
    │                                                        │
    │◄── 302 redirect to /api/sso/callback?code=...&state= ──│
    │                        │                              │
    │ GET /api/sso/callback   │                              │
    │ ────────────────────►   │                              │
    │                         │ POST /token (code→tokens)   │
    │                         │ ──────────────────────────► │
    │                         │◄─── {id_token, access_token}│
    │                         │                              │
    │                         │ Validate JWT, extract claims │
    │                         │ Map groups → role            │
    │                         │ Upsert ExternalUserTable     │
    │                         │ Create/refresh SessionsTable │
    │◄── Set-Cookie: bifrost_session + 302 → /dashboard ────│
```

```go
// transports/bifrost-http/handlers/sso.go

// GET /api/sso/login?protocol=oidc|saml
func (h *SSOHandler) InitiateLogin(ctx *fasthttp.RequestCtx)

// GET /api/sso/callback  (OIDC code exchange)
func (h *SSOHandler) OIDCCallback(ctx *fasthttp.RequestCtx) {
    code  := string(ctx.QueryArgs().Peek("code"))
    state := string(ctx.QueryArgs().Peek("state"))
    
    // Validate CSRF state
    if !h.validateState(state) {
        ctx.SetStatusCode(400); return
    }
    
    // Exchange code for tokens
    tokens, err := h.oidcClient.ExchangeCode(ctx, code)
    if err != nil { ... }
    
    // Validate ID token: signature, iss, aud, exp, nonce
    claims, err := h.oidcClient.ValidateIDToken(tokens.IDToken)
    if err != nil { ... }
    
    // Map claims → user
    user := h.mapClaims(claims)
    role := h.resolveRole(user.Groups)
    
    // Upsert external user
    extUser, _ := h.configStore.UpsertExternalUser(user)
    
    // Create Bifrost session
    session := &tables.SessionsTable{
        Token:    uuid.New().String(),
        UserID:   extUser.ID,
        Username: extUser.DisplayName,
        Role:     role,
        ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
    }
    h.configStore.CreateSession(session)
    
    // Set cookie + redirect
    ctx.Response.Header.SetCookie(buildSessionCookie(session.Token))
    ctx.Redirect("/", 302)
}

// POST /api/sso/saml/acs  (SAML Assertion Consumer Service)
func (h *SSOHandler) SAMLAssertionConsumer(ctx *fasthttp.RequestCtx)

// GET /api/sso/saml/metadata  (SP metadata XML for IdP registration)
func (h *SSOHandler) SAMLMetadata(ctx *fasthttp.RequestCtx)

// POST /api/sso/logout
func (h *SSOHandler) Logout(ctx *fasthttp.RequestCtx)
```

---

## 5. OIDC Client

```go
// framework/oauth2/oidc.go

type OIDCClient struct {
    config      SSOConfigTable
    httpClient  *http.Client  // net/http for OAuth flows
    discovery   *OIDCDiscovery  // cached from /.well-known/openid-configuration
    keySet      *KeySet         // JWKS — cached, refreshed on key miss
    mu          sync.RWMutex
}

type OIDCDiscovery struct {
    Issuer                string   `json:"issuer"`
    AuthorizationEndpoint string   `json:"authorization_endpoint"`
    TokenEndpoint         string   `json:"token_endpoint"`
    JWKsURI              string   `json:"jwks_uri"`
    UserinfoEndpoint      string   `json:"userinfo_endpoint"`
}

func (c *OIDCClient) ValidateIDToken(rawToken string) (*Claims, error) {
    // Parse header → get kid
    // Fetch key from JWKS (cache hit or HTTP fetch)
    // Verify signature
    // Validate: iss, aud, exp, nbf, nonce
    // Return standard claims + extra claims (groups, email)
}
```

---

## 6. SAML SP Implementation

```go
// framework/oauth2/saml.go
// Using: github.com/crewjam/saml  (MIT license)

type SAMLProvider struct {
    sp     *saml.ServiceProvider
    config SSOConfigTable
}

func NewSAMLProvider(config SSOConfigTable) (*SAMLProvider, error) {
    // Load IdP metadata from URL or cached XML
    // Create SP with private key + certificate
    // Configure ACS URL, entity ID
}

func (p *SAMLProvider) InitiateLogin(w http.ResponseWriter, r *http.Request) {
    // Create AuthnRequest, sign, redirect to IdP SSO URL
}

func (p *SAMLProvider) ProcessACS(r *http.Request) (*saml.Assertion, error) {
    // Parse SAMLResponse, verify signature, validate conditions
    // Return assertion with NameID + attribute statements
}
```

---

## 7. SCIM 2.0 Endpoints

SCIM 2.0 (RFC 7644) allows the IdP to push user/group changes to Bifrost.

```go
// transports/bifrost-http/handlers/scim.go

// Authentication: Bearer token from SCIMTokenTable
// All SCIM endpoints require SCIM bearer token (separate from session auth)

// Users resource
// GET    /scim/v2/Users              List users (filter, count, startIndex)
// POST   /scim/v2/Users              Provision new user
// GET    /scim/v2/Users/{id}         Get user
// PUT    /scim/v2/Users/{id}         Replace user
// PATCH  /scim/v2/Users/{id}         Update user (operations)
// DELETE /scim/v2/Users/{id}         Deprovision user (set Active=false)

// Groups resource
// GET    /scim/v2/Groups             List groups
// POST   /scim/v2/Groups             Create group
// GET    /scim/v2/Groups/{id}        Get group
// PUT    /scim/v2/Groups/{id}        Replace group + membership
// PATCH  /scim/v2/Groups/{id}        Update group membership
// DELETE /scim/v2/Groups/{id}        Delete group

// ServiceProviderConfig
// GET    /scim/v2/ServiceProviderConfig
// GET    /scim/v2/Schemas
// GET    /scim/v2/ResourceTypes
```

### 7.1 SCIM User Provisioning

```go
// framework/scim/provisioner.go

func (p *SCIMProvisioner) ProvisionUser(scimUser SCIMUser) (*ExternalUserTable, error) {
    user := &tables.ExternalUserTable{
        ID:              uuid.New().String(),
        ExternalID:      scimUser.ExternalID,
        Email:           scimUser.Emails[0].Value,
        DisplayName:     scimUser.DisplayName,
        Active:          scimUser.Active,
        ProvisionedVia:  "scim",
    }
    // Map group membership → role
    user.AssignedRole = p.resolveRoleFromGroups(scimUser.Groups)
    return p.configStore.UpsertExternalUser(user)
}

func (p *SCIMProvisioner) DeprovisionUser(externalID string) error {
    // Set Active=false — do NOT delete (audit trail)
    // Invalidate all active sessions for this user
    return p.configStore.DeactivateExternalUser(externalID)
}
```

---

## 8. Group → Role Mapping

```go
// Stored in SSOConfigTable.GroupRoleMapping as JSON
// Example: {"platform-team": "admin", "ml-team": "operator", "default": "viewer"}

func (h *SSOHandler) resolveRole(groups []string) string {
    var mapping map[string]string
    json.Unmarshal([]byte(h.ssoConfig.GroupRoleMapping), &mapping)
    
    // Role precedence: higher privilege wins
    priority := map[string]int{
        "super_admin": 5, "admin": 4, "operator": 3,
        "viewer": 2, "api_user": 1,
    }
    
    best := h.ssoConfig.DefaultRole
    for _, group := range groups {
        if role, ok := mapping[group]; ok {
            if priority[role] > priority[best] {
                best = role
            }
        }
    }
    return best
}
```

---

## 9. SSO Config API

```go
// GET    /api/sso/config          — get SSO config (secrets masked)
// PUT    /api/sso/config          — update SSO config (super_admin only)
// POST   /api/sso/config/test     — test OIDC discovery URL reachability
// GET    /api/scim/tokens         — list SCIM bearer tokens
// POST   /api/scim/tokens         — create new SCIM token (super_admin only)
// DELETE /api/scim/tokens/{id}    — revoke SCIM token
```

---

## 10. AuthMiddleware Extension

```go
// transports/bifrost-http/lib/middleware.go

func AuthMiddleware(h fasthttp.RequestHandler) fasthttp.RequestHandler {
    return func(ctx *fasthttp.RequestCtx) {
        token := extractToken(ctx)
        
        // Existing session lookup
        session, err := configStore.FindSessionByToken(token)
        if err != nil {
            // SCIM token check (separate auth path)
            if isSCIMPath(ctx) {
                if validateSCIMToken(token) {
                    h(ctx); return
                }
            }
            ctx.SetStatusCode(401); return
        }
        
        // Attach role from session (may be SSO-derived)
        ctx.SetUserValue("session_role", session.Role)
        ctx.SetUserValue("session_user_id", session.UserID)
        h(ctx)
    }
}
```

---

## 11. UI Components

```
ui/app/enterprise/sso/
├── page.tsx                  — SSO configuration (OIDC/SAML setup wizard)
├── scim/page.tsx             — SCIM token management
└── components/
    ├── OIDCConfigForm.tsx    — OIDC discovery URL, client ID/secret, scopes
    ├── SAMLConfigForm.tsx    — SP metadata download, IdP metadata upload
    ├── GroupMappingEditor.tsx — Group → Role mapping table
    └── SCIMTokenManager.tsx  — Create/revoke SCIM bearer tokens
```

---

## 12. Security Requirements

- OIDC state parameter: cryptographically random 32 bytes, stored in server-side session with 10-min TTL
- SAML assertions: validate `NotBefore` and `NotOnOrAfter` conditions, signature on response AND assertion
- SCIM tokens: stored as bcrypt hash in `SCIMTokenTable` — only shown once on creation
- Client secrets: encrypted at rest via `framework/encrypt` AES-256-GCM
- All SSO config changes are audit-logged (TECH-002)

---

## 13. Dependencies

```
# framework/oauth2/go.mod additions:
github.com/crewjam/saml v0.4.x          # SAML SP implementation
github.com/golang-jwt/jwt/v5 v5.x.x     # JWT validation
golang.org/x/oauth2 v0.x.x              # OIDC code exchange
github.com/zitadel/oidc/v3 v3.x.x       # OIDC discovery + JWKS
```
