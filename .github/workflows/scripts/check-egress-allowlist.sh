#!/usr/bin/env bash
set -euo pipefail

# Assert that every workflow job which runs apt-get under a blocked egress
# policy also allowlists the Ubuntu APT hosts it will need.
#
# harden-runner's `egress-policy: block` drops every destination absent from
# `allowed-endpoints`, so a job that shells out to `apt-get update` fails at the
# mirror fetch unless those hosts are listed. The failure only surfaces when the
# workflow actually runs - release-pipeline.yml triggers on push to main, not on
# pull requests - so a job added with a copied-but-incomplete allowlist reaches
# production untested and takes the release with it.
#
# Usage: ./check-egress-allowlist.sh [workflow.yml ...]
#   With no arguments, checks every workflow in .github/workflows.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKFLOW_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

if [[ $# -gt 0 ]]; then
  FILES=("$@")
else
  FILES=()
  while IFS= read -r f; do FILES+=("$f"); done < <(find "${WORKFLOW_DIR}" -maxdepth 1 -name '*.yml' -o -maxdepth 1 -name '*.yaml' | sort)
fi

python3 - "${FILES[@]}" <<'PY'
import sys
import yaml

# Hosts apt-get needs on a GitHub-hosted ubuntu runner. azure.archive.ubuntu.com
# is the Azure-local mirror the runner images point at (plain HTTP), and
# esm.ubuntu.com serves the Expanded Security Maintenance lists apt refreshes
# alongside it.
REQUIRED_APT_HOSTS = ("azure.archive.ubuntu.com:80", "esm.ubuntu.com:443")

failures = []
checked = 0

for path in sys.argv[1:]:
    with open(path) as fh:
        doc = yaml.safe_load(fh) or {}

    for job_name, job in (doc.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        steps = job.get("steps") or []

        uses_apt = any(
            "apt-get" in (step.get("run") or "")
            for step in steps
            if isinstance(step, dict)
        )
        if not uses_apt:
            continue

        harden = next(
            (
                step
                for step in steps
                if isinstance(step, dict)
                and "step-security/harden-runner" in (step.get("uses") or "")
            ),
            None,
        )
        if harden is None:
            continue

        with_block = harden.get("with") or {}
        if str(with_block.get("egress-policy", "")).strip() != "block":
            continue

        checked += 1
        allowed = set((with_block.get("allowed-endpoints") or "").split())
        missing = [host for host in REQUIRED_APT_HOSTS if host not in allowed]
        if missing:
            failures.append((path, job_name, missing))

for path, job_name, missing in failures:
    print(f"FAIL {path} :: job '{job_name}' runs apt-get under egress-policy: block "
          f"but omits {', '.join(missing)}", file=sys.stderr)

if failures:
    print(f"\n{len(failures)} of {checked} apt-using blocked-egress job(s) have an "
          f"incomplete allowlist.", file=sys.stderr)
    sys.exit(1)

print(f"OK: all {checked} apt-using blocked-egress job(s) allowlist the Ubuntu APT hosts.")
PY
