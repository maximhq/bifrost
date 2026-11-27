#!/usr/bin/env bash
set -euo pipefail

# Configure AWS CLI for R2 uploads
# Usage: ./configure-r2.sh

echo "⚙️ Configuring AWS CLI for R2..."

# Publishing must never execute freshly resolved package code with release
# credentials present. Provision AWS CLI v2 in the runner image instead.
if ! command -v aws >/dev/null 2>&1 || [[ "$(aws --version 2>&1)" != aws-cli/2.* ]]; then
  echo "AWS CLI v2 must be provisioned before entering the publishing job" >&2
  exit 1
fi
umask 077

# Clean and trim environment variables (removing any whitespace). A secret
# pasted with a trailing newline would otherwise fail the strict endpoint
# match below and stop every publisher job; upload-to-r2.sh trims the same way.
R2_ENDPOINT="$(printf '%s' "${R2_ENDPOINT:-}" | tr -d '[:space:]')"
R2_ACCESS_KEY_ID="$(printf '%s' "${R2_ACCESS_KEY_ID:-}" | tr -d '[:space:]')"
R2_SECRET_ACCESS_KEY="$(printf '%s' "${R2_SECRET_ACCESS_KEY:-}" | tr -d '[:space:]')"
R2_BUCKET="${R2_BUCKET:-prod-downloads}"

# Validate environment variables
if [ -z "$R2_ENDPOINT" ] || [ -z "$R2_ACCESS_KEY_ID" ] || [ -z "$R2_SECRET_ACCESS_KEY" ]; then
  echo "❌ Missing required R2 credentials"
  exit 1
fi

if [[ ! "$R2_ENDPOINT" =~ ^https://[a-z0-9.-]+\.r2\.cloudflarestorage\.com$ ]]; then
  echo "R2_ENDPOINT must be an HTTPS Cloudflare R2 account endpoint" >&2
  exit 1
fi
if [[ ! "$R2_BUCKET" =~ ^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$ ]]; then
  echo "Invalid R2 bucket name" >&2
  exit 1
fi

# Configure AWS CLI for R2 using dedicated profile
aws configure set --profile R2 aws_access_key_id "$R2_ACCESS_KEY_ID"
aws configure set --profile R2 aws_secret_access_key "$R2_SECRET_ACCESS_KEY"
aws configure set --profile R2 region us-east-1
aws configure set --profile R2 s3.signature_version s3v4

# Test connection
echo "🔍 Testing R2 connection..."
aws s3api head-bucket --bucket "$R2_BUCKET" --endpoint-url "$R2_ENDPOINT" --profile R2 >/dev/null
echo "✅ R2 connection successful"
