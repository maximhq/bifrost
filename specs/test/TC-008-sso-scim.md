# Test Cases — SSO / SCIM 2.0

**Suite ID:** TC-008  
**SRS Reference:** §3.16 (SSO-01→08, SCIM-01→05)  
**TR Reference:** TR-F-008  
**Priority:** P1  
**Type:** Integration + API + E2E  
**Dependency:** TC-013 (License), TC-004 (RBAC)

---

## Preconditions for All Tests
- Enterprise license with `sso_oidc`, `sso_saml`, `scim` features enabled
- WireMock IdP running at `http://localhost:8088`
- OIDC discovery configured at `http://localhost:8088/.well-known/openid-configuration`
- Valid SCIM bearer token: `scim_test_token_abc123`
- RSA key pair for ID token signing (test keypair, registered in WireMock JWKS)

---

## OIDC Test JWT Claims Template

```json
{
  "iss": "http://localhost:8088",
  "sub": "user_ext_001",
  "aud": "bifrost-client",
  "email": "alice@acme.com",
  "name": "Alice Smith",
  "groups": ["platform-team", "all-staff"],
  "exp": 9999999999
}
```

Group → Role mapping configured: `{"platform-team": "operator", "engineering": "admin"}`

---

### TC-008-001 — OIDC Login Flow Redirects to IdP

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-008.1

**Steps:**
1. `GET /api/sso/login?protocol=oidc` (no auth header)

**Expected Result:**
- HTTP 302 redirect
- Location header points to `http://localhost:8088/authorize?client_id=...&redirect_uri=...&state=...&nonce=...`
- `state` parameter is present (CSRF protection)

**Status:** READY

---

### TC-008-002 — OIDC Callback — Valid Code Exchange Creates Session

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-008.1

**Preconditions:** WireMock configured to return valid signed ID token on `POST /token`.

**Steps:**
1. Simulate OIDC callback: `GET /api/sso/callback?code=valid_auth_code&state={valid_state}`
2. Verify session is created

**Expected Result:**
- HTTP 302 redirect to `/` (dashboard)
- `Set-Cookie: bifrost_session={token}` in response
- Session in DB: `user_id` linked to external user "alice@acme.com"
- `role` = "operator" (mapped from "platform-team" group)

**Status:** READY

---

### TC-008-003 — OIDC ID Token Signature Verification

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-008.2

**Preconditions:** WireMock returns ID token signed with DIFFERENT RSA key (not registered in JWKS).

**Steps:**
1. Simulate OIDC callback with tampered ID token

**Expected Result:**
- HTTP 401 or redirect to error page
- Error: "ID token signature invalid"
- No session created

**Status:** READY

---

### TC-008-004 — OIDC ID Token Expiry Validation

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-008.2

**Preconditions:** WireMock returns ID token with `exp` = past timestamp.

**Steps:**
1. Simulate callback with expired token

**Expected Result:**
- Authentication rejected
- Error: "ID token expired"

**Status:** READY

---

### TC-008-005 — OIDC Group → Role Mapping

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-008.3

**Steps:**
1. User with groups: ["engineering", "all-staff"] completes OIDC login
2. Check assigned role

**Expected Result:**
- Role = "admin" (engineering → admin, higher priority than all-staff default)
- Not "viewer" (default for unmapped groups)

**Status:** READY

---

### TC-008-006 — OIDC CSRF Protection (Invalid State)

**Priority:** P1 | **Type:** Security

**Steps:**
1. `GET /api/sso/callback?code=valid_code&state=tampered_state_value`

**Expected Result:**
- HTTP 400
- Error: "invalid state parameter"
- No session created

**Status:** READY

---

### TC-008-007 — SAML SP Metadata Available

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-008.5

**Steps:**
1. `GET /api/sso/saml/metadata` (no auth required)

**Expected Result:**
- HTTP 200
- Content-Type: `application/xml`
- Valid SAML SP metadata XML with:
  - `EntityID` matching configured SP entity ID
  - `AssertionConsumerService` URL = `/api/sso/saml/acs`
  - SP X.509 certificate present

**Status:** READY

---

### TC-008-008 — SAML ACS Accepts Valid Assertion

**Priority:** P1 | **Type:** Integration  
**TR Reference:** TR-F-008.4

**Preconditions:** WireMock generates valid signed SAML response with test user attributes.

**Steps:**
1. `POST /api/sso/saml/acs` with `SAMLResponse={base64_encoded_assertion}`

**Expected Result:**
- HTTP 302 redirect to `/` (dashboard)
- Session created with correct email and role from attribute mapping

**Status:** READY

---

### TC-008-009 — SAML ACS Rejects Unsigned Assertion

**Priority:** P1 | **Type:** Security

**Steps:**
1. POST unsigned SAML assertion to `/api/sso/saml/acs`

**Expected Result:**
- HTTP 401
- Error: "SAML assertion signature invalid"

**Status:** READY

---

### TC-008-010 — SCIM POST /Users Provisions New User

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-008.6

