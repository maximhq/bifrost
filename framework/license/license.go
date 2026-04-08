// Package license provides enterprise feature gating for Bifrost.
// Every enterprise feature handler MUST call IsFeatureEnabled before
// executing feature-specific logic.
//
// Usage:
//
//	if !license.IsFeatureEnabled("rbac") {
//	    return nil, &schemas.BifrostError{StatusCode: 402, Type: schemas.ErrTypeValidationError}
//	}
package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Feature names — MUST match keys used in license payloads.
const (
	FeatureRBAC           = "rbac"
	FeatureAuditLogs      = "audit_logs"
	FeatureGuardrails     = "guardrails"
	FeaturePIIRedactor    = "pii_redactor"
	FeatureSSOOIDC        = "sso_oidc"
	FeatureSSOSAML        = "sso_saml"
	FeatureSCIM           = "scim"
	FeatureAdaptiveRouting = "adaptive_routing"
	FeatureClustering     = "clustering"
	FeatureVault          = "vault"
	FeatureAlerts         = "alerts"
	FeatureLargePayload   = "large_payload"
	FeatureMCPToolGroups  = "mcp_tool_groups"
	FeatureUserGroups     = "user_groups"
	FeatureDataConnectors = "data_connectors"
)

// LicensePlan represents a license tier.
type LicensePlan string

const (
	PlanCommunity   LicensePlan = "community"
	PlanStartup     LicensePlan = "startup"
	PlanEnterprise  LicensePlan = "enterprise"
	PlanOnPremise   LicensePlan = "on_premise"
)

// LicensePayload is the decoded content of a license JWT.
type LicensePayload struct {
	// Standard JWT claims
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"` // organization ID
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
	ExpiresAt int64  `json:"exp"`

	// Bifrost-specific claims
	OrgName  string            `json:"org_name"`
	Plan     LicensePlan       `json:"plan"`
	Features map[string]bool   `json:"features"` // feature_name → enabled
	Limits   map[string]int64  `json:"limits"`   // e.g. "max_providers" → 10
}

// IsValidAt returns true if the license is currently valid (not expired, not before nbf).
func (p *LicensePayload) IsValidAt(t time.Time) bool {
	unix := t.Unix()
	if p.NotBefore > 0 && unix < p.NotBefore {
		return false
	}
	if p.ExpiresAt > 0 && unix > p.ExpiresAt {
		return false
	}
	return true
}

// LicenseState holds the active license and a flag for developer mode.
type LicenseState struct {
	mu          sync.RWMutex
	payload     *LicensePayload
	devMode     bool // when true, all features are enabled without a valid license
}

var global = &LicenseState{}

// Init initialises the license system. It reads the license key from the
// BIFROST_LICENSE_KEY environment variable, validates the JWT signature, and
// stores the decoded payload globally.
//
// If BIFROST_DEV_MODE=true (or no license is set), dev mode is activated and
// all features are enabled unconditionally.
func Init(publicKeyB64 string) error {
	global.mu.Lock()
	defer global.mu.Unlock()

	if os.Getenv("BIFROST_DEV_MODE") == "true" {
		global.devMode = true
		return nil
	}

	licenseKey := os.Getenv("BIFROST_LICENSE_KEY")
	if licenseKey == "" {
		// No license key → dev mode (community features only in production, all in dev)
		global.devMode = true
		return nil
	}

	pub, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return err
	}

	payload, err := validateJWT(licenseKey, ed25519.PublicKey(pub))
	if err != nil {
		return err
	}

	if !payload.IsValidAt(time.Now()) {
		return ErrLicenseExpired
	}

	global.payload = payload
	global.devMode = false
	return nil
}

// SetDevMode force-enables dev mode (for tests).
func SetDevMode(on bool) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.devMode = on
}

// IsFeatureEnabled reports whether the named enterprise feature is enabled.
// In dev mode every feature is enabled; otherwise the feature must be
// present and true in the license payload.
func IsFeatureEnabled(feature string) bool {
	global.mu.RLock()
	defer global.mu.RUnlock()

	if global.devMode {
		return true
	}
	if global.payload == nil {
		return false
	}
	if !global.payload.IsValidAt(time.Now()) {
		return false
	}
	return global.payload.Features[feature]
}

// GetPayload returns a copy of the active license payload, or nil if no valid
// license is loaded.
func GetPayload() *LicensePayload {
	global.mu.RLock()
	defer global.mu.RUnlock()
	if global.payload == nil {
		return nil
	}
	cp := *global.payload
	return &cp
}

// IsDevMode reports whether dev mode is active.
func IsDevMode() bool {
	global.mu.RLock()
	defer global.mu.RUnlock()
	return global.devMode
}

// LicenseStatus is the public-facing license summary returned by GET /api/license.
// The raw JWT is never included.
type LicenseStatus struct {
	Tier      string `json:"tier"`       // "community" | "startup" | "enterprise" | "on_premise"
	IsValid   bool   `json:"is_valid"`
	IsDevMode bool   `json:"dev_mode"`
	OrgName   string `json:"org_name,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"` // Unix timestamp; 0 = no expiry
}

// GetStatus returns the current license status without exposing the raw JWT.
func GetStatus() LicenseStatus {
	global.mu.RLock()
	defer global.mu.RUnlock()

	if global.devMode {
		return LicenseStatus{Tier: "community", IsValid: true, IsDevMode: true}
	}
	if global.payload == nil {
		return LicenseStatus{Tier: "community", IsValid: true, IsDevMode: false}
	}
	valid := global.payload.IsValidAt(time.Now())
	return LicenseStatus{
		Tier:      string(global.payload.Plan),
		IsValid:   valid,
		IsDevMode: false,
		OrgName:   global.payload.OrgName,
		ExpiresAt: global.payload.ExpiresAt,
	}
}

// validateJWT validates a simple "header.payload.signature" JWT where the
// signature is Ed25519 over base64url(header)+"."+base64url(payload).
func validateJWT(token string, pub ed25519.PublicKey) (*LicensePayload, error) {
	parts := splitJWT(token)
	if len(parts) != 3 {
		return nil, ErrInvalidLicense
	}

	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrInvalidLicense
	}
	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		return nil, ErrInvalidSignature
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidLicense
	}

	var payload LicensePayload
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		return nil, ErrInvalidLicense
	}

	return &payload, nil
}

func splitJWT(token string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			parts = append(parts, token[start:i])
			start = i + 1
		}
	}
	parts = append(parts, token[start:])
	return parts
}
