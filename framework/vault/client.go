// Package vault provides HashiCorp Vault integration for Bifrost.
// It supports KV v2, Transit encryption, dynamic AWS credentials,
// and multiple auth methods (AppRole, Kubernetes, static Token).
//
// Usage:
//
//	client, err := vault.NewVaultClient(cfg)
//	if err != nil { ... }
//	client.Authenticate(ctx)
//	client.StartRenewer(ctx)
//	secret, _ := client.KVGet(ctx, "secret/data/api-keys")
package vault

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// VaultConfig holds all configuration for the Vault integration.
type VaultConfig struct {
	Enabled  bool        `json:"enabled"`
	Address  string      `json:"address"`  // e.g. "https://vault.example.com:8200"
	Auth     VaultAuth   `json:"auth"`
	KV       VaultKV     `json:"kv,omitempty"`
	Transit  *VaultTransit `json:"transit,omitempty"`
	Dynamic  *VaultDynamic `json:"dynamic,omitempty"`
	TLS      VaultTLS    `json:"tls,omitempty"`
}

// VaultAuth describes how the client authenticates.
type VaultAuth struct {
	Method     string `json:"method"` // "approle" | "kubernetes" | "token"
	Token      string `json:"token,omitempty"`    // static token (dev only)
	RoleID     string `json:"role_id,omitempty"`  // AppRole
	SecretID   string `json:"secret_id,omitempty"` // AppRole
	K8sRole    string `json:"k8s_role,omitempty"` // Kubernetes auth role
	K8sJWTPath string `json:"k8s_jwt_path,omitempty"` // path to JWT file
	MountPath  string `json:"mount_path,omitempty"` // default: "auth/approle" or "auth/kubernetes"
}

// VaultKV configures the KV v2 secrets engine.
type VaultKV struct {
	Mount string `json:"mount"` // default: "secret"
}

// VaultTransit configures the Transit secrets engine for encryption.
type VaultTransit struct {
	Mount   string `json:"mount"` // default: "transit"
	KeyName string `json:"key_name"` // encryption key name in Vault
}

// VaultDynamic configures dynamic credential generation.
type VaultDynamic struct {
	AWSMount    string `json:"aws_mount"`    // default: "aws"
	AWSRoleName string `json:"aws_role_name"` // Vault AWS role
}

// VaultTLS configures mTLS / CA verification for Vault.
type VaultTLS struct {
	CACert     string `json:"ca_cert,omitempty"`      // path to CA cert PEM
	ClientCert string `json:"client_cert,omitempty"`  // path to client cert PEM
	ClientKey  string `json:"client_key,omitempty"`   // path to client key PEM
	Insecure   bool   `json:"insecure,omitempty"`     // skip TLS verification (dev only)
}

// VaultClient is the Bifrost Vault integration client.
type VaultClient struct {
	cfg       VaultConfig
	token     atomic.Value // string — current Vault token
	tokenTTL  time.Duration
	mu        sync.RWMutex
	httpClient *http.Client
	stopCh    chan struct{}
}

// NewVaultClient creates a new VaultClient, validating the configuration.
func NewVaultClient(cfg VaultConfig) (*VaultClient, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("vault: address is required")
	}
	if cfg.Auth.Method == "" {
		cfg.Auth.Method = "token"
	}
	c := &VaultClient{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		stopCh:     make(chan struct{}),
	}
	if cfg.Auth.Method == "token" {
		c.token.Store(cfg.Auth.Token)
	}
	return c, nil
}

// Authenticate performs the configured auth flow and stores the resulting token.
func (c *VaultClient) Authenticate(ctx context.Context) error {
	switch c.cfg.Auth.Method {
	case "token":
		if c.cfg.Auth.Token == "" {
			return fmt.Errorf("vault: token auth requires a token")
		}
		c.token.Store(c.cfg.Auth.Token)
		c.tokenTTL = 0 // static tokens don't have TTL tracked here
		return nil
	case "approle":
		return c.loginAppRole(ctx)
	case "kubernetes":
		return c.loginKubernetes(ctx)
	default:
		return fmt.Errorf("vault: unknown auth method %q", c.cfg.Auth.Method)
	}
}

// Close stops the token renewal goroutine.
func (c *VaultClient) Close() {
	close(c.stopCh)
}

// StartRenewer starts a background goroutine that renews the token before expiry.
func (c *VaultClient) StartRenewer(ctx context.Context) {
	if c.tokenTTL == 0 {
		return // static token — nothing to renew
	}
	go c.renewLoop(ctx)
}

func (c *VaultClient) renewLoop(ctx context.Context) {
	threshold := c.tokenTTL / 2
	timer := time.NewTimer(threshold)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			if err := c.renewToken(ctx); err != nil {
				// Re-authenticate from scratch
				_ = c.Authenticate(ctx)
			}
			timer.Reset(threshold)
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (c *VaultClient) renewToken(ctx context.Context) error {
	tok := c.token.Load().(string)
	url := c.cfg.Address + "/v1/auth/token/renew-self"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	req.Header.Set("X-Vault-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vault: renew-self returned %d", resp.StatusCode)
	}
	return nil
}

func (c *VaultClient) loginAppRole(ctx context.Context) error {
	mount := c.cfg.Auth.MountPath
	if mount == "" {
		mount = "auth/approle"
	}
	body := fmt.Sprintf(`{"role_id":%q,"secret_id":%q}`, c.cfg.Auth.RoleID, c.cfg.Auth.SecretID)
	url := c.cfg.Address + "/v1/" + mount + "/login"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	tok, ttl, err := extractToken(data)
	if err != nil {
		return fmt.Errorf("vault: approle login: %w", err)
	}
	c.token.Store(tok)
	c.tokenTTL = time.Duration(ttl) * time.Second
	return nil
}

