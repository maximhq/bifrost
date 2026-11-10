#!/usr/bin/env bash
# Pins the one-click deploy templates to a freshly released transport version.
#
# render.yaml (Render Blueprint), .do/deploy.template.yaml (DigitalOcean App
# Platform) and deploy/heroku/Dockerfile (Heroku container build) pin the tag.
# The Railway template, the Koyeb button and the Fly.io config track :latest
# and need nothing here.
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
FILES=(render.yaml .do/deploy.template.yaml deploy/heroku/Dockerfile)
CHANGED=()

for file in "${FILES[@]}"; do
  if ! grep -qE 'maximhq/bifrost:v[0-9][0-9A-Za-z.+-]*|^ *tag: v[0-9][0-9A-Za-z.+-]*' "$file"; then
    echo "::warning::$file has no pinned maximhq/bifrost tag; skipping"
    continue
  fi
  # Exact tag only: a pinned prerelease such as v2.5.0-rc1 must not count as
  # v2.5.0 being pinned, so escape the dots and require the tag to end here.
  # Covers both the image reference form and DigitalOcean's `tag:` field.
  if grep -qE "(maximhq/bifrost:|^ *tag: )${TAG//./\\.}([^0-9A-Za-z.+-]|\$)" "$file"; then
    echo "$file already pins $TAG"
    continue
  fi
  # Write through a temp file: BSD sed treats "-i -E" as a backup suffix.
  sed -E "s#(maximhq/bifrost:)v[0-9][0-9A-Za-z.+-]*#\1$TAG#g; s#^( *tag: )v[0-9][0-9A-Za-z.+-]*#\1$TAG#" "$file" > "$file.tmp"
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

git add "${CHANGED[@]}"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
# --skip-ci in the first line is what this repository's workflows check to skip
# a rerun (see the check-skip jobs); GitHub's own [skip ci] marker is not used.
git commit -m "Pins one-click deploy templates to $TAG --skip-ci"

# Other release jobs push to this branch too. Rebase onto whatever landed in the
# meantime and retry, instead of failing the release after the image is out.
for attempt in 1 2 3 4 5; do
  git pull --rebase origin "$CURRENT_BRANCH"
  if git push origin "$CURRENT_BRANCH"; then
    exit 0
  fi
  echo "Push attempt $attempt failed, retrying"
  sleep $((attempt * 5))
done
echo "::error::Could not push the deploy template pin to $CURRENT_BRANCH after 5 attempts"
exit 1
