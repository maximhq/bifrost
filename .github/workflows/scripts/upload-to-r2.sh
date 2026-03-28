#!/usr/bin/env bash
set -euo pipefail

# Upload builds to R2 with retry logic
# Usage: ./upload-to-r2.sh <transport-version>
#
# Environment variables:
#   R2_ENDPOINT          - Required. R2 endpoint URL
#   R2_BUCKET            - Required. R2 bucket name
#   R2_AWS_PROFILE       - Optional. AWS CLI profile (default: R2)
#   SKIP_LATEST_UPLOAD   - Optional. Set to "true" to skip latest/ upload
#                          (used when multiple jobs upload in parallel and
#                          the finalize job handles latest/ separately)

if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <transport-version> (e.g., transports/v1.2.3)"
  exit 1
fi
TRANSPORT_VERSION="$1"
if [[ ! -d "./dist" ]]; then
  echo "❌ ./dist not found. Build artifacts must be present before upload."
  exit 1
fi
: "${R2_ENDPOINT:?R2_ENDPOINT env var is required}"
: "${R2_BUCKET:?R2_BUCKET env var is required}"

# Strip 'transports/' prefix from version
VERSION_ONLY=${TRANSPORT_VERSION#transports/v}
CLI_VERSION="v${VERSION_ONLY}"
R2_ENDPOINT="$(echo "$R2_ENDPOINT" | tr -d '[:space:]')"

echo "📤 Uploading binaries for version: $CLI_VERSION"

# Function to upload with retry
# Uses aws s3 cp --recursive instead of s3 sync --delete so that
# parallel upload jobs don't wipe each other's files.
upload_with_retry() {
  local source_path="$1"
  local dest_path="$2"
  local max_retries=3

  for attempt in $(seq 1 $max_retries); do
    echo "🔄 Attempt $attempt/$max_retries: Uploading to $dest_path"

    if aws s3 cp "$source_path" "$dest_path" \
       --endpoint-url "$R2_ENDPOINT" \
       --profile "${R2_AWS_PROFILE:-R2}" \
       --no-progress \
       --recursive; then
      echo "✅ Upload successful to $dest_path"
      return 0
    else
      echo "⚠️ Attempt $attempt failed"
      if [ $attempt -lt $max_retries ]; then
        delay=$((2 ** attempt))
        echo "🕐 Waiting ${delay}s before retry..."
        sleep $delay
      fi
    fi
  done

  echo "❌ All $max_retries attempts failed for $dest_path"
  return 1
}

# Upload to versioned path
if ! upload_with_retry "./dist/" "s3://$R2_BUCKET/bifrost/$CLI_VERSION/"; then
  exit 1
fi

# Skip latest/ upload if requested (finalize job handles it after all builds complete)
if [[ "${SKIP_LATEST_UPLOAD:-false}" == "true" ]]; then
  echo "⏭️ Skipping latest/ upload (will be handled by finalize job)"
  echo "🎉 Binaries uploaded successfully to R2 (versioned path)"
  exit 0
fi

# Check if this is a prerelease version (semver: presence of a hyphen denotes pre-release)
if [[ "$CLI_VERSION" == *-* ]]; then
  echo "🔍 Detected prerelease version: $CLI_VERSION"
  echo "⏭️ Skipping upload to latest/ for prerelease"
else
  echo "🔍 Detected stable release: $CLI_VERSION"

  # Small delay between uploads (configurable; default 2s)
  sleep "${INTER_UPLOAD_SLEEP_SECONDS:-2}"

  # Upload to latest path
  echo "📤 Uploading to latest/"
  if ! upload_with_retry "./dist/" "s3://$R2_BUCKET/bifrost/latest/"; then
    exit 1
  fi
fi

echo "🎉 All binaries uploaded successfully to R2"
