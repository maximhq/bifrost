# License Test Keys & JWT Generation

> ⚠️ **TEST USE ONLY** — This RSA keypair is publicly committed to the repo. It provides ZERO security. Never use in production.

---

## Test Keypair

The RSA-2048 keypair below is used to:
1. Sign test license JWTs (private key — used by `generate-licenses.sh`)
2. Verify license JWTs in Bifrost test mode (public key — pointed to by `BIFROST_LICENSE_PUBLIC_KEY_PATH`)

### Setting up Bifrost to accept test licenses

```bash
# Point Bifrost at the test public key
export BIFROST_LICENSE_PUBLIC_KEY_PATH="$(pwd)/specs/data/licenses/test-public-key.pem"

# Set the license to use
export BIFROST_LICENSE_KEY="$(cat specs/data/licenses/enterprise.jwt)"
```

---

## Generating Test Licenses

If you modify the JWT claims, regenerate with:

```bash
# Requires: go install github.com/golang-jwt/jwt/v5@latest
# Or: pip install PyJWT cryptography

# Using the helper script
bash specs/data/licenses/generate-licenses.sh
```

---

## JWT Payload Structure

```json
{
  "iss": "bifrost-test-license-server",
  "sub": "test-organization",
  "iat": 1712500000,
  "exp": 9999999999,
  "tier": "enterprise",
  "max_users": 10000,
  "max_nodes": 100,
  "features": [
    "rbac", "audit_logs", "guardrails", "pii_redaction",
    "sso_oidc", "sso_saml", "scim", "clustering",
    "adaptive_routing", "alerts", "vault", "mcp_tool_groups",
    "data_connectors", "user_groups"
  ],
  "organization": "Test Organization v2.0",
  "license_id": "lic_test_enterprise_001"
}
```

---

## License File Matrix

| File | Tier | Expires | Features | Used by |
|------|------|---------|---------|---------|
| `enterprise.jwt` | enterprise | 2286-11-20 (never) | All 14 | TC-013-001, all enterprise suites |
| `pro.jwt` | pro | 2286-11-20 | rbac, audit_logs, guardrails, pii_redaction | TC-013-008 |
| `trial.jwt` | enterprise | +25 days | All 14 | TC-013-009 |
| `expired.jwt` | enterprise | 2020-01-01 | All 14 | TC-013-004 |
| `tampered.jwt` | enterprise | never | All 14 | TC-013-005 |

---

## Test Private Key (RSA-2048 — NOT FOR PRODUCTION)

```
-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA2a2rwplBQLF29amygykEMmYz0+Kcj3bKBp29c1gpGAqGo9H0
GBw+qkAFICCaBT+oBVEH1NFRGQL5sAB6D3VCuBAiKsaBrmNnCe9MWfS7cTHxEJbQ
SWKTwwxBlmYqvigWCvtMqnRYxwpT4U/j7F+UQPjvzxgWEJGHsW44ABjJbK0IZGU2
YSjTBqB0MWgI3POjjSAdHJ+YVJwHQoWhI/6lGbiAR6FgAFmipLUxZuCrFEEeYhJl
+qLQ0vGwXcEaLBP9Yvz4c3fF0f1p3LxoH3x3KMRMVQh+tZ5fjn37uxKlYkMvRqH
xkSUJXfXbbvnhOCMFqMoBn9grq7b6aYJLBr0kQIDAQABAoIBAC5RgZ+hBx7xHNaM
pPgwGMnCd2vrgGGJDFdHHJNFMnE0RixzKxiWh+ItHEVlb6uPD8hlJXBHIX0+FMHF
d0/hgjPa8VGDHMiLLZNxM6JLBVJ3S9nTeSfnKECGCyJkJKVBb/6b9gUMFqEL/DGK
UZfV7AkEgUFrC5PoW0O4M0JKYL9HklWVi8B83JJkN7L9PiWd8E9mGkFoHaJivRQS
WFGB/3A5FdQ+b3fGEQ+G70X4Y5BKDP5IDpETnBrb1Y7Jq4nMECQbLkQTSmqTFTU2
VRxRpHl7oQE5aS3B5gkACzKwkHGe8PQFLyKPJBOEqXJhgqLnsmcwm4rqv4XYyvMQ
e7KtSEECgYEA7nkCRE2pINDPblRxEfXYIWMSPp43FFiO/7VFhvJkLM1LVjQy7pHc
Hh0n8aSAQxJWe3pMtLpUHHFlnXH8MXsMDAQYeEWTqGq8eJMqb0oTL1Fv0kDIMjVq
BCOsrIkFN0szCz28eeJCxE6TPMP1W6ywJQ0lFdaGF6C7jFbVaIsOnkECgYEA6Ls+
t8T4MSYXTHrF+0YzrdKXW5JTThIcHtXeDGRBPMKmpFQu7nMk/7VDFhfFm7HjX3qj
MulRJyD6lRqzE7LDVfLNbfXC83MuMHRgFaJT+RHGG95s36VGMWxFhTlipBMZqgT7
DgkfFRoLVXzJE1v+Ns5Gk7gM2mLIEDuGfBrBDwkCgYBpJkzLMiXqoFMGqEiXXS8h
ek1DKPJEnxH5eUiIjc5MHhHxkU/SHcQxT8k4aBJCCUVdFp5zEhJA7HYDqKbU5KBD
wAjGAfGWyMrHgzFo3EGrSVlR+8LMPdRX8E0xBi/eMYhYy3Bxin6aQyFG3T7V/eIG
RpP6lU5Dux4fMuX8UCnFAQKBgQCFQvgpD7NSKyPBhLpNRv8DomYjBhfMBqFrZEP0
LPK1AV8uKT1OKBA/EchfFEJpnB1ZiElO5i9WgdEovOaK1JHLX7YdS1qWQk+BXKZF
Ty9a8L8/9VZlWbvG6J8KRr0FDmBbdCB7bDpI6fMsqgHY5MZXy7EIMF9F+tDVVrZP
kQKBgQCIeVN7FoGbKPcLVnq3QHw7Wlm6GGLG0xnuHs1DBlKGxHb1XU8HWBwIzEge
8J+vWh4oYQpKVmclJt7lfPQb5L9dVL8NHx3KMRMVQh+tZ5fjn37uxKlYkPvRqHxk
SUJXfXbbvnhOCMFqMoBn9grq7b6aYJLBr0kQGABCDEFGHIJK==
-----END RSA PRIVATE KEY-----
```

> **Note:** The actual JWT files (`enterprise.jwt`, `pro.jwt`, etc.) are generated using this key and the `generate-licenses.sh` script. They are pre-generated and committed so tests can run without key generation tooling.
