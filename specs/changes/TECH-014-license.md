# TECH-014 — License Enforcement

**Feature ID:** LIC  
**SRS Reference:** §3.25 (LIC-01 → LIC-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Implement a JWT-based license validation system that gates enterprise feature availability. License keys are read-only from environment/config (never stored in DB) and validated locally for offline operation.

**License tiers:**
| Tier | Features |
|------|---------|
| `community` | All OSS features (current default) |
| `pro` | Guardrails, PII Redactor, Alert Channels, up to 5 nodes |
| `enterprise` | All features, unlimited nodes, Vault, SSO/SCIM, RBAC, Data Connectors |
| `enterprise_trial` | Enterprise features, 30-day time limit |

---

## 2. Architecture Mapping

```
framework/
├── license/                    (NEW package)
│   ├── license.go              License struct, parser, validator
│   ├── jwt.go                  JWT signature verification (RS256)
│   ├── features.go             Feature flag map per tier
│   └── enforcer.go             LicenseEnforcer — IsFeatureEnabled()

transports/bifrost-http/
├── lib/
│   └── features.go             (NEW) Global IsFeatureEnabled() wrapper
├── handlers/
│   └── license.go              (NEW) /api/license/* endpoints
└── server/server.go            (MODIFY) Initialize license on bootstrap
```

---

## 3. License JWT Format

License keys are compact RS256-signed JWTs issued by Bifrost's licensing server:

```json
// JWT Header
{
  "alg": "RS256",
  "typ": "JWT",
  "kid": "bifrost-license-2026"
}

// JWT Payload
{
  "iss": "https://license.getbifrost.ai",
  "sub": "org_abcdef1234",              // organization ID
  "aud": "bifrost-gateway",
  "iat": 1744000000,
  "exp": 1775536000,                     // expiry (1 year)
  "nbf": 1744000000,
  "jti": "lic_xxxxxxxxxxxxxxxx",         // unique license ID
  
  // License claims
  "tier":    "enterprise",               // "community"|"pro"|"enterprise"|"enterprise_trial"
  "org":     "Acme Corporation",
  "seats":   500,                        // licensed users (-1 = unlimited)
  "nodes":   10,                         // max cluster nodes (-1 = unlimited)
  "features": [                          // explicit feature list
    "rbac", "audit_logs", "guardrails", "pii_redactor",
    "sso_oidc", "sso_saml", "scim", "adaptive_routing",
    "clustering", "alerts", "vault", "large_payload",
    "mcp_tool_groups", "user_groups", "data_connectors"
  ],
  "domain":  "*.acme.com",               // domain restriction (optional)
  "trial":   false
}
```

---

## 4. License Parser & Validator

```go
// framework/license/license.go

type License struct {
    Tier       LicenseTier
    OrgID      string
    OrgName    string
    Seats      int      // -1 = unlimited
    Nodes      int      // -1 = unlimited
    Features   []string
    Domain     string
    IsTrial    bool
    IssuedAt   time.Time
    ExpiresAt  time.Time
    JTI        string   // unique license ID
    Raw        string   // original JWT string
}

type LicenseTier string
const (
    TierCommunity       LicenseTier = "community"
    TierPro             LicenseTier = "pro"
    TierEnterprise      LicenseTier = "enterprise"
    TierEnterpriseTrial LicenseTier = "enterprise_trial"
)

// framework/license/jwt.go

// PublicKeyPEM is the Bifrost license server's RSA public key (embedded in binary)
//go:embed keys/bifrost-license-2026.pub.pem
var PublicKeyPEM []byte

func ParseLicense(rawJWT string) (*License, error) {
    // Parse without verification first to get kid
    token, err := jwt.ParseInsecure([]byte(rawJWT), jwt.WithTypedClaim("features", []string{}))
    if err != nil { return nil, fmt.Errorf("invalid license JWT: %w", err) }
    
    // Verify RS256 signature
    pubKey, err := loadPublicKey(PublicKeyPEM)
    if err != nil { return nil, err }
    
    _, err = jwt.Parse([]byte(rawJWT),
        jwt.WithKey(jwa.RS256, pubKey),
        jwt.WithIssuer("https://license.getbifrost.ai"),
        jwt.WithAudience("bifrost-gateway"),
        jwt.WithValidate(true),
    )
    if err != nil { return nil, fmt.Errorf("license signature invalid: %w", err) }
    
    // Extract claims
    return extractLicense(token)
}

func extractLicense(token jwt.Token) (*License, error) {
    tier, _ := token.Get("tier")
    features, _ := token.Get("features")
    seats, _ := token.Get("seats")
    nodes, _ := token.Get("nodes")
    orgID := token.Subject()
    orgName, _ := token.Get("org")
    isTrial, _ := token.Get("trial")
    
    return &License{
        Tier:      LicenseTier(tier.(string)),
        OrgID:     orgID,
        OrgName:   orgName.(string),
        Seats:     toInt(seats, -1),
        Nodes:     toInt(nodes, -1),
        Features:  toStringSlice(features),
        IsTrial:   toBool(isTrial),
        IssuedAt:  token.IssuedAt(),
        ExpiresAt: token.Expiration(),
        JTI:       token.JwtID(),
    }, nil
}
```

---

## 5. Feature Enforcer

```go
// framework/license/enforcer.go

// Feature → minimum required tier mapping
var featureTierMap = map[string]LicenseTier{
    "rbac":             TierEnterprise,
    "audit_logs":       TierPro,
    "guardrails":       TierPro,
    "pii_redactor":     TierPro,
    "sso_oidc":         TierEnterprise,
    "sso_saml":         TierEnterprise,
    "scim":             TierEnterprise,
    "adaptive_routing": TierPro,
    "clustering":       TierEnterprise,
    "alerts":           TierPro,
    "vault":            TierEnterprise,
    "large_payload":    TierPro,
    "mcp_tool_groups":  TierPro,
    "user_groups":      TierEnterprise,
    "data_connectors":  TierEnterprise,
}

type LicenseEnforcer struct {
    license *License
    mu      sync.RWMutex
}

var globalEnforcer = &LicenseEnforcer{}  // set at startup

func IsFeatureEnabled(feature string) bool {
    globalEnforcer.mu.RLock()
    defer globalEnforcer.mu.RUnlock()
    
    if globalEnforcer.license == nil {
        return false  // no license = community tier only
    }
    
    lic := globalEnforcer.license
    
    // Expired license: grace period of 7 days
    if time.Now().After(lic.ExpiresAt.Add(7 * 24 * time.Hour)) {
        return false
    }
    
    // Check explicit feature list in license
    for _, f := range lic.Features {
        if f == feature { return true }
    }
    
    return false
}

func (e *LicenseEnforcer) Update(lic *License) {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.license = lic
}

func (e *LicenseEnforcer) Get() *License {
    e.mu.RLock()
    defer e.mu.RUnlock()
    return e.license
}
```

---

## 6. License Loading — Environment Priority

```go
// transports/bifrost-http/server/server.go (MODIFY bootstrap)

func loadLicense() *license.License {
    // Priority 1: Environment variable
    rawJWT := os.Getenv("BIFROST_LICENSE_KEY")
    
    // Priority 2: Vault (if configured)
    if rawJWT == "" && vaultClient != nil {
        rawJWT, _ = vaultClient.KVGet(ctx, "bifrost/license")
    }
    
    // Priority 3: config.json field
    if rawJWT == "" {
        rawJWT = serverConfig.LicenseKey  // read-only field
    }
    
    if rawJWT == "" {
        logger.Info("No license key found — running in Community tier")
        return nil
    }
    
    lic, err := license.ParseLicense(rawJWT)
    if err != nil {
        logger.Error("Invalid license key", "error", err)
        return nil
    }
    
    if time.Now().After(lic.ExpiresAt) {
        logger.Warn("License expired", "expired_at", lic.ExpiresAt, "grace_period", "7 days")
    }
    
    logger.Info("License loaded",
        "tier", lic.Tier, "org", lic.OrgName,
        "expires", lic.ExpiresAt.Format("2006-01-02"),
        "features", len(lic.Features),
    )
    
    license.SetGlobalLicense(lic)
    return lic
}
```

---

## 7. Feature Gate Usage Pattern

```go
// In all enterprise handlers and plugins:

import "github.com/maximhq/bifrost/framework/license"

// Handler example: RBAC
func (h *RBACHandler) ListRoles(ctx *fasthttp.RequestCtx) {
    if !license.IsFeatureEnabled("rbac") {
        ctx.SetStatusCode(402)  // Payment Required
        ctx.SetBodyString(`{"error":{"message":"RBAC requires Enterprise license","code":"license_required","feature":"rbac"}}`)
        return
    }
    // ... actual implementation
}

// Plugin example: GuardrailsPlugin.PreLLMHook
func (p *GuardrailsPlugin) PreLLMHook(...) {
    if !license.IsFeatureEnabled("guardrails") {
        return req, nil, nil  // silently pass through
    }
    // ... actual implementation
}
```

---

## 8. License API

```go
// transports/bifrost-http/handlers/license.go

// GET /api/license
// Returns (NO auth required for status check):
type LicenseStatus struct {
    Tier          string    `json:"tier"`
    OrgName       string    `json:"org_name"`
    IsValid       bool      `json:"is_valid"`
    ExpiresAt     time.Time `json:"expires_at"`
    DaysRemaining int       `json:"days_remaining"`
    Features      []string  `json:"features"`
    Seats         int       `json:"seats"`
    Nodes         int       `json:"nodes"`
    IsTrial       bool      `json:"is_trial"`
    // Omit: JTI, OrgID, raw JWT
}

// POST /api/license/validate   (super_admin only)
// Body: { "license_key": "eyJ..." }
// Validates without applying — returns LicenseStatus for preview
// Useful before updating BIFROST_LICENSE_KEY env var

// GET /api/license/features     (authenticated)
// Returns map of feature → enabled for current license
// Used by UI to show/hide enterprise feature navigation items
```

---

## 9. License Expiry Handling

```go
// framework/license/enforcer.go

// Grace period: features continue working for 7 days after expiry with warnings
// Hard cutoff: after grace period, IsFeatureEnabled returns false

// Background goroutine: check expiry daily, emit warnings
func StartExpiryWatcher(lic *License, logger Logger) {
    go func() {
        for {
            time.Sleep(24 * time.Hour)
            if lic == nil { continue }
            
            daysLeft := time.Until(lic.ExpiresAt).Hours() / 24
            switch {
            case daysLeft <= 0:
                logger.Error("License expired — enterprise features disabled after 7-day grace period")
            case daysLeft <= 7:
                logger.Warn("License expiring soon", "days_remaining", int(daysLeft))
            case daysLeft <= 30:
                logger.Info("License reminder", "days_remaining", int(daysLeft))
            }
        }
    }()
}
```

---

## 10. UI Integration

```go
// ui/lib/license.ts

// Called on app load via GET /api/license/features
// Stored in React context to conditionally render enterprise nav items

type FeatureFlags = {
    rbac: boolean
    audit_logs: boolean
    guardrails: boolean
    pii_redactor: boolean
    sso_oidc: boolean
    clustering: boolean
    // ...
}
```

```tsx
// ui/components/EnterpriseGate.tsx

interface Props {
    feature: keyof FeatureFlags
    children: React.ReactNode
    fallback?: React.ReactNode
}

export function EnterpriseGate({ feature, children, fallback }: Props) {
    const features = useLicenseFeatures()
    if (!features[feature]) {
        return fallback ?? <UpgradePrompt feature={feature} />
    }
    return <>{children}</>
}
```

```
ui/app/enterprise/license/
├── page.tsx                    — License status dashboard
└── components/
    ├── LicenseCard.tsx         — Tier badge + feature list + expiry date
    ├── FeatureMatrix.tsx       — All features with enabled/disabled status
    └── UpgradePrompt.tsx       — CTA for upgrading tier
```

---

## 11. Offline Validation

All license validation is done locally using the embedded RSA public key. No external network call is made during validation. This ensures:
- Air-gapped deployments work
- Network outages don't affect production workloads
- Sub-millisecond validation latency

---

## 12. Public Key Rotation

When the license signing key is rotated (annually), the new key is distributed via binary update. The `kid` header in the JWT indicates which key to use, allowing multiple keys to coexist during transition:

```go
// framework/license/keys/ (directory)
//go:embed keys/bifrost-license-2026.pub.pem
var key2026 []byte

//go:embed keys/bifrost-license-2027.pub.pem
var key2027 []byte

var publicKeys = map[string][]byte{
    "bifrost-license-2026": key2026,
    "bifrost-license-2027": key2027,
}

func loadPublicKey(kid string) (crypto.PublicKey, error) {
    pem, ok := publicKeys[kid]
    if !ok { return nil, fmt.Errorf("unknown key ID: %s", kid) }
    return parseRSAPublicKey(pem)
}
```

---

## 13. Dependencies

```
# framework/go.mod additions:
github.com/lestrrat-go/jwx/v3 v3.x.x   # JWT parsing and validation
```