**Steps:**
1. `POST /scim/v2/Users` with `Authorization: Bearer scim_test_token_abc123`
2. Body:
```json
{
  "schemas": ["urn:ietf:params:scim:schemas:core:2.0:User"],
  "userName": "bob@acme.com",
  "displayName": "Bob Builder",
  "emails": [{"value": "bob@acme.com", "primary": true}],
  "active": true,
  "groups": [{"value": "grp_engineering", "display": "Engineering"}]
}
```

**Expected Result:**
- HTTP 201
- Response includes `id` (Bifrost user ID)
- User created in `external_users` table with `role = admin` (engineering mapping)
- `GET /api/users/{id}` returns user (accessible by super_admin)

**Status:** READY

---

### TC-008-011 — SCIM DELETE /Users Deactivates User

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-008.7

**Steps:**
1. First provision user (TC-008-010)
2. `DELETE /scim/v2/Users/{scim_id}` with valid SCIM token

**Expected Result:**
- HTTP 204
- User `active` = false in DB (NOT deleted — audit trail preserved)
- All active sessions for this user invalidated
- Attempting inference with their session token → 401

**Status:** READY

---

### TC-008-012 — SCIM PATCH Updates Email

**Priority:** P1 | **Type:** API  
**TR Reference:** TR-F-008.8

**Steps:**
1. `PATCH /scim/v2/Users/{id}` with:
```json
{
  "schemas": ["urn:ietf:params:scim:api:messages:2.0:PatchOp"],
  "Operations": [{"op": "replace", "path": "emails[primary eq true].value", "value": "bob.new@acme.com"}]
}
```

**Expected Result:**
- HTTP 200
- User email updated to "bob.new@acme.com" in DB

**Status:** READY

---

### TC-008-013 — SCIM Invalid Bearer Token Returns 401

**Priority:** P1 | **Type:** Security  
**TR Reference:** TR-F-008.9

**Steps:**
1. `GET /scim/v2/Users` with `Authorization: Bearer invalid_token_xyz`

**Expected Result:**
- HTTP 401
- Body: SCIM error schema with `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"],"status":"401"}`

**Status:** READY

---

### TC-008-014 — SCIM ServiceProviderConfig Endpoint

**Priority:** P1 | **Type:** API

**Steps:**
1. `GET /scim/v2/ServiceProviderConfig` with valid SCIM token

**Expected Result:**
- HTTP 200
- SCIM ServiceProviderConfig object with supported features
- `patch.supported` = true
- `filter.supported` = true
- `etag.supported` = false (if not implemented)

**Status:** READY

---

### TC-008-015 — JIT Provisioning — New SSO User Auto-Created

**Priority:** P1 | **Type:** Integration

**Preconditions:** User "charlie@acme.com" does NOT exist in external_users table.

**Steps:**
1. Charlie completes OIDC login successfully
2. Check `external_users` table

**Expected Result:**
- User "charlie@acme.com" auto-created (JIT provisioning)
- `provisioned_via` = "oidc"
- Session created and valid

**Status:** READY

---

### TC-008-016 — SSO Login E2E (Browser Flow)

**Priority:** P1 | **Type:** E2E

**Steps:**
1. Navigate to Bifrost UI login page
2. Click "Login with SSO" button
3. Browser redirects to WireMock IdP
4. WireMock auto-submits login form (test credentails)
5. Browser returns to Bifrost dashboard

**Expected Result:**
- User is logged in
- Username displayed in sidebar
- Role badge shows "Operator" (based on group mapping)

**Status:** READY

---

### TC-008-017 — SSO Config API — Group → Role Mapping Update

**Priority:** P1 | **Type:** API

**Steps:**
1. `PUT /api/sso/config` with `super_admin_token`
2. Update `group_role_mapping`: add `{"data-team": "viewer"}`
3. Login user who is member of "data-team" group

**Expected Result:**
- User gets "viewer" role after mapping update
- Previous users unaffected (mapping change is forward-looking for new sessions)

**Status:** READY

---

### TC-008-018 — SCIM Token Create and Revoke

**Priority:** P1 | **Type:** API

**Steps:**
1. `POST /api/scim/tokens` with `super_admin_token` → returns new token (shown once)
2. Use token to `GET /scim/v2/Users` → 200
3. `DELETE /api/scim/tokens/{id}` → 204
4. Attempt `GET /scim/v2/Users` with revoked token → 401

**Expected Result:**
- Token lifecycle works correctly

**Status:** READY

---

### TC-008-019 — SSO Logout Invalidates Session

**Priority:** P1 | **Type:** API

**Steps:**
1. Login via SSO → get session cookie
2. `POST /api/sso/logout` with session cookie
3. Use old session cookie for API call

**Expected Result:**
- Logout: HTTP 200
- Post-logout API call: HTTP 401 (session invalidated)

**Status:** READY

---

### TC-008-020 — SSO Requires Enterprise License

**Priority:** P1 | **Type:** Integration

**Preconditions:** Community license (no BIFROST_LICENSE_KEY).

**Steps:**
1. `GET /api/sso/login?protocol=oidc`

**Expected Result:**
- HTTP 402 or redirect to upgrade page
- Not a redirect to IdP

**Status:** READY
