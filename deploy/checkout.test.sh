#!/usr/bin/env bash
set -euo pipefail

# The Cloud Shell one-liners in README.md and the deployment guides update an
# existing `bifrost` clone or create one, then run the platform's deploy
# script. Every page must carry the same form, and each distinct command is
# exercised verbatim against a local bare repository whose deploy scripts only
# record that they ran: a fresh directory clones and runs; an existing clone is
# fast-forwarded and runs; a clone that cannot fast-forward must stop before the
# script, since `git clone` refuses the non-empty directory.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAILED=0
fail() { echo "FAIL: $*" >&2; FAILED=1; }

PAGES=(README.md docs/deployment-guides/ecs.mdx docs/deployment-guides/platforms/cloud-run.mdx docs/deployment-guides/platforms/aks.mdx docs/deployment-guides/platforms/eks.mdx docs/deployment-guides/platforms/gke.mdx)
FORM='^\( git -C bifrost pull --ff-only 2>/dev/null \|\| git clone --depth 1 https://github\.com/maximhq/bifrost \) && bifrost/deploy/[a-z]+/deploy\.sh$'
declare -A SEEN=()
LINES=()
for page in "${PAGES[@]}"; do
  [[ -f "$ROOT/$page" ]] || { fail "$page is missing"; continue; }
  # Any line that runs a deploy script after a clone must be in the exact form.
  while IFS= read -r line; do
    if ! [[ "$line" =~ $FORM ]]; then
      fail "$page carries a checkout one-liner in a different form: $line"
      continue
    fi
    if [[ -z "${SEEN[$line]:-}" ]]; then SEEN[$line]=1; LINES+=("$line"); fi
  done < <(grep -E 'git clone .*bifrost/deploy/[a-z]+/deploy\.sh' "$ROOT/$page")
done
[[ ${#LINES[@]} -ge 3 ]] || { fail "expected at least three distinct Cloud Shell one-liners across the README and guides, found ${#LINES[@]}"; exit 1; }
echo "distinct one-liners: ${#LINES[@]} (from ${#PAGES[@]} pages)"

# A local "upstream" whose deploy scripts append their path to a marker file.
UP="$WORK/upstream"; mkdir -p "$UP"
git -C "$UP" init -q -b main
for line in "${LINES[@]}"; do
  script="${line##*&& bifrost/}"
  mkdir -p "$UP/$(dirname "$script")"
  printf '#!/usr/bin/env bash\necho "%s" >> "$CHECKOUT_TEST_MARKER"\n' "$script" > "$UP/$script"
  chmod +x "$UP/$script"
done
git -C "$UP" -c user.email=t@example.com -c user.name=t add -A
git -C "$UP" -c user.email=t@example.com -c user.name=t commit -q -m "v1"
git clone -q --bare "$UP" "$WORK/bifrost.git"
advance_upstream() {
  echo "$1" > "$UP/VERSION"
  git -C "$UP" -c user.email=t@example.com -c user.name=t add -A
  git -C "$UP" -c user.email=t@example.com -c user.name=t commit -q -m "$1"
  git -C "$UP" push -q "$WORK/bifrost.git" main
}

export CHECKOUT_TEST_MARKER="$WORK/marker"
# Point a README command at the local bare repository (file:// keeps --depth meaningful).
local_cmd() { printf '%s' "${1//https:\/\/github.com\/maximhq\/bifrost/file://$WORK/bifrost.git}"; }

n=0
for line in "${LINES[@]}"; do
  n=$((n+1)); script="${line##*&& bifrost/}"
  RUN="$WORK/run$n"; mkdir -p "$RUN"

  # 1. Fresh directory: clone and run.
  : > "$CHECKOUT_TEST_MARKER"
  if (cd "$RUN" && bash -c "$(local_cmd "$line")" > "$WORK/log" 2>&1); then
    grep -q "^$script$" "$CHECKOUT_TEST_MARKER" || fail "$script fresh: the deploy script did not run"
    [[ -d "$RUN/bifrost/.git" ]] || fail "$script fresh: no clone was created"
  else
    cat "$WORK/log" >&2; fail "$script fresh: the one-liner exited non-zero"
  fi

  # 2. Existing clone: fast-forward to the new upstream revision, then run.
  advance_upstream "v$n-2"
  : > "$CHECKOUT_TEST_MARKER"
  if (cd "$RUN" && bash -c "$(local_cmd "$line")" > "$WORK/log" 2>&1); then
    grep -q "^$script$" "$CHECKOUT_TEST_MARKER" || fail "$script existing clone: the deploy script did not run"
    [[ "$(git -C "$RUN/bifrost" rev-parse HEAD)" == "$(git -C "$WORK/bifrost.git" rev-parse main)" ]] || fail "$script existing clone: the clone was not fast-forwarded to the current revision"
  else
    cat "$WORK/log" >&2; fail "$script existing clone: the one-liner exited non-zero"
  fi

  # 3. A clone that cannot fast-forward (local commit, upstream moved on): no script run.
  echo local > "$RUN/bifrost/LOCAL"
  git -C "$RUN/bifrost" -c user.email=t@example.com -c user.name=t add -A
  git -C "$RUN/bifrost" -c user.email=t@example.com -c user.name=t commit -q -m "local change"
  advance_upstream "v$n-3"
  : > "$CHECKOUT_TEST_MARKER"
  if (cd "$RUN" && bash -c "$(local_cmd "$line")" > "$WORK/log" 2>&1); then
    fail "$script diverged clone: the one-liner must exit non-zero when the pull cannot fast-forward"
  else
    [[ ! -s "$CHECKOUT_TEST_MARKER" ]] || fail "$script diverged clone: the deploy script ran despite the failed checkout: $(cat "$CHECKOUT_TEST_MARKER")"
  fi
done

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: Cloud Shell checkout one-liners"
