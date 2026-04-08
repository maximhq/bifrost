# TECH-008 — HashiCorp Vault Integration

**Feature ID:** VAULT  
**SRS Reference:** §3.20 (VAULT-01 → VAULT-10)  
**CR Reference:** CR-ENT-001, CR-ENT-002  
**Version:** 1.0 | **Date:** 2026-04-08  
**Status:** Design Ready

---

## 1. Overview

Integrate HashiCorp Vault as an external secret backend, replacing direct API key storage in the database. Vault provides dynamic secret generation, automatic rotation, and centralized secret lifecycle management.

**Integration modes:**
1. **Static Secret Mount** — store API keys in Vault KV v2, read on demand
2. **Dynamic Secrets** — Vault generates short-lived credentials (AWS IAM, database)
3. **Transit Encryption** — use Vault Transit engine to encrypt/decrypt config data (replaces `framework/encrypt`)
4. **AppRole Auth** — Bifrost authenticates to Vault using AppRole (for automated deployments)
5. **Kubernetes Auth** — authenticate using service account JWT (for K8s deployments)

---

## 2. Architecture Mapping

```
framework/
├── vault/                         (NEW package)
│   ├── client.go                  VaultClient — HTTP connection, auth, renewal
│   ├── auth/
│   │   ├── approle.go             AppRole login + token renewal
│   │   ├── kubernetes.go          Kubernetes JWT auth
│   │   └── token.go               Static token auth (for dev)
│   ├── kv.go                      KV v2 secret read/list/write
│   ├── transit.go                 Transit engine encrypt/decrypt
│   ├── dynamic.go                 Dynamic secret lease management
│   ├── renewer.go                 Token + lease renewal goroutine
│   └── config.go                  VaultConfig

framework/configstore/
└── encryption.go     (MODIFY) Support Vault Transit as alternate encryption backend

core/schemas/
└── bifrost.go        (MODIFY) Add "vault://" prefix resolution in key parsing

transports/bifrost-http/
└── handlers/vault.go  (NEW) Vault status API
```

---

## 3. Configuration

```go
// framework/vault/config.go

type VaultConfig struct {
    Enabled    bool
    Address    string              // e.g., "https://vault.example.com:8200"
    Namespace  string              // Vault Enterprise namespace (optional)
    Auth       VaultAuthConfig
    KV         *VaultKVConfig
    Transit    *VaultTransitConfig
    TLSConfig  *VaultTLSConfig
}

type VaultAuthConfig struct {
    Method     AuthMethod          // "approle" | "kubernetes" | "token"
    // AppRole
    RoleID     string
    SecretID   string              // or env.VAULT_SECRET_ID
    // Kubernetes
    K8sRole    string
    JWTPath    string              // default: /var/run/secrets/kubernetes.io/serviceaccount/token
    K8sMount   string              // default: "kubernetes"
    // Token
    Token      string              // or env.VAULT_TOKEN
}

type VaultKVConfig struct {
    Mount      string    // default: "secret"
    PathPrefix string    // e.g., "bifrost/prod"
    // Secret path pattern: {PathPrefix}/providers/{provider_name}/keys/{key_id}
}

type VaultTransitConfig struct {
    Mount      string    // default: "transit"
    KeyName    string    // encryption key name in Vault
}

type VaultTLSConfig struct {
    CACert     string    // path to CA cert file or PEM content
    ClientCert string    // mTLS client cert
    ClientKey  string    // mTLS client key
    Insecure   bool      // skip TLS verification (dev only)
}

type AuthMethod string
const (
    AuthAppRole    AuthMethod = "approle"
    AuthKubernetes AuthMethod = "kubernetes"
    AuthToken      AuthMethod = "token"
)
```

---

## 4. VaultClient

```go
// framework/vault/client.go

type VaultClient struct {
    config   VaultConfig
    client   *api.Client        // vault.hashicorp.com/api
    token    atomic.Value       // current Vault token
    renewer  *Renewer
    mu       sync.RWMutex
}

func NewVaultClient(config VaultConfig) (*VaultClient, error) {
    cfg := api.DefaultConfig()
    cfg.Address = config.Address
    if config.TLSConfig != nil {
        cfg.ConfigureTLS(&api.TLSConfig{
            CACert:     config.TLSConfig.CACert,
            ClientCert: config.TLSConfig.ClientCert,
            ClientKey:  config.TLSConfig.ClientKey,
            Insecure:   config.TLSConfig.Insecure,
        })
    }
    
    client, err := api.NewClient(cfg)
    // ... set namespace, authenticate
    return &VaultClient{config: config, client: client}, nil
}

func (c *VaultClient) Authenticate() error {
    switch c.config.Auth.Method {
    case AuthAppRole:
        return c.authenticateAppRole()
    case AuthKubernetes:
        return c.authenticateKubernetes()
    case AuthToken:
        c.client.SetToken(c.config.Auth.Token)
        return nil
    }
    return fmt.Errorf("unknown auth method: %s", c.config.Auth.Method)
}
```

