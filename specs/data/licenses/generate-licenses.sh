#!/usr/bin/env bash
# ============================================================
# Generate Test License JWTs
# Uses test RSA-2048 private key (NOT for production)
# Requires: openssl + python3 + PyJWT
#   pip install PyJWT cryptography
# ============================================================

set -euo pipefail
DIR="$(dirname "$0")"

# Generate RSA keypair if not present
if [ ! -f "$DIR/test-private-key.pem" ]; then
  echo "==> Generating RSA-2048 test keypair..."
  openssl genrsa -out "$DIR/test-private-key.pem" 2048
  openssl rsa -in "$DIR/test-private-key.pem" -pubout -out "$DIR/test-public-key.pem"
  echo "    ✓ Keypair generated"
fi

PRIVATE_KEY=$(cat "$DIR/test-private-key.pem")
NOW=$(date +%s)
FAR_FUTURE=9999999999   # ~2286-11-20
NEAR_FUTURE=$(( NOW + 25 * 24 * 3600 ))  # 25 days from now (trial)

# Common features
ALL_FEATURES='["rbac","audit_logs","guardrails","pii_redaction","sso_oidc","sso_saml","scim","clustering","adaptive_routing","alerts","vault","mcp_tool_groups","data_connectors","user_groups"]'
PRO_FEATURES='["rbac","audit_logs","guardrails","pii_redaction"]'

generate_jwt() {
  local OUTPUT_FILE="$1"
  local PAYLOAD="$2"

  python3 - <<PYEOF
import jwt
import json

private_key = """$PRIVATE_KEY"""
payload = $PAYLOAD

token = jwt.encode(
    payload,
    private_key,
    algorithm="RS256",
    headers={"kid": "test-key-01"}
)
with open("$OUTPUT_FILE", "w") as f:
    f.write(token)
print(f"    ✓ Generated $OUTPUT_FILE")
PYEOF
}

echo ""
echo "==> Generating enterprise.jwt (tier=enterprise, exp=never)..."
generate_jwt "$DIR/enterprise.jwt" "{
    'iss': 'bifrost-test-license-server',
    'sub': 'test-organization',
    'iat': $NOW,
    'exp': $FAR_FUTURE,
    'tier': 'enterprise',
    'max_users': 10000,
    'max_nodes': 100,
    'features': $ALL_FEATURES,
    'organization': 'Test Organization v2.0',
    'license_id': 'lic_test_enterprise_001'
}"

echo "==> Generating pro.jwt (tier=pro, subset features)..."
generate_jwt "$DIR/pro.jwt" "{
    'iss': 'bifrost-test-license-server',
    'sub': 'test-org-pro',
    'iat': $NOW,
    'exp': $FAR_FUTURE,
    'tier': 'pro',
    'max_users': 100,
    'max_nodes': 3,
    'features': $PRO_FEATURES,
    'organization': 'Test Org Pro',
    'license_id': 'lic_test_pro_002'
}"

echo "==> Generating trial.jwt (enterprise features, +25 days)..."
generate_jwt "$DIR/trial.jwt" "{
    'iss': 'bifrost-test-license-server',
    'sub': 'test-org-trial',
    'iat': $NOW,
    'exp': $NEAR_FUTURE,
    'tier': 'enterprise',
    'trial': True,
    'max_users': 10000,
    'max_nodes': 100,
    'features': $ALL_FEATURES,
    'organization': 'Trial Organization',
    'license_id': 'lic_test_trial_003'
}"

echo "==> Generating expired.jwt (exp=2020-01-01)..."
generate_jwt "$DIR/expired.jwt" "{
    'iss': 'bifrost-test-license-server',
    'sub': 'test-org-expired',
    'iat': 1000000000,
    'exp': 1577836800,
    'tier': 'enterprise',
    'features': $ALL_FEATURES,
    'organization': 'Expired Organization',
    'license_id': 'lic_test_expired_004'
}"

echo "==> Generating tampered.jwt (valid structure but invalid signature)..."
VALID_JWT=$(cat "$DIR/enterprise.jwt")
# Tamper: flip last character of signature
TAMPERED="${VALID_JWT::-1}X"
echo -n "$TAMPERED" > "$DIR/tampered.jwt"
echo "    ✓ Generated tampered.jwt"

echo ""
echo "==> All license JWTs generated successfully!"
echo ""
echo "Test commands:"
echo "  export BIFROST_LICENSE_KEY=\$(cat specs/data/licenses/enterprise.jwt)"
echo "  export BIFROST_LICENSE_PUBLIC_KEY_PATH=specs/data/licenses/test-public-key.pem"
