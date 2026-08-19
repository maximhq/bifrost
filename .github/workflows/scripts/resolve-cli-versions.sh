#!/usr/bin/env bash
set -uo pipefail

# Resolves the last three published versions of each coding CLI the harness
# drives, and emits them as a GitHub Actions matrix.
#
# Why resolve once, here, instead of per job: "latest" is a moving target. If
# each matrix leg queried npm itself, a release landing mid-run would leave one
# leg's latest-1 equal to another leg's latest, and the run would silently test
# the same version twice while claiming three-version coverage. One snapshot,
# fanned out via fromJSON, makes all three legs agree by construction.
#
# Output (written to $GITHUB_OUTPUT as `matrix`):
#
#   [{"offset":0,"label":"latest","claude":"2.1.220","codex":"0.145.0","opencode":"1.18.15"},
#    {"offset":1,"label":"latest-1",...},
#    {"offset":2,"label":"latest-2",...}]
#
# Versions are true latest, deliberately - no minimum-age floor. These are
# third-party clients being exercised against Bifrost, not dependencies being
# shipped, so the supply-chain rationale for aging a release out does not
# apply, and lagging upstream by a week would defeat the point of testing
# latest at all.
#
# Prereleases are excluded: a version containing "-" (1.19.0-beta.1) is not
# what a user gets from `npm i -g`, so it is not what the release gate should
# be validating against.

if ! command -v jq >/dev/null 2>&1; then
  echo "❌ jq is required" >&2
  exit 1
fi

# Fallback pins, used only if npm resolution fails outright. These mirror the
# defaults in test-cli-harness.sh; keep them in sync when bumping either.
FALLBACK_CLAUDE="${CLAUDE_CODE_VERSION:-2.1.220}"
FALLBACK_CODEX="${CODEX_VERSION:-0.145.0}"
FALLBACK_OPENCODE="${OPENCODE_VERSION:-1.18.10}"

VERSION_COUNT="${VERSION_COUNT:-3}"

# resolve_versions <npm-package>
#
# Prints up to VERSION_COUNT stable versions, newest first, ending at the
# dist-tag `latest`. Anchoring on the dist-tag rather than the tail of the
# versions array matters: the array is in publish order, so a patch backported
# to an older line (1.17.21 published after 1.18.4) sits last without being
# newest. Slicing around the dist-tag keeps "latest" meaning what npm says it
# means, and latest-1/-2 its immediate stable predecessors.
resolve_versions() {
  local pkg="$1" meta

  if ! meta=$(npm view "$pkg" --json 2>/dev/null); then
    return 1
  fi

  jq -er --argjson want "$VERSION_COUNT" '
    ."dist-tags".latest as $latest
    | (.versions | if type == "array" then . else [.] end)
    | map(select(contains("-") | not))
    | . as $stable
    | (($stable | index($latest)) // (($stable | length) - 1)) as $i
    | (if $i - $want + 1 < 0 then 0 else $i - $want + 1 end) as $from
    | $stable[$from : $i + 1]
    | reverse
    | .[]
  ' <<<"$meta" 2>/dev/null
}

# nth_or_last <index> <version...>
#
# Clamps to the oldest available version when a package has published fewer
# than VERSION_COUNT stable releases, so a young package cannot collapse the
# matrix or emit an empty version string.
nth_or_last() {
  local want="$1"
  shift
  local -a versions=("$@")
  local last=$((${#versions[@]} - 1))
  if [ "$want" -gt "$last" ]; then
    want="$last"
  fi
  echo "${versions[$want]}"
}

resolve_failed=0

# Captured as newline-delimited text before being split into arrays. Checking
# emptiness on the raw string sidesteps `set -u` treating a declared-but-empty
# array as unbound, which is exactly the state the npm-failure path produces --
# i.e. the check for the failure would itself fail.
echo "🔎 Resolving the last $VERSION_COUNT stable versions of each CLI..."
claude_raw=$(resolve_versions "@anthropic-ai/claude-code" || true)
codex_raw=$(resolve_versions "@openai/codex" || true)
opencode_raw=$(resolve_versions "opencode-ai" || true)

for pair in "@anthropic-ai/claude-code:$claude_raw" "@openai/codex:$codex_raw" "opencode-ai:$opencode_raw"; do
  if [ -z "${pair#*:}" ]; then
    echo "::warning::could not resolve versions for ${pair%%:*} from npm"
    resolve_failed=1
  fi
done

CLAUDE_VERSIONS=()
CODEX_VERSIONS=()
OPENCODE_VERSIONS=()
if [ "$resolve_failed" -eq 0 ]; then
  while IFS= read -r line; do CLAUDE_VERSIONS+=("$line"); done <<<"$claude_raw"
  while IFS= read -r line; do CODEX_VERSIONS+=("$line"); done <<<"$codex_raw"
  while IFS= read -r line; do OPENCODE_VERSIONS+=("$line"); done <<<"$opencode_raw"
fi

if [ "$resolve_failed" -ne 0 ]; then
  # Degrade to a single pinned leg rather than an empty matrix. An empty matrix
  # skips the job, and the release gates downstream accept 'skipped' -- so a
  # transient npm outage would wave a release through with the CLI harness
  # never having run. One pinned leg plus a loud warning keeps that from being
  # silent.
  echo "::warning::falling back to pinned CLI versions; three-version coverage is NOT in effect for this run"
  matrix=$(jq -cn \
    --arg claude "$FALLBACK_CLAUDE" \
    --arg codex "$FALLBACK_CODEX" \
    --arg opencode "$FALLBACK_OPENCODE" \
    '[{offset: 0, label: "pinned", claude: $claude, codex: $codex, opencode: $opencode}]')
else
  entries=()
  for offset in $(seq 0 $((VERSION_COUNT - 1))); do
    label="latest"
    if [ "$offset" -gt 0 ]; then
      label="latest-$offset"
    fi
    entries+=("$(jq -cn \
      --argjson offset "$offset" \
      --arg label "$label" \
      --arg claude "$(nth_or_last "$offset" "${CLAUDE_VERSIONS[@]}")" \
      --arg codex "$(nth_or_last "$offset" "${CODEX_VERSIONS[@]}")" \
      --arg opencode "$(nth_or_last "$offset" "${OPENCODE_VERSIONS[@]}")" \
      '{offset: $offset, label: $label, claude: $claude, codex: $codex, opencode: $opencode}')")
  done
  matrix=$(printf '%s\n' "${entries[@]}" | jq -cs '.')
fi

echo ""
echo "Resolved matrix:"
jq -r '.[] | "  \(.label): claude=\(.claude) codex=\(.codex) opencode=\(.opencode)"' <<<"$matrix"

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "matrix=$matrix" >>"$GITHUB_OUTPUT"
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "## CLI harness versions under test"
    echo ""
    echo "| Leg | claude | codex | opencode |"
    echo "| --- | --- | --- | --- |"
    jq -r '.[] | "| \(.label) | `\(.claude)` | `\(.codex)` | `\(.opencode)` |"' <<<"$matrix"
    echo ""
  } >>"$GITHUB_STEP_SUMMARY"
fi
