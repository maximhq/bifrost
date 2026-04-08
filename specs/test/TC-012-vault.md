# Test Cases — HashiCorp Vault Integration

**Suite ID:** TC-012  
**SRS Reference:** §3.20  
**Priority:** P1  
**Type:** Integration + Unit  
**Dependency:** TC-013 (License: vault feature)

---

## Preconditions
- Enterprise license with `vault` feature
- HashiCorp Vault Dev server running at `http://localhost:8200`, root token `dev-root`
- KV v2 secrets engine mounted at `secret/`
- AppRole auth method enabled
- Vault pre-seeded:
  - `vault kv put secret/bifrost/openai api_key="sk-vault-test-openai-key123"`
  - `vault kv put secret/bifrost/anthropic api_key="ant-vault-test-key456"`

---

### TC-012-001 — Vault URI Resolved at Provider Load

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Configure provider with `"api_keys":[{"key":"vault://secret/bifrost/openai#api_key"}]`
2. Start/reload Bifrost
3. Send inference request through this provider

**Expected Result:**
- Bifrost resolves `vault://secret/bifrost/openai#api_key` to `sk-vault-test-openai-key123`
- Request sent to mock LLM with real resolved key in Authorization header
- `GET /api/providers/{id}` still shows masked key (not the resolved value)

**Status:** READY

---

### TC-012-002 — Vault Connection Config via API

**Priority:** P1 | **Type:** API

**Steps:**
1. `PUT /api/vault/config` with `admin_token`
2. Body:
```json
{
  "address": "http://localhost:8200",
  "auth_method": "token",
  "token": "dev-root",
  "kv_version": "v2",
  "mount_path": "secret"
}
```

**Expected Result:**
- HTTP 200
- Config saved (token stored encrypted)

**Status:** READY

---

### TC-012-003 — Vault Connectivity Test

**Priority:** P1 | **Type:** API

**Steps:**
1. `POST /api/vault/test` with `admin_token`

**Expected Result:**
- HTTP 200
- `{"status":"connected","vault_version":"1.15.x","seal_status":"unsealed"}`

**Status:** READY

---

### TC-012-004 — Invalid Vault Token Rejected at Config Time

**Priority:** P1 | **Type:** API

**Steps:**
1. `PUT /api/vault/config` with `token: "invalid-token-xyz"`
2. `POST /api/vault/test`

**Expected Result:**
- Test returns HTTP 200 with `{"status":"connection_failed","error":"permission denied"}`
- OR: Config save succeeds but test reveals auth failure

**Status:** READY

---

### TC-012-005 — AppRole Authentication Method

**Priority:** P1 | **Type:** Integration

**Preconditions:** AppRole configured in Vault: role_id + secret_id for Bifrost.

**Steps:**
1. Configure Vault: `auth_method=approle, role_id={...}, secret_id={...}`
2. Restart Bifrost
3. Send inference request using Vault-backed key

**Expected Result:**
- Bifrost authenticates with AppRole
- Vault token obtained and cached
- Inference succeeds with resolved key

**Status:** READY

---

### TC-012-006 — Vault Token Renewed Before Expiry

**Priority:** P1 | **Type:** Integration

**Preconditions:** Vault token with TTL = 60 seconds. Renewal daemon runs at TTL/2 = 30s.

**Steps:**
1. Watch for token renewal log message
2. Wait 45 seconds
3. Verify Bifrost still operational (key still resolved)

**Expected Result:**
- Log shows "Vault token renewed successfully" at ~30s
- Inference still works at 45s (token not expired)
- Token TTL reset to 60s after renewal

**Status:** READY

---

### TC-012-007 — Vault Secret Update Reflected Without Restart

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Vault has `api_key="sk-old-key"` at path
2. Bifrost loads and caches the old key
3. Update Vault: `vault kv put secret/bifrost/openai api_key="sk-new-key"`
4. Wait for Vault cache TTL (default 30s) or trigger refresh
5. Send inference request

**Expected Result:**
- Bifrost uses `sk-new-key` after cache expires
- No restart required

**Status:** READY

---

### TC-012-008 — Vault Unavailable — Fail Closed

**Priority:** P1 | **Type:** Integration

**Preconditions:** Vault started but then stopped mid-session.

**Steps:**
1. Provider using Vault-backed key
2. Stop Vault
3. Cache TTL expires (or purged manually)
4. Send inference request

**Expected Result:**
- Bifrost returns error: "unable to resolve Vault secret: connection refused"
- HTTP 503 or 502 returned to caller (fail-closed for security)
- Log: "Vault secret resolution failed — blocking request"

**Status:** READY

---

### TC-012-009 — Vault Secret Path Not Found

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Configure provider with `key="vault://secret/bifrost/nonexistent#api_key"`
2. Attempt inference request

**Expected Result:**
- Error: "Vault secret not found at path: secret/bifrost/nonexistent"
- HTTP 502

**Status:** READY

---

### TC-012-010 — Plain (Non-Vault) Keys Still Work Alongside Vault Keys

**Priority:** P1 | **Type:** Integration

**Steps:**
1. Provider "openai-a": key = Vault reference (resolved)
2. Provider "openai-b": key = plaintext "sk-plain-key-test"
3. Send requests to both providers

**Expected Result:**
- Both providers work
- No interference between Vault and non-Vault key resolution

**Status:** READY
