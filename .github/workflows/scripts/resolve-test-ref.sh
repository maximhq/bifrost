#!/usr/bin/env bash
set -euo pipefail

# Resolves the commit a secret-bearing test run checks out, and refuses anything
# whose code could come from a fork.
#
# run-core-tests.yml and pr-tests.yml run with every provider secret. A free-form
# ref would accept refs/pull/<n>/head or a fork's SHA, both of which
# actions/checkout fetches through this repo. So:
#
#   branch <name>   a plain branch name of this repo, resolved with ls-remote
#   pr <number>     a PR whose head branch lives in this repo; empty means the
#                   dispatched commit (GITHUB_SHA)
#
# Writes sha= (and number= for a PR) to GITHUB_OUTPUT. To test a fork PR, review
# it and push that commit to a branch here first.
#
# The workflows run this from a sparse checkout of their own commit, before the
# requested code is checked out, so the requested code never decides what runs.
#
# Usage: ./resolve-test-ref.sh branch <name>
#        ./resolve-test-ref.sh pr [number]

fail() {
  echo "::error::$*" >&2
  exit 1
}

emit() {
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    printf '%s\n' "$@" >> "$GITHUB_OUTPUT"
  fi
}

[[ $# -eq 2 ]] || fail "usage: $0 branch <name> | pr [number]"
: "${GITHUB_REPOSITORY:?}"
MODE="$1"
VALUE="$2"

case "$MODE" in
  branch)
    if [[ ! "$VALUE" =~ ^[A-Za-z0-9._][A-Za-z0-9._/-]*$ || "$VALUE" == refs/* || "$VALUE" == *..* ]]; then
      fail "branch must be a plain branch name of ${GITHUB_REPOSITORY}, got: $(printf '%q' "$VALUE")"
    fi
    # Exact match on the ref name: ls-remote patterns match from the tail, so
    # refs/heads/x/refs/heads/<name> would also come back.
    sha=$(git ls-remote "https://github.com/${GITHUB_REPOSITORY}.git" "refs/heads/${VALUE}" \
      | awk -v ref="refs/heads/${VALUE}" '$2 == ref { print $1 }')
    [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || fail "no branch '${VALUE}' in ${GITHUB_REPOSITORY}"
    echo "Testing ${VALUE} at ${sha}"
    emit "sha=${sha}"
    ;;
  pr)
    if [[ -z "$VALUE" ]]; then
      : "${GITHUB_SHA:?}"
      echo "No PR number; testing the dispatched commit ${GITHUB_SHA}"
      emit "sha=${GITHUB_SHA}"
      exit 0
    fi
    [[ "$VALUE" =~ ^[0-9]+$ ]] || fail "pr_number must be a number, got: $(printf '%q' "$VALUE")"
    pr=$(gh api "repos/${GITHUB_REPOSITORY}/pulls/${VALUE}")
    head_repo=$(jq -r '.head.repo.full_name // ""' <<<"$pr")
    if [[ "$head_repo" != "$GITHUB_REPOSITORY" ]]; then
      fail "PR #${VALUE} comes from '${head_repo:-a deleted fork}', not ${GITHUB_REPOSITORY}. Fork PRs are not run with secrets."
    fi
    sha=$(jq -r '.head.sha' <<<"$pr")
    [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || fail "PR #${VALUE} has no valid head SHA: $(printf '%q' "$sha")"
    echo "Testing PR #${VALUE} at ${sha}"
    emit "number=${VALUE}" "sha=${sha}"
    ;;
  *)
    fail "unknown mode $(printf '%q' "$MODE"); want branch or pr"
    ;;
esac
