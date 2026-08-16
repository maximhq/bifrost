#!/usr/bin/env bash
set -uo pipefail

# Publishes a test report to R2 so it can be linked from the public Mintlify
# release notes.
#
# Usage: ./upload-test-reports-to-r2.sh <transport-version> <label> <source-dir>
#   e.g. ./upload-test-reports-to-r2.sh 1.6.10 cli-harness-latest tests/e2e/clis/reports
#
# Why R2 and not the GitHub Actions artifact: an artifact link requires a GitHub
# login and is deleted at the end of its retention window, so a release note
# pointing at one 404s for most readers and for everyone eventually. R2 is the
# same public-asset path the release already uses for CLI binaries.
#
# The key is derived from the release version alone:
#
#   test-reports/v<version>/<label>/index.html
#
# That is deliberate. The uploading jobs (test-core, test-cli-harness) and the
# job that writes the changelog are different jobs, and passing URLs between
# them would mean threading job outputs through the whole release graph. A
# version-derived key lets push-mintlify-changelog.sh construct the same URL
# independently, with no coordination at all.
#
# NEVER fails the calling job. These reports are release *documentation*; a
# flaky object-store upload must not turn a green test run red. Every failure
# path warns and exits 0.

if [ $# -ne 3 ]; then
  echo "Usage: $0 <transport-version> <label> <source-dir>" >&2
  exit 2
fi

VERSION="${1#v}"
LABEL="$2"
SOURCE_DIR="$3"

if [ -z "${R2_ENDPOINT:-}" ] || [ -z "${R2_BUCKET:-}" ]; then
  echo "::warning::R2 not configured (R2_ENDPOINT/R2_BUCKET unset) - skipping test report upload for $LABEL"
  exit 0
fi

if [ ! -d "$SOURCE_DIR" ]; then
  echo "::warning::no report directory at $SOURCE_DIR - nothing to publish for $LABEL"
  exit 0
fi

if [ -z "$(find "$SOURCE_DIR" -type f -print -quit 2>/dev/null)" ]; then
  echo "::warning::$SOURCE_DIR is empty - nothing to publish for $LABEL"
  exit 0
fi

R2_ENDPOINT="$(echo "$R2_ENDPOINT" | tr -d '[:space:]')"
DEST="s3://${R2_BUCKET}/test-reports/v${VERSION}/${LABEL}/"

echo "📤 Publishing $LABEL test report: $SOURCE_DIR -> $DEST"

# --content-type is set per extension so the browser renders index.html rather
# than offering it as a download, which is what R2 does with the default
# binary/octet-stream on an unknown key.
upload() {
  local include="$1" content_type="$2"
  aws s3 cp "$SOURCE_DIR" "$DEST" \
    --endpoint-url "$R2_ENDPOINT" \
    --profile "${R2_AWS_PROFILE:-R2}" \
    --no-progress \
    --recursive \
    --exclude "*" \
    --include "$include" \
    --content-type "$content_type"
}

rc=0
upload "*.html" "text/html; charset=utf-8" || rc=$?
upload "*.json" "application/json" || rc=$?
upload "*.log" "text/plain; charset=utf-8" || rc=$?
upload "*.md" "text/markdown; charset=utf-8" || rc=$?

if [ "$rc" -ne 0 ]; then
  echo "::warning::test report upload for $LABEL did not fully succeed (exit $rc) - the release-note link may 404"
  exit 0
fi

echo "✅ Published $LABEL test report"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "Published \`$LABEL\` report to \`test-reports/v${VERSION}/${LABEL}/\`."
    echo ""
  } >>"$GITHUB_STEP_SUMMARY"
fi
