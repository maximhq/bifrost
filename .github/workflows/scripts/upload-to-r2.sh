#!/bin/bash
set -euo pipefail

# Upload builds to R2 with retry logic
# Usage: ./upload-to-r2.sh <transport-version>

TRANSPORT_VERSION="$1"

# Strip 'transports/' prefix from version
VERSION_ONLY=${TRANSPORT_VERSION#transports/v}
CLI_VERSION="v${VERSION_ONLY}"
R2_ENDPOINT="$(echo "$R2_ENDPOINT" | tr -d '[:space:]')"

echo "📤 Uploading binaries for version: $CLI_VERSION"

# Function to upload with retry
upload_with_retry() {
  local source_path="$1"
  local dest_path="$2"
  local max_retries=3
  
  for attempt in $(seq 1 $max_retries); do
    echo "🔄 Attempt $attempt/$max_retries: Uploading to $dest_path"
    
    if aws s3 sync "$source_path" "$dest_path" \
       --endpoint-url "$R2_ENDPOINT" \
       --no-progress \
       --delete; then
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
if ! upload_with_retry "./dist/" "s3://prod-downloads/bifrost/$CLI_VERSION/"; then
  exit 1
fi

# Small delay between uploads
sleep 2

# Upload to latest path
if ! upload_with_retry "./dist/" "s3://prod-downloads/bifrost/latest/"; then
  exit 1
fi

echo "🎉 All binaries uploaded successfully to R2"
