#!/usr/bin/env bash
set -euo pipefail

# Exercise bump-deploy-image-tags.sh against a throwaway git repository with a
# bare origin holding all three pinned templates: prerelease skip, already pinned, a pinned prerelease that must be
# replaced, a file without a pin, a real bump that lands on origin, and a push
# race where origin moved underneath the job.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

# fresh_repo <pinned-tag>: a clone of a bare origin whose three templates pin the
# tag: render.yaml (image reference), .do/deploy.template.yaml (DigitalOcean's
# `tag:` field) and deploy/heroku/Dockerfile (FROM line).
fresh_repo() {
  rm -rf "$WORK/origin.git" "$WORK/repo" "$WORK/other"
  git init -q --bare -b main "$WORK/origin.git"
  git clone -q "$WORK/origin.git" "$WORK/repo" 2>/dev/null
  (
    cd "$WORK/repo"
    git config user.name test; git config user.email test@example.com
    git checkout -q -b main
    mkdir -p .do deploy/heroku
    printf 'services:\n  - type: web\n    image:\n      url: docker.io/maximhq/bifrost:%s\n' "$1" > render.yaml
    printf 'spec:\n  services:\n    - name: bifrost\n      image:\n        registry_type: DOCKER_HUB\n        registry: maximhq\n        repository: bifrost\n        tag: %s\n      http_port: 8080\n' "$1" > .do/deploy.template.yaml
    printf 'FROM maximhq/bifrost:%s\n' "$1" > deploy/heroku/Dockerfile
    git add -A; git commit -q -m "seed"; git push -q origin main
  )
}

run_bump() {
  (cd "$WORK/repo" && GITHUB_REF_NAME=main bash "$SCRIPT_DIR/bump-deploy-image-tags.sh" "$1") > "$WORK/log" 2>&1
}

# pinned: the tag each template carries, as "render do heroku".
pinned() {
  printf '%s %s %s' \
    "$(sed -n 's/.*maximhq\/bifrost:\(v[^ ]*\).*/\1/p' "$WORK/repo/render.yaml")" \
    "$(sed -n 's/^ *tag: \(v[^ ]*\).*/\1/p' "$WORK/repo/.do/deploy.template.yaml")" \
    "$(sed -n 's/^FROM maximhq\/bifrost:\(v[^ ]*\).*/\1/p' "$WORK/repo/deploy/heroku/Dockerfile")"
}
all_pinned() { [[ "$(pinned)" == "$1 $1 $1" ]]; }
origin_subject() { git --git-dir="$WORK/origin.git" log -1 --format=%s main; }

# 1. Prereleases never move the pin.
fresh_repo v2.5.0
run_bump 2.6.0-rc1 || fail "prerelease: script exited non-zero"
all_pinned v2.5.0 || fail "prerelease: pins changed to $(pinned)"
[[ "$(origin_subject)" == seed ]] || fail "prerelease: a commit was pushed"

# 2. Already pinned: nothing to commit.
fresh_repo v2.6.0
run_bump 2.6.0 || fail "already pinned: script exited non-zero"
grep -q "already pins v2.6.0" "$WORK/log" || fail "already pinned: no skip message"
[[ "$(origin_subject)" == seed ]] || fail "already pinned: a commit was pushed"

# 3. A pinned prerelease of the same version is not "already pinned".
fresh_repo v2.6.0-rc1
run_bump 2.6.0 || fail "pinned prerelease: script exited non-zero"
all_pinned v2.6.0 || fail "pinned prerelease: expected v2.6.0 everywhere, got $(pinned)"

# 4. A file without a pinned tag is skipped with a warning.
fresh_repo v2.5.0
printf 'services: []\n' > "$WORK/repo/render.yaml"
(cd "$WORK/repo" && git commit -q -am "unpin" && git push -q origin main)
run_bump 2.6.0 || fail "no pin: script exited non-zero"
grep -q "::warning::render.yaml has no pinned" "$WORK/log" || fail "no pin: missing warning"
[[ "$(pinned)" == " v2.6.0 v2.6.0" ]] || fail "no pin: the other templates were not bumped, got '$(pinned)'"

# 5. A stable bump replaces the tag and lands on origin with the skip marker.
fresh_repo v2.5.0
run_bump 2.6.0 || fail "bump: script exited non-zero"
all_pinned v2.6.0 || fail "bump: expected v2.6.0 everywhere, got $(pinned)"
[[ "$(origin_subject)" == *"v2.6.0"*"--skip-ci"* ]] || fail "bump: origin subject is '$(origin_subject)'"

# 6. Origin moved after the job checked out: the push must rebase and succeed.
fresh_repo v2.5.0
git clone -q "$WORK/origin.git" "$WORK/other" 2>/dev/null
(cd "$WORK/other" && git config user.name o && git config user.email o@example.com && echo x > other.txt && git add other.txt && git commit -q -m "concurrent" && git push -q origin main)
run_bump 2.6.0 || { cat "$WORK/log" >&2; fail "race: script exited non-zero"; }
all_pinned v2.6.0 || fail "race: expected v2.6.0 everywhere, got $(pinned)"
[[ "$(origin_subject)" == *"v2.6.0"* ]] || fail "race: origin subject is '$(origin_subject)'"
[[ "$(git --git-dir="$WORK/origin.git" rev-list --count main)" == 3 ]] || fail "race: expected a linear history of 3 commits, got $(git --git-dir="$WORK/origin.git" rev-list --count main)"
git --git-dir="$WORK/origin.git" cat-file -e main:other.txt || fail "race: the concurrent commit was lost"

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: bump-deploy-image-tags.sh"