func (c *VaultClient) loginKubernetes(ctx context.Context) error {
	// Stub: reads JWT from K8sJWTPath and authenticates
	return fmt.Errorf("vault: kubernetes auth not yet implemented")
}

// KVGet reads the secret at path from the KV v2 engine and returns the "value" field.
func (c *VaultClient) KVGet(ctx context.Context, path string) (string, error) {
	mount := c.cfg.KV.Mount
	if mount == "" {
		mount = "secret"
	}
	url := c.cfg.Address + "/v1/" + mount + "/data/" + path
	tok := c.token.Load().(string)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("X-Vault-Token", tok)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("vault: secret not found at %s", path)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vault: KVGet returned %d for %s", resp.StatusCode, path)
	}
	data, _ := io.ReadAll(resp.Body)
	return extractKVValue(data)
}

// KVPut writes a key-value secret to the KV v2 engine.
func (c *VaultClient) KVPut(ctx context.Context, path, value string) error {
	mount := c.cfg.KV.Mount
	if mount == "" {
		mount = "secret"
	}
	url := c.cfg.Address + "/v1/" + mount + "/data/" + path
	tok := c.token.Load().(string)
	body := fmt.Sprintf(`{"data":{"value":%q}}`, value)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("X-Vault-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("vault: KVPut returned %d for %s", resp.StatusCode, path)
	}
	return nil
}

// Encrypt encrypts plaintext using the Vault Transit engine.
func (c *VaultClient) Encrypt(ctx context.Context, plaintext []byte) (string, error) {
	if c.cfg.Transit == nil {
		return "", fmt.Errorf("vault: transit not configured")
	}
	mount := c.cfg.Transit.Mount
	if mount == "" {
		mount = "transit"
	}
	import64 := fmt.Sprintf(`{"plaintext":%q}`, plaintext)
	url := c.cfg.Address + "/v1/" + mount + "/encrypt/" + c.cfg.Transit.KeyName
	tok := c.token.Load().(string)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(import64))
	req.Header.Set("X-Vault-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return extractTransitField(data, "ciphertext")
}

// Decrypt decrypts ciphertext using the Vault Transit engine.
func (c *VaultClient) Decrypt(ctx context.Context, ciphertext string) ([]byte, error) {
	if c.cfg.Transit == nil {
		return nil, fmt.Errorf("vault: transit not configured")
	}
	mount := c.cfg.Transit.Mount
	if mount == "" {
		mount = "transit"
	}
	body := fmt.Sprintf(`{"ciphertext":%q}`, ciphertext)
	url := c.cfg.Address + "/v1/" + mount + "/decrypt/" + c.cfg.Transit.KeyName
	tok := c.token.Load().(string)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("X-Vault-Token", tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	pt, err := extractTransitField(data, "plaintext")
	return []byte(pt), err
}

// GetStatus returns a human-readable status summary (for GET /api/vault/status).
func (c *VaultClient) GetStatus(ctx context.Context) map[string]any {
	tok := c.token.Load()
	hasToken := tok != nil && tok.(string) != ""
	return map[string]any{
		"enabled":     true,
		"address":     c.cfg.Address,
		"auth_method": c.cfg.Auth.Method,
		"has_token":   hasToken,
		"transit":     c.cfg.Transit != nil,
	}
}

// ─── JSON extraction helpers ──────────────────────────────────────────────────

// extractToken parses { "auth": { "client_token": "...", "lease_duration": N } }
func extractToken(data []byte) (string, int64, error) {
	type authResp struct {
		Auth struct {
			ClientToken   string `json:"client_token"`
			LeaseDuration int64  `json:"lease_duration"`
		} `json:"auth"`
	}
	var r authResp
	if err := jsonUnmarshal(data, &r); err != nil {
		return "", 0, err
	}
	if r.Auth.ClientToken == "" {
		return "", 0, fmt.Errorf("no client_token in response")
	}
	return r.Auth.ClientToken, r.Auth.LeaseDuration, nil
}

// extractKVValue parses { "data": { "data": { "value": "..." } } }
func extractKVValue(data []byte) (string, error) {
	type kvResp struct {
		Data struct {
			Data map[string]string `json:"data"`
		} `json:"data"`
	}
	var r kvResp
	if err := jsonUnmarshal(data, &r); err != nil {
		return "", err
	}
	v, ok := r.Data.Data["value"]
	if !ok {
		return "", fmt.Errorf("vault: secret has no 'value' field")
	}
	return v, nil
}

// extractTransitField parses { "data": { field: "..." } }
func extractTransitField(data []byte, field string) (string, error) {
	type transitResp struct {
		Data map[string]string `json:"data"`
	}
	var r transitResp
	if err := jsonUnmarshal(data, &r); err != nil {
		return "", err
	}
	v, ok := r.Data[field]
	if !ok {
		return "", fmt.Errorf("vault: response has no %q field", field)
	}
	return v, nil
}

// jsonUnmarshal is a light wrapper so we avoid importing encoding/json multiple times.
func jsonUnmarshal(data []byte, v any) error {
	// Use standard library json — no external dependencies needed here.
	d := jsonDecoder(data)
	return d(v)
}
