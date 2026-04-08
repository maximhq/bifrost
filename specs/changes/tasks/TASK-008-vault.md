# TASK-008 — Vault Integration

**Feature:** HashiCorp Vault Integration  
**TECH Spec:** [TECH-008-vault.md](../TECH-008-vault.md)  
**Phase:** 3 (Infrastructure)  
**Depends on:** TASK-014 (license)  
**Estimate:** 5 days  
**Assignee:** —  
**Status:** 🟢 Completed

---

## Context

Vault integration replaces direct API key storage in the database with HashiCorp Vault as the secret backend. API keys stored as `vault://path/to/secret` references are resolved at runtime. The `vault://` resolver is transparent to the rest of the system.

**Integration modes supported:**
1. Static KV v2 — read API keys from Vault paths
2. Dynamic Secrets — Vault-generated AWS IAM credentials for Bedrock
3. Transit Encryption — replace `framework/encrypt` AES with Vault Transit engine
4. Auth: AppRole (production), Kubernetes (K8s deployments), Token (dev only)

---

## Tasks

### TASK-008-01 — `framework/vault` package scaffold

**Files to create:**
- `framework/vault/config.go` — `VaultConfig`, `VaultAuthConfig`, `VaultKVConfig`, `VaultTransitConfig`, `VaultTLSConfig`
- `framework/vault/client.go` — `VaultClient` (connection, auth dispatch)
- `framework/vault/auth/approle.go` — AppRole login + token renewal
- `framework/vault/auth/kubernetes.go` — Kubernetes JWT auth
- `framework/vault/auth/token.go` — static token auth (dev)
- `framework/vault/kv.go` — `KVGet`, `KVPut`, `KVList`
- `framework/vault/transit.go` — `Encrypt`, `Decrypt`
- `framework/vault/dynamic.go` — `GetAWSCredentials`, lease management
- `framework/vault/renewer.go` — token renewal goroutine
- `framework/go.mod` — add `github.com/hashicorp/vault/api v1.x.x`

**Acceptance criteria:**
- [ ] `NewVaultClient(config)` returns error if config is invalid
- [ ] `Authenticate()` dispatches to correct auth method
- [ ] `KVGet(ctx, path)` reads from `{mount}/data/{path}` and returns the `value` field
- [ ] TLS config applied: CA cert, client cert/key, insecure skip verify
- [ ] `framework/go.mod` updated; `go work sync` passes

---

### TASK-008-02 — Token renewal goroutine

**File:** `framework/vault/renewer.go`

**Acceptance criteria:**
- [ ] Renewer started via `VaultClient.StartRenewer()` after successful authentication
- [ ] Renewal attempted when token TTL < 50% of lease duration (configurable via `threshold`)
- [ ] On renewal failure: re-authenticate from scratch
- [ ] Renewer goroutine exits cleanly on `VaultClient.Close()`
- [ ] Token renewal failures logged as warnings (not fatal)

---

### TASK-008-03 — `vault://` URI resolver

**Files to modify:**
- `framework/envutils/resolve.go` (or equivalent) — extend `Resolve()` to handle `vault://` prefix

**Implementation:**
```go
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
```

**Acceptance criteria:**
- [ ] `vault://` prefix resolved by calling `KVGet` on the VaultClient
- [ ] Resolver returns empty string + warning log on Vault error (fail open)
- [ ] Vault not configured → `vault://` values treated as empty string with warning
- [ ] `env.VAR_NAME` resolver still works alongside `vault://`
- [ ] Unit test: resolver with mock VaultClient

---

### TASK-008-04 — Transit encryption backend

**Files to modify/create:**
- `framework/configstore/encryption.go` — add `VaultTransitEncryptor` implementing `Encryptor` interface
- `framework/vault/transit.go` — `Encrypt()`, `Decrypt()` already created in TASK-008-01

**Acceptance criteria:**
- [ ] `VaultTransitEncryptor` implements same `Encryptor` interface as `AESEncryptor`
- [ ] If `vault.transit` config present: use Vault Transit for config encryption
- [ ] If not configured: fall back to existing AES-256-GCM
- [ ] Existing encrypted data (AES) remains readable after migration (no forced re-encryption)
- [ ] Re-encryption endpoint: `POST /api/vault/reencrypt` (super_admin only) — migrates existing AES-encrypted values to Transit

---

### TASK-008-05 — Dynamic secrets for Bedrock

**Files to modify:**
- `core/providers/bedrock/bedrock.go` — add optional `DynamicCredentialProvider` interface
- `framework/vault/dynamic.go` — `GetAWSCredentials()`, credential caching

**Acceptance criteria:**
- [ ] Bedrock provider accepts dynamic credentials when `vault.dynamic.aws_role` configured
- [ ] Credentials cached in memory until `ExpiresAt - 60s` (pre-refresh)
- [ ] Credential refresh triggered automatically before expiry
- [ ] Falls back to static credentials if Vault unavailable

---

### TASK-008-06 — Server bootstrap integration

**Files to modify:**
- `transports/bifrost-http/server/server.go` — `Bootstrap()` initializes Vault client

**Flow:**
```go
if config.Vault.Enabled {
    vaultClient, err = vault.NewVaultClient(config.Vault)
    vaultClient.Authenticate()
    vaultClient.StartRenewer()
    envutils.SetVaultClient(vaultClient)
    if config.Vault.Transit != nil {
        configStore.SetEncryptor(vault.NewTransitEncryptor(vaultClient))
    }
}
```

**Acceptance criteria:**
- [ ] Vault initialization failure: log error + fatal exit (Vault is required if enabled)
- [ ] Vault not configured: startup proceeds normally with existing behavior
- [ ] All `vault://` references in config resolved after `envutils.SetVaultClient()` called

---

### TASK-008-07 — Vault status API

**Files to create:**
- `transports/bifrost-http/handlers/vault.go`

**Endpoints:**
```
GET  /api/vault/status            — connection status, token TTL, auth method (super_admin)
POST /api/vault/sync              — force re-read all vault:// values (super_admin)
POST /api/vault/rotate-key        — trigger Transit key rotation (super_admin, audit-logged)
GET  /api/vault/test              — test connectivity + list accessible paths
```

**Acceptance criteria:**
- [ ] `/api/vault/status` always responds (returns `{"enabled": false}` if Vault not configured)
- [ ] Token TTL and auth method included in status response
- [ ] All endpoints require `vault` feature enabled
- [ ] Key rotation is audit-logged

---

### TASK-008-08 — UI: Vault status page

**Files to create:**
- `ui/app/enterprise/vault/page.tsx`
- `ui/app/enterprise/vault/components/VaultConnectionCard.tsx`
- `ui/app/enterprise/vault/components/KeyRotationPanel.tsx`

**Acceptance criteria:**
- [ ] Connection status card: auth method, token TTL, mount points
- [ ] "Test Connection" button calls `/api/vault/test` inline
- [ ] "Rotate Key" button with confirmation dialog calls `/api/vault/rotate-key`
- [ ] Page inside `<EnterpriseGate feature="vault">`

---

## Definition of Done

- [ ] All subtasks complete
- [ ] Integration test: `KVGet` with mock Vault server (using `vault server -dev`)
- [ ] Integration test: `vault://secret/path` in provider API key config → resolved correctly
- [ ] Integration test: token renewal goroutine re-authenticates on forced token invalidation
- [ ] `go mod tidy` in `framework/` succeeds
- [ ] `make build` passes
