#!/usr/bin/env bash
set -euo pipefail

# Reads a release version from a plain-text version file or a package.json and
# writes it to GITHUB_OUTPUT as `version` (and `full-tag` when a tag prefix is
# given).
#
# The value lands in GITHUB_OUTPUT and is then read by later steps. GitHub
# expands ${{ }} textually before the shell parses a `run:` line, so a version
# file holding `1.0.0$(...)` would execute wherever a step interpolates it. This
# is the one place those versions enter a workflow, and it rejects anything that
# is not a SemVer 2.0.0 string (see semver.sh, shared with extract-npx-version.sh).
#
# Usage: ./read-version-file.sh <path> [tag-prefix]
#   path        a version file, or a package.json (read via .version)
#   tag-prefix  optional; emits full-tag=<tag-prefix>/v<version>

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "Usage: $0 <path> [tag-prefix]" >&2
  exit 1
fi

FILE="$1"
TAG_PREFIX="${2:-}"

# shellcheck source=SCRIPTDIR/semver.sh
source "$(dirname "${BASH_SOURCE[0]}")/semver.sh"

if [[ ! -f "$FILE" ]]; then
  echo "::error::version source not found: $FILE" >&2
  exit 1
fi

if [[ "$(basename "$FILE")" == "package.json" ]]; then
  VERSION=$(jq -r '.version' "$FILE")
else
  # Strip only the trailing line ending. Deleting all whitespace would join a
  # split file ("1.0.1" over "0") into a version it never held (1.0.10).
  VERSION=$(cat "$FILE")
  VERSION="${VERSION%$'\r'}"
fi

if ! is_semver "$VERSION"; then
  # printf %q keeps a hostile value from being interpreted by the log viewer.
  echo "::error::invalid version in $FILE: $(printf '%q' "$VERSION"). Expected SemVer MAJOR.MINOR.PATCH[-pre][+build]" >&2
  exit 1
fi

echo "Version from $FILE: $VERSION"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "version=${VERSION}"
    if [[ -n "$TAG_PREFIX" ]]; then
      echo "full-tag=${TAG_PREFIX}/v${VERSION}"
    fi
  } >> "$GITHUB_OUTPUT"
fi
