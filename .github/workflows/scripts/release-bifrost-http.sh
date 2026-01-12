#!/usr/bin/env bash
set -euo pipefail

# Release bifrost-http component
# Usage: ./release-bifrost-http.sh <version>

# Get the absolute path of the script directory
# Use readlink if available (Linux), otherwise use cd/pwd (macOS compatible)
if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi

# Source Go utilities for exponential backoff
source "$SCRIPT_DIR/go-utils.sh"

# Validate input argument
if [ "${1:-}" = "" ]; then
  echo "Usage: $0 <version>" >&2
  exit 1
fi

VERSION="$1"
TAG_NAME="transports/v${VERSION}"

echo "🚀 Releasing bifrost-http v$VERSION..."

# Get core and framework versions from version files
CORE_VERSION="v$(tr -d '\n\r' < core/version)"
FRAMEWORK_VERSION="v$(tr -d '\n\r' < framework/version)"

echo "🔍 DEBUG: CORE_VERSION: $CORE_VERSION"
echo "🔍 DEBUG: FRAMEWORK_VERSION: $FRAMEWORK_VERSION"


# Get plugin versions from version files
echo "🔌 Getting plugin versions from version files..."
declare -A PLUGIN_VERSIONS

# Get versions for plugins that exist in the plugins/ directory
for plugin_dir in plugins/*/; do
  if [ -d "$plugin_dir" ]; then
    plugin_name=$(basename "$plugin_dir")
    PLUGIN_VERSION="v$(tr -d '\n\r' < "${plugin_dir}version")"
    PLUGIN_VERSIONS["$plugin_name"]="$PLUGIN_VERSION"
    echo "   📦 $plugin_name: $PLUGIN_VERSION (from version file)"
  fi
done

# Also check for any plugins already in transport go.mod that might not be in plugins/ directory
cd transports
echo "🔍 Checking for additional plugins in transport go.mod..."
# Parse go.mod plugin lines and add missing ones
while IFS= read -r plugin_line; do
  plugin_name=$(echo "$plugin_line" | awk -F'/' '{print $NF}' | awk '{print $1}')
  current_version=$(echo "$plugin_line" | awk '{print $NF}')

  # Only add if we don't already have this plugin
  if [[ -z "${PLUGIN_VERSIONS[$plugin_name]:-}" ]]; then
    echo "   📦 $plugin_name: $current_version (from transport go.mod)"
    PLUGIN_VERSIONS["$plugin_name"]="$current_version"
  fi
done < <(grep "github.com/maximhq/bifrost/plugins/" go.mod)
cd ..

echo "🔧 Using versions:"
echo "   Core: $CORE_VERSION"
echo "   Framework: $FRAMEWORK_VERSION"
echo "   Plugins:"
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  echo "     - $plugin_name: ${PLUGIN_VERSIONS[$plugin_name]}"
done

# Update transport dependencies to use plugin versions from version files
echo "🔧 Using plugin versions from version files for transport..."
PLUGINS_USED=()

# Track which plugins are actually used by the transport
cd transports
for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
  plugin_version="${PLUGIN_VERSIONS[$plugin_name]}"

  # Check if transport depends on this plugin
  if grep -q "github.com/maximhq/bifrost/plugins/$plugin_name" go.mod; then
    echo "  📦 Using $plugin_name plugin $plugin_version"
    go_get_with_backoff "github.com/maximhq/bifrost/plugins/$plugin_name@$plugin_version"
    PLUGINS_USED+=("$plugin_name:$plugin_version")
  fi
done

# Also ensure core and framework are up to date

echo "  🔧 Updating core to $CORE_VERSION"
go_get_with_backoff "github.com/maximhq/bifrost/core@$CORE_VERSION"

echo "  📦 Updating framework to $FRAMEWORK_VERSION"
go_get_with_backoff "github.com/maximhq/bifrost/framework@$FRAMEWORK_VERSION"

go mod tidy

cd ..

# We need to build UI first before we can validate the transport build
echo "🎨 Building UI..."
make build-ui

# Building hello-world plugin
echo "🔨 Building hello-world plugin..."
cd examples/plugins/hello-world
make build
cd ../../..

# Validate transport build
echo "🔨 Validating transport build..."
cd transports

# Run unit tests with coverage
echo "🧪 Running unit tests with coverage..."
go test --race -coverprofile=coverage.txt -coverpkg=./... ./...

# Upload coverage to Codecov
if [ -n "${CODECOV_TOKEN:-}" ]; then
  echo "📊 Uploading coverage to Codecov..."
  curl -Os https://uploader.codecov.io/latest/linux/codecov
  chmod +x codecov
  ./codecov -t "$CODECOV_TOKEN" -f coverage.txt -F transports
  rm -f codecov coverage.txt
else
  echo "ℹ️ CODECOV_TOKEN not set, skipping coverage upload"
  rm -f coverage.txt
fi

# Build the binary for integration testing
echo "🔨 Building binary for integration testing..."
mkdir -p ../tmp
cd bifrost-http
go build -o ../../tmp/bifrost-http .
cd ..

# Run integration tests with different configurations
echo "🧪 Running integration tests with different configurations..."
CONFIGS_TO_TEST=(
  "default"
  "emptystate"
  "noconfigstorenologstore"
  "witconfigstorelogstorepostgres"
  "withconfigstore"
  "withconfigstorelogsstorepostgres"
  "withconfigstorelogsstoresqlite"
  "withdynamicplugin"
  "withobservability"
  "withsemanticcache"
)

TEST_BINARY="../tmp/bifrost-http"
CONFIGS_DIR="../.github/workflows/configs"

# Cleanup function to ensure Docker services are stopped
cleanup_docker() {
  echo "🧹 Cleaning up Docker services..."
  docker compose -f "$CONFIGS_DIR/docker-compose.yml" down 2>/dev/null || true
}

# Register cleanup handler to run on script exit (success or failure)
trap cleanup_docker EXIT

# Running docker compose
echo "🐳 Starting Docker services (PostgreSQL, Weaviate, Redis)..."
docker compose -f "$CONFIGS_DIR/docker-compose.yml" up -d

# Wait for services to be healthy with polling
echo "⏳ Waiting for Docker services to be ready..."
MAX_WAIT=300
ELAPSED=0
SERVICES_READY=false

# Get expected number of services
EXPECTED_SERVICES=$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" config --services 2>/dev/null | wc -l | tr -d ' ')

while [ $ELAPSED -lt $MAX_WAIT ]; do
  # Get running container count
  RUNNING_COUNT=$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps --status running -q 2>/dev/null | wc -l | tr -d ' ')

  # Check health status: count healthy and unhealthy (starting/unhealthy) services
  # Services without healthchecks will show empty health status
  HEALTH_OUTPUT=$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps --format "{{.Name}}:{{.Health}}" 2>/dev/null)
  HEALTHY_COUNT=$(echo "$HEALTH_OUTPUT" | grep -c ":healthy") || HEALTHY_COUNT=0
  UNHEALTHY_COUNT=$(echo "$HEALTH_OUTPUT" | grep -cE ":(starting|unhealthy)") || UNHEALTHY_COUNT=0

  # All services are ready when:
  # 1. All expected services are running
  # 2. No services are in "starting" or "unhealthy" state
  if [ "$RUNNING_COUNT" -eq "$EXPECTED_SERVICES" ] && [ "$UNHEALTHY_COUNT" -eq "0" ]; then
    SERVICES_READY=true
    echo "✅ All Docker services are ready ($HEALTHY_COUNT with healthchecks, ${ELAPSED}s)"
    break
  fi

  sleep 2
  ELAPSED=$((ELAPSED + 2))
  echo "   ⏳ Waiting for services... ($RUNNING_COUNT/$EXPECTED_SERVICES running, $HEALTHY_COUNT healthy, $UNHEALTHY_COUNT starting, ${ELAPSED}s/${MAX_WAIT}s)"
done

if [ "$SERVICES_READY" = false ]; then
  echo "❌ Docker services failed to become healthy within ${MAX_WAIT}s"
  echo "   Current service status:"
  docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps
  exit 1
fi

for config in "${CONFIGS_TO_TEST[@]}"; do
  echo "  🔍 Testing with config: $config"
  config_path="$CONFIGS_DIR/$config"

  # Clean up databases before each config test for a clean slate
  echo "    🧹 Resetting PostgreSQL database..."
  # Note: DROP DATABASE cannot run inside a transaction, so we use separate -c flags
  # First terminate any active connections, then drop and recreate the database
  # PGPASSWORD is required for psql authentication (matches POSTGRES_PASSWORD in docker-compose.yml)
  docker exec -e PGPASSWORD=bifrost_password "$(docker compose -f "$CONFIGS_DIR/docker-compose.yml" ps -q postgres)" \
    psql -U bifrost -d postgres \
    -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = 'bifrost' AND pid <> pg_backend_pid();" \
    -c "DROP DATABASE IF EXISTS bifrost;" \
    -c "CREATE DATABASE bifrost;"

  echo "    🧹 Cleaning up SQLite database files for config: $config..."
  find "$config_path" -type f \( -name "*.db" -o -name "*.db-shm" -o -name "*.db-wal" \) -delete 2>/dev/null || true
  echo "    ✅ Database cleanup complete"

  if [ ! -d "$config_path" ]; then
    echo "    ⚠️  Warning: Config directory not found: $config_path (skipping)"
    continue
  fi

  # Create a temporary log file for server output
  SERVER_LOG=$(mktemp)

  # Start the server in background with a timeout, logging to file and console
  timeout 120s $TEST_BINARY --app-dir "$config_path" --port 18080 --log-level debug 2>&1 | tee "$SERVER_LOG" &
  SERVER_PID=$!

  # Wait for server to be ready by looking for the startup message
  echo "    ⏳ Waiting for server to start..."
  MAX_WAIT=30
  ELAPSED=0
  SERVER_READY=false

  while [ $ELAPSED -lt $MAX_WAIT ]; do
    if grep -q "successfully started bifrost, serving UI on http://localhost:18080" "$SERVER_LOG" 2>/dev/null; then
      SERVER_READY=true
      echo "    ✅ Server started successfully with config: $config"
      break
    fi

    # Check if server process is still running
    if ! kill -0 $SERVER_PID 2>/dev/null; then
      echo "    ❌ Server process died before starting with config: $config"
      rm -f "$SERVER_LOG"
      exit 1
    fi

    sleep 1
    ELAPSED=$((ELAPSED + 1))
  done

  if [ "$SERVER_READY" = false ]; then
    echo "    ❌ Server failed to start within ${MAX_WAIT}s with config: $config"
    kill $SERVER_PID 2>/dev/null || true
    wait $SERVER_PID 2>/dev/null || true
    rm -f "$SERVER_LOG"
    exit 1
  fi

  # Run get_curls.sh to test all GET endpoints
  echo "    🧪 Running API endpoint tests..."
  echo "    🔍 DEBUG: SCRIPT_DIR=$SCRIPT_DIR"
  echo "    🔍 DEBUG: PWD=$(pwd)"
  GET_CURLS_SCRIPT="$SCRIPT_DIR/get_curls.sh"
  echo "    🔍 DEBUG: GET_CURLS_SCRIPT=$GET_CURLS_SCRIPT"
  echo "    🔍 DEBUG: File exists check: $([ -f "$GET_CURLS_SCRIPT" ] && echo 'YES' || echo 'NO')"

  if [ -f "$GET_CURLS_SCRIPT" ]; then
    BASE_URL="http://localhost:18080" "$GET_CURLS_SCRIPT"
    CURL_EXIT_CODE=$?

    if [ $CURL_EXIT_CODE -eq 0 ]; then
      echo "    ✅ API endpoint tests passed for config: $config"
    else
      echo "    ❌ API endpoint tests failed for config: $config (exit code: $CURL_EXIT_CODE)"
      kill $SERVER_PID 2>/dev/null || true
      wait $SERVER_PID 2>/dev/null || true
      rm -f "$SERVER_LOG"
      exit 1
    fi
  else
    echo "    ⚠️  Warning: get_curls.sh not found at $GET_CURLS_SCRIPT (skipping endpoint tests)"
  fi

  # Kill the server
  kill $SERVER_PID 2>/dev/null || true
  wait $SERVER_PID 2>/dev/null || true

  # Clean up log file
  rm -f "$SERVER_LOG"

  # Clean up any lingering processes
  sleep 1
done

cd ..
echo "✅ Transport build validation successful"

# Commit and push changes if any
# First, pull latest changes to avoid conflicts
CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [ "$CURRENT_BRANCH" = "HEAD" ]; then
  # In detached HEAD state (common in CI), use GITHUB_REF_NAME or default to main
  CURRENT_BRANCH="${GITHUB_REF_NAME:-main}"
fi

echo "Pulling latest changes from origin/$CURRENT_BRANCH..."
if ! git pull origin "$CURRENT_BRANCH"; then
  echo "❌ Error: git pull origin $CURRENT_BRANCH failed"
  exit 1
fi

# Stage any changes made to transports/
git add transports/

# Check if there are staged changes after pulling
if ! git diff --cached --quiet; then
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
  echo "🔧 Committing and pushing changes..."
  git commit -m "transports: update dependencies --skip-pipeline"
  git push -u origin HEAD
else
  echo "ℹ️ No staged changes to commit"
fi

# Install cross-compilation toolchains
echo "📦 Installing cross-compilation toolchains..."
bash ./.github/workflows/scripts/install-cross-compilers.sh

# Build Go executables
echo "🔨 Building executables..."
bash ./.github/workflows/scripts/build-executables.sh $VERSION

# Configure and upload to R2
echo "📤 Uploading binaries..."
bash ./.github/workflows/scripts/configure-r2.sh
bash ./.github/workflows/scripts/upload-to-r2.sh "$TAG_NAME"

# Capturing changelog
CHANGELOG_BODY=$(cat transports/changelog.md)
# Skip comments from changelog
CHANGELOG_BODY=$(echo "$CHANGELOG_BODY" | grep -v '^<!--' | grep -v '^-->')
# If changelog is empty, return error
if [ -z "$CHANGELOG_BODY" ]; then
  echo "❌ Changelog is empty"
  exit 1
fi
echo "📝 New changelog: $CHANGELOG_BODY"

# Finding previous tag
echo "🔍 Finding previous tag..."
PREV_TAG=$(git tag -l "transports/v*" | sort -V | tail -1)
if [[ "$PREV_TAG" == "$TAG_NAME" ]]; then
  PREV_TAG=$(git tag -l "transports/v*" | sort -V | tail -2 | head -1)
fi
echo "🔍 Previous tag: $PREV_TAG"

# Get message of the tag
echo "🔍 Getting previous tag message..."
PREV_CHANGELOG=$(git tag -l --format='%(contents)' "$PREV_TAG")
echo "📝 Previous changelog body: $PREV_CHANGELOG"

# Checking if tag message is the same as the changelog
if [[ "$PREV_CHANGELOG" == "$CHANGELOG_BODY" ]]; then
  echo "❌ Changelog is the same as the previous changelog"
  exit 1
fi

# Create and push tag
echo "🏷️ Creating tag: $TAG_NAME"
git tag "$TAG_NAME" -m "Release transports v$VERSION" -m "$CHANGELOG_BODY"
git push origin "$TAG_NAME"

# Create GitHub release
TITLE="Bifrost HTTP v$VERSION"

# Mark prereleases when version contains a hyphen
PRERELEASE_FLAG=""
if [[ "$VERSION" == *-* ]]; then
  PRERELEASE_FLAG="--prerelease"
fi

LATEST_FLAG=""
if [[ "$VERSION" != *-* ]]; then
  LATEST_FLAG="--latest"
fi

# Generate plugin version summary
PLUGIN_UPDATES=""
if [ ${#PLUGINS_USED[@]} -gt 0 ]; then
  PLUGIN_UPDATES="

### 🔌 Plugin Versions
This release includes the following plugin versions:
"
  for plugin_info in "${PLUGINS_USED[@]}"; do
    plugin_name="${plugin_info%%:*}"
    plugin_version="${plugin_info##*:}"
    PLUGIN_UPDATES="$PLUGIN_UPDATES- **$plugin_name**: \`$plugin_version\`
"
  done
else
  # Show all available plugin versions even if not directly used
  PLUGIN_UPDATES="

### 🔌 Available Plugin Versions
The following plugin versions are compatible with this release:
"
  for plugin_name in "${!PLUGIN_VERSIONS[@]}"; do
    plugin_version="${PLUGIN_VERSIONS[$plugin_name]}"
    PLUGIN_UPDATES="$PLUGIN_UPDATES- **$plugin_name**: \`$plugin_version\`
"
  done
fi

BODY="## Bifrost HTTP Transport Release v$VERSION

$CHANGELOG_BODY

### Installation

#### Docker
\`\`\`bash
docker run -p 8080:8080 maximhq/bifrost:v$VERSION
\`\`\`

#### Binary Download
\`\`\`bash
npx @maximhq/bifrost --transport-version v$VERSION
\`\`\`

### Docker Images
- **\`maximhq/bifrost:v$VERSION\`** - This specific version
- **\`maximhq/bifrost:latest\`** - Latest version (updated with this release)

---
_This release was automatically created with dependencies: core \`$CORE_VERSION\`, framework \`$FRAMEWORK_VERSION\`. All plugins have been validated and updated._"

if [ -z "${GH_TOKEN:-}" ] && [ -z "${GITHUB_TOKEN:-}" ]; then
  echo "Error: GH_TOKEN or GITHUB_TOKEN is not set. Please export one to authenticate the GitHub CLI."
  exit 1
fi

echo "🎉 Creating GitHub release for $TITLE..."
gh release create "$TAG_NAME" \
  --title "$TITLE" \
  --notes "$BODY" \
  ${PRERELEASE_FLAG} ${LATEST_FLAG}

echo "✅ Bifrost HTTP released successfully"

# Print summary
echo ""
echo "📋 Release Summary:"
echo "   🏷️  Tag: $TAG_NAME"
echo "   🔧 Core version: $CORE_VERSION"
echo "   🔧 Framework version: $FRAMEWORK_VERSION"
echo "   📦 Transport: Updated"
if [ ${#PLUGINS_USED[@]} -gt 0 ]; then
  echo "   🔌 Plugins used: ${PLUGINS_USED[*]}"
else
  echo "   🔌 Available plugins: $(printf "%s " "${!PLUGIN_VERSIONS[@]}")"
fi
echo "   🎉 GitHub release: Created"

echo "success=true" >> "$GITHUB_OUTPUT"
