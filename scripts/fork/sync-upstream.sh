#!/usr/bin/env bash
# Brings upstream Bifrost changes into this fork.
#
# Usage: scripts/fork/sync-upstream.sh [options]
#   --upstream-url URL      upstream repository (default: https://github.com/maximhq/bifrost.git)
#   --upstream-branch NAME  upstream branch to follow (default: dev)
#   --mirror NAME           local branch kept identical to upstream (default: dev; "" to skip)
#   --branch NAME           fork branch that receives the merge (default: plus)
#   --no-verify             skip scripts/fork/verify.sh after merging
#   --push                  push the mirror and fork branch to origin when done
#
# Steps: fetch upstream, fast-forward the mirror branch, merge upstream into the fork branch
# with git rerere on (recorded conflict resolutions are replayed), regenerate the bundled
# OpenAPI spec when it conflicts, then run the fork's verification.
#
# Exit codes: 0 synced (or already up to date), 1 conflicts left to resolve by hand (the merge
# stays in progress; resolve, `git commit`, then run scripts/fork/verify.sh), 2 usage or state
# error, 3 merged but verification failed.
set -euo pipefail

upstream_url="https://github.com/maximhq/bifrost.git"
upstream_branch="dev"
mirror="dev"
branch="plus"
verify=true
push=false
while [ $# -gt 0 ]; do
  case "$1" in
    --upstream-url) upstream_url="$2"; shift ;;
    --upstream-branch) upstream_branch="$2"; shift ;;
    --mirror) mirror="$2"; shift ;;
    --branch) branch="$2"; shift ;;
    --no-verify) verify=false ;;
    --push) push=true ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift
done

root="$(git rev-parse --show-toplevel)"
cd "$root"

if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "working tree has uncommitted changes; commit or stash them first" >&2
  exit 2
fi
if [ -e "$(git rev-parse --git-path MERGE_HEAD)" ]; then
  echo "a merge is already in progress; finish or abort it first" >&2
  exit 2
fi

if ! git remote get-url upstream >/dev/null 2>&1; then
  git remote add upstream "$upstream_url"
fi
git fetch --no-tags upstream "$upstream_branch"
upstream_ref="upstream/$upstream_branch"
upstream_sha="$(git rev-parse --short "$upstream_ref")"

# Keep the mirror branch byte-for-byte equal to upstream, so `git diff <mirror> <branch>` is
# exactly what the fork changes. It only ever fast-forwards.
if [ -n "$mirror" ]; then
  for ref in "refs/heads/$mirror" "refs/remotes/origin/$mirror"; do
    if git show-ref --verify --quiet "$ref" && ! git merge-base --is-ancestor "$ref" "$upstream_ref"; then
      echo "$ref has commits that are not upstream; $mirror must stay an exact mirror of $upstream_ref" >&2
      exit 2
    fi
  done
  if [ "$(git rev-parse --abbrev-ref HEAD)" = "$mirror" ]; then
    git merge --ff-only "$upstream_ref"
  else
    git branch -f "$mirror" "$upstream_ref"
  fi
fi

git checkout -q "$branch"
git config rerere.enabled true
git config rerere.autoupdate true

if git merge-base --is-ancestor "$upstream_ref" HEAD; then
  echo "$branch already contains $upstream_ref ($upstream_sha)"
else
  merge_failed=false
  git merge --no-ff --no-edit -m "Merge upstream $upstream_branch ($upstream_sha) into $branch" "$upstream_ref" || merge_failed=true

  if [ "$merge_failed" = true ]; then
    # The bundled spec is generated from the YAML sources, so it is never merged by hand: once
    # every other conflict is resolved (the sources may be among them), it is rebuilt.
    openapi_json="docs/openapi/openapi.json"
    unresolved="$(git diff --name-only --diff-filter=U | grep -vx "$openapi_json" || true)"
    if [ -z "$unresolved" ] && git diff --name-only --diff-filter=U | grep -qx "$openapi_json"; then
      git checkout --theirs -- "$openapi_json"
      if (cd docs/openapi && python3 bundle.py >/dev/null); then
        git add "$openapi_json"
        echo "regenerated $openapi_json"
      else
        echo "could not regenerate $openapi_json (needs python3 with PyYAML)" >&2
      fi
    fi

    unresolved="$(git diff --name-only --diff-filter=U)"
    if [ -n "$unresolved" ]; then
      echo
      echo "Conflicts left to resolve (see FORK.md, 'Resolving sync conflicts'):"
      while IFS= read -r file; do
        echo "  $file"
      done <<<"$unresolved"
      echo
      echo "Resolve them (regenerate $openapi_json with 'cd docs/openapi && python3 bundle.py' last),"
      echo "then: git add <files> && git commit --no-edit && scripts/fork/verify.sh"
      exit 1
    fi
    git commit --no-edit
  fi
  echo "merged $upstream_ref ($upstream_sha) into $branch"
fi

if [ "$verify" = true ]; then
  if ! scripts/fork/verify.sh; then
    echo "merged, but fork verification failed; fix it before pushing" >&2
    exit 3
  fi
fi

if [ "$push" = true ]; then
  if [ -n "$mirror" ]; then
    git push origin "$mirror"
  fi
  git push origin "$branch"
fi
