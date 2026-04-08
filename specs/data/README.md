# Test Data Catalog — Bifrost v2.0 Enterprise

**Version:** 1.0 | **Date:** 2026-04-08  
**Reference:** TD-001 (Test Design), TEP-001 (Execution Plan)  
**Status:** READY

---

## Directory Structure

```
specs/data/
├── README.md                          ← This file
├── seed/                              ← SQL seed scripts (ordered)
│   ├── 00-schema-extensions.sql      ← Enterprise tables
│   ├── 01-users-sessions.sql         ← Test users + static tokens
│   ├── 02-providers.sql              ← Mock providers
│   ├── 03-virtual-keys.sql           ← VK fixtures
│   ├── 04-rbac.sql                   ← Roles, permissions, assignments
│   └── 05-guardrails-pii.sql         ← Policies
├── fixtures/                          ← JSON/YAML fixture files
│   ├── sessions.json                  ← Auth tokens per role
│   ├── virtual-keys.json              ← VK configurations
│   ├── providers.json                 ← Provider configs
│   ├── rbac-roles.json                ← Role definitions
│   ├── guardrail-policies.json        ← Guardrail test policies
│   ├── pii-samples.json               ← PII test strings + expected output
│   ├── scim-users.json                ← SCIM user payloads
│   ├── scim-groups.json               ← SCIM group payloads
│   └── alert-channels.json            ← Alert channel configs
├── licenses/                          ← Test JWT license files
│   ├── README.md                      ← How JWTs were generated
│   ├── test-private-key.pem           ← RSA-2048 test private key (TEST USE ONLY)
│   ├── test-public-key.pem            ← RSA-2048 test public key
│   ├── enterprise.jwt                 ← Valid enterprise license
│   ├── pro.jwt                        ← Valid pro license
│   ├── trial.jwt                      ← Enterprise trial (25 days)
│   ├── expired.jwt                    ← Expired license (2020-01-01)
│   └── tampered.jwt                   ← Invalid signature
├── mock-llm/                          ← Mock LLM server fixtures
│   ├── chat-completion.json           ← Non-streaming response
│   ├── chat-completion-stream.sse     ← SSE streaming response
│   ├── embeddings.json                ← Embedding response
│   ├── moderation-safe.json           ← Moderation score: 0.05
│   ├── moderation-unsafe.json         ← Moderation score: 0.95
│   └── transcription.json             ← Transcription response
├── wiremock/                          ← WireMock stubs (OIDC/SAML/Webhooks)
│   ├── mappings/
│   │   ├── oidc-discovery.json
│   │   ├── oidc-token.json
│   │   ├── oidc-jwks.json
│   │   ├── oidc-userinfo.json
│   │   ├── saml-metadata.json
│   │   └── webhook-capture.json
│   └── __files/
│       ├── oidc-discovery.json        ← Discovery document content
│       └── jwks.json                  ← JWKS key set content
├── pii/                               ← PII detection test cases
│   ├── detection-cases.json           ← Input → expected entities
│   └── redaction-cases.json           ← Input → expected redacted output
├── performance/                       ← k6 test scripts
│   ├── k6-baseline.js                 ← 1,000 RPS steady
│   ├── k6-ramp.js                     ← Ramp to 5,000 RPS
│   ├── k6-spike.js                    ← Spike load test
│   ├── k6-streaming.js                ← 1,000 concurrent streams
│   └── k6-config.json                 ← Shared k6 config
└── vault/                             ← Vault test data
    ├── vault-init.sh                  ← Vault dev setup script
    └── secrets.json                   ← Test secrets to seed
```

---

## Data Generation Strategy

| Category | Method | Refresh Frequency |
|----------|--------|------------------|
| SQL seeds | Static SQL, version-controlled | Per major schema change |
| JSON fixtures | Static, hand-crafted | Per feature change |
| License JWTs | Generated with test RSA keypair | One-time (keys pinned) |
| Mock LLM responses | Static JSON fixtures | Per API schema change |
| WireMock stubs | Static JSON mappings | Per IdP protocol change |
| PII test cases | Curated dataset (no real PII) | As new entity types added |
| k6 scripts | Parameterized JS | Per load target change |
| Vault secrets | Shell script (dev Vault only) | Per test environment rebuild |

---

## Usage

```bash
# 1. Start infrastructure
docker-compose -f tests/docker-compose.yml up -d

# 2. Run seed scripts in order
for f in specs/data/seed/*.sql; do
  psql "$BIFROST_DB_URL" -f "$f"
done

# 3. Seed Vault
bash specs/data/vault/vault-init.sh

# 4. Set test license
export BIFROST_LICENSE_KEY=$(cat specs/data/licenses/enterprise.jwt)

# 5. Run tests
go test ./tests/integration/... -timeout 15m
```

---

## Security Notice

> ⚠️ **TEST DATA ONLY** — All keys, tokens, and credentials in this directory are for **testing purposes only**. They MUST NOT be used in any production environment. The RSA keypair in `licenses/` is publicly known (committed to this repo) and provides zero security.
