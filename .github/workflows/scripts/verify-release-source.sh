#!/usr/bin/env bash
# Bind publication to canonical repository IDs, an approved ref, and the event commit.
set -euo pipefail
reject() { echo "Release source rejected: $1" >&2; exit 1; }
[[ "${GITHUB_REPOSITORY:-}" == maximhq/bifrost ]] || reject 'repository name mismatch'
[[ "${GITHUB_REPOSITORY_ID:-}" == 951115072 ]] || reject 'repository ID mismatch'
[[ "${GITHUB_REPOSITORY_OWNER_ID:-}" == 139708451 ]] || reject 'repository owner ID mismatch'
case "${GITHUB_EVENT_NAME:-}" in push|workflow_dispatch) ;; *) reject 'requires a push or explicit dispatch' ;; esac
case "${GITHUB_REF:-}" in refs/heads/main|refs/heads/v2.0.0) ;; *) reject 'unapproved release branch' ;; esac
[[ "${GITHUB_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || reject 'invalid event commit'
[[ "$(git rev-parse HEAD)" == "$GITHUB_SHA" ]] || reject 'checkout differs from event commit'
case "$(git remote get-url origin)" in
  https://github.com/maximhq/bifrost|https://github.com/maximhq/bifrost.git|git@github.com:maximhq/bifrost.git) ;;
  *) reject 'noncanonical origin' ;;
esac
tracked_status="$(git status --porcelain --untracked-files=no)" || reject 'could not inspect tracked files'
[[ -z "$tracked_status" ]] || reject 'modified tracked files'
echo 'Canonical repository and immutable release source verified'
