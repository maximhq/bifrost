#!/usr/bin/env bash
set -euo pipefail

# Fail only on documentation links this change breaks.
#
# `mintlify broken-links` reports the whole docs tree, and the tree already has
# broken links. Failing on the total would hand every unrelated docs pull request
# an inherited red check, and a check that is red on arrival stops being read.
# So run the same check against the comparison point and report the difference:
# the rule is "no new broken links", which is enforceable from the first commit
# and still shrinks the debt whenever someone fixes one.
#
# Usage: check-new-broken-links.sh <base-ref>

BASE_REF=${1:?usage: check-new-broken-links.sh <base-ref>}
# Pinned, not @latest: the two runs below are only comparable if they were
# produced by the same tool, and resolving the tag twice leaves the check one
# upstream publish away from diffing two different versions' output. A bump here
# is a deliberate change with a diff to review, like any other dependency.
MINTLIFY_VERSION=${MINTLIFY_VERSION:-4.2.810}
REPO_ROOT=$(git rev-parse --show-toplevel)
TEMP_ROOT=$(mktemp -d)
BASE_TREE="$TEMP_ROOT/base-tree"
BASE_REPORT="$TEMP_ROOT/base-report"
HEAD_REPORT="$TEMP_ROOT/head-report"

cleanup() {
  git -C "$REPO_ROOT" worktree remove --force "$BASE_TREE" >/dev/null 2>&1 || true
  # Keep the worktree and reports under one private mktemp directory. Deriving
  # sibling names from the worktree path would leave those report paths
  # unreserved until redirection and make cleanup act on paths we did not create.
  rm -rf "$TEMP_ROOT"
}
trap cleanup EXIT

# mintlify ends a completed run with exactly one of these verdicts: the clean
# one exits 0, the other exits 1. Both are dropped from the compared report —
# the tally moves whenever the count does, which would otherwise register as a
# finding on every change, including one that only fixes links.
CLEAN_VERDICT='^success no broken links found$'
BROKEN_VERDICT='^found [0-9]+ broken links? in [0-9]+ files?$'

# Collect the broken-link report for one docs directory, normalized so two runs
# are comparable.
collect() {
  local docs_dir=$1 stderr_file=$2 output status
  set +e
  # Mintlify's report and verdict are stdout. Keep npm/install diagnostics on
  # stderr out of the normalized link set: a warning that appears on only one
  # run is not a broken documentation link and must not become a false diff.
  output=$(cd "$docs_dir" && npx --yes "mintlify@${MINTLIFY_VERSION}" broken-links 2>"$stderr_file")
  status=$?
  set -e

  # Strip every CSI escape sequence, not just the colour ones: the progress
  # spinner repaints with cursor-movement codes (ESC[2K, ESC[1A, ESC[G) and its
  # last repaint shares a line with the verdict, so a colour-only filter leaves
  # control bytes glued to the front of the one line that has to be recognized.
  # Then drop the spinner frames themselves — how many a run emits depends on
  # how long it took, so they differ between two runs of the same docs tree.
  output=$(printf '%s\n' "$output" \
    | sed -E -e 's/\x1b\[[0-9;?]*[A-Za-z]//g' \
             -e 's/[[:space:]]*$//' \
             -e '/checking for broken links/d')

  # A difference between two reports only means something if both sides are
  # reports. An invocation that fails the same way twice — an incompatible
  # release, a failed install, a crash — normalizes to two identical outputs
  # with no links in them, which compares equal and passes the check without
  # anything having been checked. Require the verdict only a completed run
  # prints, and fail loudly when it is absent.
  if ! printf '%s\n' "$output" | grep -Eq "$CLEAN_VERDICT|$BROKEN_VERDICT"; then
    echo "ERROR: mintlify@${MINTLIFY_VERSION} broken-links did not complete in $docs_dir (exit $status)" >&2
    echo "  Expected a run ending in 'success no broken links found' or 'found N broken links in M files'." >&2
    printf '%s\n' "$output" | tail -20 | sed 's/^/  /' >&2
    if [ -s "$stderr_file" ]; then
      echo "  stderr:" >&2
      tail -20 "$stderr_file" | sed 's/^/    /' >&2
    fi
    return 1
  fi

  # Normalize with one sed program rather than a grep filter: grep exits 1 when
  # it selects nothing, and under `set -o pipefail` that fails the collection.
  # A report holding nothing but the verdict — the shape of a docs tree with no
  # broken links at all — would then turn the cleanest possible result into a
  # red check with no diagnostic.
  printf '%s\n' "$output" \
    | sed -E -e "/${CLEAN_VERDICT}/d" \
             -e "/${BROKEN_VERDICT}/d" \
             -e '/^[[:space:]]*$/d' \
    | sort -u
}

echo "Collecting broken links at ${BASE_REF}..."
git -C "$REPO_ROOT" worktree add --detach "$BASE_TREE" "$BASE_REF" >/dev/null
collect "$BASE_TREE/docs" "$TEMP_ROOT/base-stderr" > "$BASE_REPORT" || exit 1

echo "Collecting broken links at HEAD..."
collect "$REPO_ROOT/docs" "$TEMP_ROOT/head-stderr" > "$HEAD_REPORT" || exit 1

NEW_LINKS=$(comm -13 "$BASE_REPORT" "$HEAD_REPORT" || true)
FIXED_LINKS=$(comm -23 "$BASE_REPORT" "$HEAD_REPORT" || true)

if [ -n "$FIXED_LINKS" ]; then
  echo "Links this change fixes:"
  printf '%s\n' "$FIXED_LINKS" | sed 's/^/  /'
fi

if [ -n "$NEW_LINKS" ]; then
  echo "::error::this change introduces broken documentation links"
  printf '%s\n' "$NEW_LINKS" | sed 's/^/  /'
  exit 1
fi

PRE_EXISTING=$(wc -l < "$BASE_REPORT" | tr -d ' ')
echo "✅ No new broken links (${PRE_EXISTING} pre-existing, unchanged by this change)"
