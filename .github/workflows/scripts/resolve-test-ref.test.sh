#!/usr/bin/env bash
set -uo pipefail

# Tests resolve-test-ref.sh, which decides what commit run-core-tests.yml and
# pr-tests.yml check out before running with every provider secret. Fake `git`
# and `gh` binaries on PATH stand in for ls-remote and the pulls API, so this
# runs offline.
#
# Usage: ./resolve-test-ref.test.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESOLVER="${SCRIPT_DIR}/resolve-test-ref.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

REPO="maximhq/bifrost"
SHA_DEV="1111111111111111111111111111111111111111"
SHA_PR="2222222222222222222222222222222222222222"
SHA_RUN="3333333333333333333333333333333333333333"

mkdir -p "$WORK/bin"
# Permissive on purpose: every branch name "exists" unless listed here, so the
# resolver's own input checks are the only thing refusing refs/pull/..., "..",
# or "-dev". ls-remote's URL is recorded so the test can pin the repo.
cat > "$WORK/bin/git" <<EOF
#!/usr/bin/env bash
echo "\$*" >> "$WORK/git.log"
[[ "\$1" == ls-remote ]] || exit 2
case "\$3" in
  refs/heads/nope|refs/heads/$SHA_PR) ;;
  refs/heads/down) exit 128 ;;
  refs/heads/multi) printf '%s\trefs/heads/multi\n%s\trefs/heads/x/refs/heads/multi\n' "$SHA_DEV" "$SHA_PR" ;;
  *) printf '%s\t%s\n' "$SHA_DEV" "\$3" ;;
esac
EOF
# Same idea: any PR path answers as a same-repo PR unless listed, so the
# numeric check is what refuses "12;id".
cat > "$WORK/bin/gh" <<EOF
#!/usr/bin/env bash
case "\$2" in
  repos/$REPO/pulls/2) echo '{"head":{"sha":"$SHA_PR","repo":{"full_name":"someone/bifrost"}}}' ;;
  repos/$REPO/pulls/3) echo '{"head":{"sha":"$SHA_PR","repo":null}}' ;;
  repos/$REPO/pulls/4) echo '{"head":{"sha":"\$(id)","repo":{"full_name":"$REPO"}}}' ;;
  repos/$REPO/pulls/99) echo 'gh: Not Found (HTTP 404)' >&2; exit 1 ;;
  *) echo '{"head":{"sha":"$SHA_PR","repo":{"full_name":"$REPO"}}}' ;;
esac
EOF
chmod +x "$WORK/bin/git" "$WORK/bin/gh"

passed=0
failed=0

# run <mode> <value>: runs the resolver, leaving its GITHUB_OUTPUT in $WORK/out.
run() {
  : > "$WORK/out"
  PATH="$WORK/bin:$PATH" GITHUB_REPOSITORY="$REPO" GITHUB_SHA="$SHA_RUN" \
    GITHUB_OUTPUT="$WORK/out" "$RESOLVER" "$@" >/dev/null 2>&1
}

expect_ok() {
  local label="$1" want="$2"; shift 2
  if run "$@" && [[ "$(cat "$WORK/out")" == "$want" ]]; then
    echo "  ok - $label"; passed=$((passed + 1))
  else
    echo "FAIL: $label: got $(printf '%q' "$(cat "$WORK/out")"), want $(printf '%q' "$want")" >&2
    failed=$((failed + 1))
  fi
}

expect_refused() {
  local label="$1"; shift
  if run "$@" || [[ -s "$WORK/out" ]]; then
    echo "FAIL: resolver accepted $label" >&2; failed=$((failed + 1))
  else
    echo "  ok - refuses $label"; passed=$((passed + 1))
  fi
}

# --- branch: run-core-tests.yml ---------------------------------------------------
expect_ok "a branch of this repo resolves to its commit" "sha=${SHA_DEV}" branch dev
if grep -q "ls-remote https://github.com/${REPO}.git refs/heads/dev" "$WORK/git.log"; then
  echo "  ok - looks the branch up in this repo only"; passed=$((passed + 1))
else
  echo "FAIL: ls-remote was not pointed at ${REPO}: $(cat "$WORK/git.log")" >&2; failed=$((failed + 1))
fi

# The refs a fork's code is reachable through from this repo.
expect_refused "refs/pull/8998/head" branch refs/pull/8998/head
expect_refused "refs/heads/dev" branch refs/heads/dev
expect_refused "a bare commit SHA" branch "$SHA_PR"
expect_refused "a branch that does not exist" branch nope
expect_refused "a failed ls-remote" branch down
expect_ok "a tail-matching ref does not shadow the exact one" "sha=${SHA_DEV}" branch multi
# shellcheck disable=SC2016 # a literal payload
expect_refused "command substitution" branch 'dev$(id)'
expect_refused "a path traversal" branch 'a/../b'
expect_refused "a space" branch 'dev x'
expect_refused "an option-looking name" branch '-dev'
expect_refused "an empty branch" branch ''

# --- pr: pr-tests.yml -------------------------------------------------------------
expect_ok "no PR number tests the dispatched commit" "sha=${SHA_RUN}" pr ''
expect_ok "a same-repo PR resolves to its head" "number=1
sha=${SHA_PR}" pr 1
expect_refused "a fork PR" pr 2
expect_refused "a PR whose fork was deleted" pr 3
expect_refused "a malformed head SHA" pr 4
expect_refused "a PR that does not exist" pr 99
expect_refused "a non-numeric PR number" pr '12;id'
expect_refused "a negative PR number" pr '-1'

# --- usage ------------------------------------------------------------------------
expect_refused "an unknown mode" tag v1
expect_refused "no arguments"

echo ""
if [ "$failed" -ne 0 ]; then
  echo "$failed failed, $passed passed" >&2
  exit 1
fi
echo "$passed passed"