---

## 5. KV Secret Resolution

### 5.1 Secret Path Convention

```
KV v2 mount: "secret"
Path prefix: "bifrost/prod"

Provider API key:
  secret/data/bifrost/prod/providers/openai/keys/key_001
  → { "data": { "value": "sk-..." } }

Encryption key:
  secret/data/bifrost/prod/encryption/master
  → { "data": { "key": "base64-encoded-32-bytes" } }
```

### 5.2 Key Resolution in Config

Extend the existing `env.VAR_NAME` resolver to support `vault://` URIs:

```go
// framework/envutils/resolve.go (MODIFY)

func Resolve(value string, vaultClient *vault.VaultClient) string {
    if strings.HasPrefix(value, "env.") {
        return os.Getenv(strings.TrimPrefix(value, "env."))
    }
    if strings.HasPrefix(value, "vault://") && vaultClient != nil {
        path := strings.TrimPrefix(value, "vault://")
        secret, err := vaultClient.KVGet(context.Background(), path)
        if err != nil {
            logger.Warn("vault secret read failed", "path", path, "error", err)
            return ""
        }
        return secret
    }
    return value
}

// Usage in config:
// { "value": "vault://bifrost/prod/providers/openai/keys/key_001" }
```

### 5.3 KV Read Implementation

```go
// framework/vault/kv.go

func (c *VaultClient) KVGet(ctx context.Context, path string) (string, error) {
    c.mu.RLock()
    defer c.mu.RUnlock()
    
    fullPath := fmt.Sprintf("%s/data/%s", c.config.KV.Mount, path)
    secret, err := c.client.Logical().ReadWithContext(ctx, fullPath)
    if err != nil {
        return "", fmt.Errorf("vault KV read %q: %w", path, err)
    }
    if secret == nil || secret.Data == nil {
        return "", fmt.Errorf("vault secret not found: %q", path)
    }
    
    data, ok := secret.Data["data"].(map[string]interface{})
    if !ok {
        return "", fmt.Errorf("invalid vault secret format: %q", path)
    }
    
    val, ok := data["value"].(string)
    if !ok {
        return "", fmt.Errorf("vault secret %q missing 'value' field", path)
    }
    return val, nil
}

func (c *VaultClient) KVPut(ctx context.Context, path string, data map[string]interface{}) error {
    fullPath := fmt.Sprintf("%s/data/%s", c.config.KV.Mount, path)
    _, err := c.client.Logical().WriteWithContext(ctx, fullPath, map[string]interface{}{"data": data})
    return err
}

func (c *VaultClient) KVList(ctx context.Context, path string) ([]string, error) {
    fullPath := fmt.Sprintf("%s/metadata/%s", c.config.KV.Mount, path)
    secret, err := c.client.Logical().ListWithContext(ctx, fullPath)
    if err != nil { return nil, err }
    keys, _ := secret.Data["keys"].([]interface{})
    result := make([]string, len(keys))
    for i, k := range keys { result[i] = k.(string) }
    return result, nil
}
```

---

## 6. Transit Encryption Backend

Replace `framework/encrypt` AES-256-GCM with Vault Transit:

```go
// framework/vault/transit.go

func (c *VaultClient) Encrypt(ctx context.Context, plaintext []byte) (string, error) {
    path := fmt.Sprintf("%s/encrypt/%s", c.config.Transit.Mount, c.config.Transit.KeyName)
    b64 := base64.StdEncoding.EncodeToString(plaintext)
    secret, err := c.client.Logical().WriteWithContext(ctx, path, map[string]interface{}{
        "plaintext": b64,
    })
    if err != nil { return "", err }
    return secret.Data["ciphertext"].(string), nil
}

func (c *VaultClient) Decrypt(ctx context.Context, ciphertext string) ([]byte, error) {
    path := fmt.Sprintf("%s/decrypt/%s", c.config.Transit.Mount, c.config.Transit.KeyName)
    secret, err := c.client.Logical().WriteWithContext(ctx, path, map[string]interface{}{
        "ciphertext": ciphertext,
    })
    if err != nil { return nil, err }
    b64 := secret.Data["plaintext"].(string)
    return base64.StdEncoding.DecodeString(b64)
}
```

