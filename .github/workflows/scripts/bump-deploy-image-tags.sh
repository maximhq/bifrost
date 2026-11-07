#!/usr/bin/env bash
# Pins the one-click deploy templates to a freshly released transport version.
#
# Today that is render.yaml at the repository root (the "Deploy to Render"
# Blueprint). The Railway template tracks :latest and needs nothing here.
#
# Only stable versions are pinned, matching create-docker-manifest.sh which
# only moves the :latest tag for stable versions.
set -euo pipefail

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  echo "Usage: $0 <version>" >&2
  echo "Example: $0 2.5.0" >&2
  exit 1
fi

if [[ "$VERSION" == *-* ]]; then
  echo "Prerelease $VERSION: leaving deploy templates on the previous stable tag"
  exit 0
fi

TAG="v$VERSION"
FILES=(render.yaml)
CHANGED=()

for file in "${FILES[@]}"; do
  if ! grep -qE 'maximhq/bifrost:v[0-9][0-9A-Za-z.+-]*' "$file"; then
    echo "::warning::$file has no pinned maximhq/bifrost tag; skipping"
    continue
  fi
  if grep -q "maximhq/bifrost:$TAG\b" "$file"; then
    echo "$file already pins $TAG"
    continue
  fi
  # Write through a temp file: BSD sed treats "-i -E" as a backup suffix.
  sed -E "s#(maximhq/bifrost:)v[0-9][0-9A-Za-z.+-]*#\1$TAG#g" "$file" > "$file.tmp"
  mv "$file.tmp" "$file"
  CHANGED+=("$file")
  echo "Pinned $file to $TAG"
done

if [ "${#CHANGED[@]}" -eq 0 ]; then
  echo "Nothing to commit"
  exit 0
fi

CURRENT_BRANCH="$(git rev-parse --abbrev-ref HEAD)"
if [ "$CURRENT_BRANCH" = "HEAD" ]; then
  CURRENT_BRANCH="${GITHUB_REF_NAME:-main}"
fi

git pull origin "$CURRENT_BRANCH"
git add "${CHANGED[@]}"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git commit -m "Pins one-click deploy templates to $TAG --skip-ci"
git push origin "$CURRENT_BRANCH"
