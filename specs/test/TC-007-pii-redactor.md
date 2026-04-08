# Test Cases — PII Detection & Redaction

**Suite ID:** TC-007  
**SRS Reference:** §3.15 (PII-01 → PII-10)  
**TR Reference:** TR-F-007  
**Priority:** P0  
**Type:** Integration + Unit + API  
**Dependency:** TC-013 (License)

---

## Preconditions for All Tests
- Enterprise license with `pii_redactor` feature enabled
- Plugin order: piiredactor runs BEFORE logging plugin
- PII plugin configured with all standard entity types enabled

---

## Known PII Test Strings (from TD-001)

| Entity Type | Valid Sample | Invalid Sample (must NOT flag) |
|-------------|-------------|-------------------------------|
| EMAIL | `user@example.com` | `not-an-email` |
| PHONE | `+1-800-555-0199` | `1234` |
| SSN | `123-45-6789` | `12-3456-789` (wrong format) |
| CREDIT_CARD | `4532015112830366` (valid Luhn) | `1234567890123456` (invalid Luhn) |
| IP_ADDRESS | `192.168.1.100` | `999.999.999.999` |

---

### TC-007-001 — Email Address Detected and Redacted in Request

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-007.1

**Preconditions:** PII plugin scope=request, mode=mask, EMAIL entity enabled.

**Steps:**
1. `POST /v1/chat/completions` with content "My email is user@example.com, help me."
2. Verify what the mock LLM actually receives

**Expected Result:**
- LLM receives: "My email is [REDACTED_EMAIL], help me."
- HTTP 200 returned to caller
- Log store shows redacted content (not original)

**Status:** READY

---

### TC-007-002 — Phone Number Detected and Masked

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-007.2

**Steps:**
1. Request content: "Call me at +1-800-555-0199 tomorrow"

**Expected Result:**
- Provider receives: "Call me at [REDACTED_PHONE] tomorrow"

**Status:** READY

---

### TC-007-003 — SSN Detected and Masked

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-007.2

**Steps:**
1. Request content: "My SSN is 123-45-6789"

**Expected Result:**
- Provider receives: "My SSN is [REDACTED_SSN]"

**Status:** READY

---

### TC-007-004 — Valid Credit Card Detected (Luhn Pass)

**Priority:** P0 | **Type:** Integration + Unit  
**TR Reference:** TR-F-007.7

**Steps:**
1. Unit: `Luhn.Check("4532015112830366")` = true
2. Integration: Request with "card: 4532015112830366" → provider sees "[REDACTED_CREDIT_CARD]"

**Expected Result:**
- Valid CC flagged and redacted

**Status:** READY

---

### TC-007-005 — Invalid Credit Card NOT Detected (Luhn Fail)

**Priority:** P0 | **Type:** Unit  
**TR Reference:** TR-F-007.7

**Steps:**
1. Unit: `Luhn.Check("1234567890123456")` = false
2. Integration: Request with "card: 1234567890123456" → provider sees original number

**Expected Result:**
- Invalid Luhn number NOT flagged (false positive prevention)

**Status:** READY

---

### TC-007-006 — PII Not Stored in Log Store (Critical)

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-007.6

**Steps:**
1. Send `POST /v1/chat/completions` with `"content": "Email me at private@email.com"`
2. After response, query log store: `GET /api/logs` for that request
3. Inspect `request_body` field in log entry

**Expected Result:**
- Log `request_body` shows `"private@email.com"` replaced with `"[REDACTED_EMAIL]"`
- Original email does NOT appear anywhere in log database

**Status:** READY

---

### TC-007-007 — PII in LLM Response is Redacted

**Priority:** P0 | **Type:** Integration

**Preconditions:** PII plugin scope includes "response". Mock LLM returns "Contact John at john@corp.com"

**Steps:**
1. Send any chat completion
2. Mock LLM response contains email address

**Expected Result:**
- Response content returned to caller: "Contact John at [REDACTED_EMAIL]"
- Caller never sees original email

**Status:** READY

---

### TC-007-008 — Mask Mode Format is Correct

**Priority:** P0 | **Type:** Unit  
**TR Reference:** TR-F-007.3

**Steps:**
1. Unit test: `Redactor.RedactText("user@example.com", mode=mask)` for email entity

**Expected Result:**
- Returns `[REDACTED_EMAIL]` (exact string, uppercase entity type)
- No original email characters remain

**Status:** READY

---

### TC-007-009 — Hash Mode is Deterministic

**Priority:** P0 | **Type:** Unit  
**TR Reference:** TR-F-007.4

**Steps:**
1. Call `Redactor.RedactText("user@example.com", mode=hash)` twice

**Expected Result:**
- Both calls return identical `[HASH_EMAIL_xxxxxxxxxxxx]` string
- Hash is 12-char hex prefix of SHA-256

**Status:** READY

---

### TC-007-010 — Tokenize Mode Produces Reversible Token

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-007.5

**Preconditions:** PII plugin configured with mode=tokenize. KVStore (Redis) available.

**Steps:**
1. Send request with email "private@test.com"
2. Provider receives "PII_EMAIL_{uuid}" token
3. Call `POST /api/pii/detokenize` with `{"token": "PII_EMAIL_{uuid}"}`

**Expected Result:**
- Provider sees token (not original email)
- Detokenize API returns `{"original": "private@test.com"}`

**Status:** READY

---

### TC-007-011 — Custom Regex PII Pattern Applied

**Priority:** P0 | **Type:** Integration  
**TR Reference:** TR-F-007.8

**Preconditions:** Custom PII pattern: `EMP-\d{6}` (employee ID pattern), mode=mask.

**Steps:**
1. Request content: "Employee EMP-123456 has been assigned"

**Expected Result:**
- Provider receives: "Employee [REDACTED_CUSTOM] has been assigned"

**Status:** READY

---

### TC-007-012 — Multiple PII Entities in Single Message — All Redacted

**Priority:** P0 | **Type:** Integration

**Steps:**
1. Request: "Name: John Doe, Email: john@test.com, Phone: 555-1234, SSN: 123-45-6789"

**Expected Result:**
- All detected entities ([REDACTED_EMAIL], [REDACTED_PHONE], [REDACTED_SSN]) replaced
- Positions don't overlap or corrupt surrounding text

**Status:** READY

---

### TC-007-013 — PII Plugin Dry-Run Test API

**Priority:** P1 | **Type:** API

**Steps:**
1. `POST /api/pii/test` with `admin_token`
2. Body: `{"text": "Call me at 555-1234", "mode": "mask"}`

**Expected Result:**
- HTTP 200
- `{"redacted": "Call me at [REDACTED_PHONE]", "entities": [{"type":"PHONE","value":"555-1234","start":10,"end":18}]}`

**Status:** READY

---

### TC-007-014 — PII Detection Config Update (Enable/Disable Entity Types)

**Priority:** P1 | **Type:** API

**Steps:**
1. Disable EMAIL entity via `PUT /api/pii/config` — set EMAIL enabled=false
2. Send request with email address
3. Verify email NOT redacted (entity disabled)
4. Re-enable EMAIL — verify it's redacted again

**Expected Result:**
- Dynamic enable/disable works without restart

**Status:** READY