```go
// framework/configstore/encryption.go (MODIFY)
// Add VaultTransitEncryptor that implements Encryptor interface

type Encryptor interface {
    Encrypt(ctx context.Context, plaintext []byte) (string, error)
    Decrypt(ctx context.Context, ciphertext string) ([]byte, error)
}

// Existing: AESEncryptor (local AES-256-GCM)
// New:      VaultTransitEncryptor (calls Vault Transit API)
```

---

## 7. Token Renewal

Vault tokens expire. The renewer goroutine keeps the token alive:

```go
// framework/vault/renewer.go

type Renewer struct {
    client    *VaultClient
    interval  time.Duration
    threshold float64    // renew when TTL < threshold * lease_duration
    done      chan struct{}
}

func (r *Renewer) Start() {
    go func() {
        ticker := time.NewTicker(r.interval)
        for {
            select {
            case <-ticker.C:
                r.tryRenew()
            case <-r.done:
                return
            }
        }
    }()
}

func (r *Renewer) tryRenew() {
    secret, err := r.client.client.Auth().Token().RenewSelfWithContext(
        context.Background(), int(r.interval.Seconds()),
    )
    if err != nil {
        // Token non-renewable or expired — re-authenticate
        logger.Warn("vault token renewal failed, re-authenticating", "error", err)
        r.client.Authenticate()
        return
    }
    r.client.token.Store(secret.Auth.ClientToken)
}
```

---

## 8. Dynamic Secrets (AWS)

For AWS Bedrock, Vault can generate short-lived IAM credentials:

```go
// framework/vault/dynamic.go

type DynamicCredential struct {
    AccessKeyID     string
    SecretAccessKey string
    SecurityToken   string
    LeaseID         string
    LeaseDuration   time.Duration
    ExpiresAt       time.Time
}

func (c *VaultClient) GetAWSCredentials(ctx context.Context, role string) (*DynamicCredential, error) {
    path := fmt.Sprintf("aws/sts/%s", role)
    secret, err := c.client.Logical().WriteWithContext(ctx, path, nil)
    if err != nil { return nil, err }
    
    return &DynamicCredential{
        AccessKeyID:     secret.Data["access_key"].(string),
        SecretAccessKey: secret.Data["secret_key"].(string),
        SecurityToken:   secret.Data["security_token"].(string),
        LeaseID:         secret.LeaseID,
        LeaseDuration:   time.Duration(secret.LeaseDuration) * time.Second,
        ExpiresAt:       time.Now().Add(time.Duration(secret.LeaseDuration) * time.Second),
    }, nil
}
```

---

## 9. Vault Status API

```go
// transports/bifrost-http/handlers/vault.go

// GET /api/vault/status
// Returns: connected, token_ttl, auth_method, transit_key_info, accessible_paths

// POST /api/vault/rotate-key     (super_admin only, audit-logged)
// Triggers key rotation on Vault Transit engine

// POST /api/vault/sync           (super_admin only)
// Force re-read all vault:// values into config cache

// GET /api/vault/test            (super_admin only)
// Tests connectivity to Vault and returns health info
```

---

## 10. Server Bootstrap Integration

```go
// transports/bifrost-http/server/server.go

func Bootstrap(config Config) error {
    // ... existing init ...
    
    // Initialize Vault client if configured
    if config.Vault.Enabled {
        vaultClient, err := vault.NewVaultClient(config.Vault)
        if err != nil {
            return fmt.Errorf("vault init failed: %w", err)
        }
        if err := vaultClient.Authenticate(); err != nil {
            return fmt.Errorf("vault auth failed: %w", err)
        }
        vaultClient.StartRenewer()
        
        // Inject into envutils resolver
        envutils.SetVaultClient(vaultClient)
        
        // Use Vault Transit for config encryption if configured
        if config.Vault.Transit != nil {
            configStore.SetEncryptor(vault.NewTransitEncryptor(vaultClient))
        }
    }
}
```

---

## 11. UI Components

```
ui/app/enterprise/vault/
├── page.tsx                    — Vault connection status + token info
└── components/
    ├── VaultConnectionCard.tsx — Auth method, TTL, mount points
    ├── SecretBrowser.tsx       — Browse KV paths accessible to Bifrost
    └── KeyRotationPanel.tsx    — Transit key rotation + re-encryption trigger
```

---

## 12. Dependencies

```
# framework/go.mod additions:
github.com/hashicorp/vault/api v1.x.x   # Official Vault API client
```
