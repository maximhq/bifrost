#!/usr/bin/env bash
set -euo pipefail

# Two guards on harden-runner egress allowlists:
#
#   1. every job that runs apt-get under `egress-policy: block` allowlists the
#      Ubuntu APT hosts it will need;
#   2. every job that runs the provider harness allowlists each external host
#      the harness will dial - see the second python block for why that is not
#      the same set as "the hosts the collection sends requests to".
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
# Exported for the python blocks below. They run from stdin (`python3 - ...`),
# where __file__ is "<stdin>" rather than a path - deriving the repo root there
# silently resolves somewhere above the checkout, every input file reads as
# absent, and the check passes having examined nothing.
REPO_ROOT="$(cd "${WORKFLOW_DIR}/../.." && pwd)"
export REPO_ROOT

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

# ---------------------------------------------------------------------------
# 2. Provider-harness hosts
# ---------------------------------------------------------------------------
#
# The harness sends nearly every request to 127.0.0.1:8080, so its collection
# looks like it needs no egress at all. It does: bifrost-http runs on the SAME
# runner and makes the upstream provider calls, and it downloads any URL passed
# as a document/image source for providers whose APIs cannot take a URL
# directly. Those fetches leave the runner and are governed by the same
# allowlist.
#
# That combination is why this check exists. A blocked host surfaces as a
# connection error attributed to the provider, so it reads as a flaky provider
# rather than a firewall - and release-pipeline.yml only runs on push to main,
# so the first time anyone sees it is during a real release.
#
# The rule enforced here is deliberately blunt: every external host appearing in
# the harness collection or its seeded provider config must be either
# allowlisted or listed in NOT_DIALLED_HOSTS with a reason. Classifying
# automatically would mean deciding which URLs Bifrost fetches and which the
# provider fetches, which depends on per-provider API shape and would silently
# get it wrong. Forcing a human to say which one it is cannot.

python3 - "${FILES[@]}" <<'PY'
import json
import os
import re
import sys
import yaml

REPO_ROOT = os.environ["REPO_ROOT"]
COLLECTION = os.path.join(REPO_ROOT, "tests/e2e/api/collections/provider-harness.json")
PROVIDER_CONFIG = os.path.join(REPO_ROOT, "tests/integrations/python/config.json")

# A missing input means this check verified nothing, which must be an error and
# not a silent pass - that is the exact failure mode the REPO_ROOT export fixes.
for required_input in (COLLECTION, PROVIDER_CONFIG):
    if not os.path.exists(required_input):
        print(f"::error::egress check input not found: {required_input}", file=sys.stderr)
        sys.exit(1)

# Scripts that mean "this job runs the provider harness".
HARNESS_MARKERS = ("test-core.sh", "test-provider-harness.sh")

# Hosts that appear in the sources scanned above but that the runner never
# dials, each with the reason it is exempt. Anything not listed here and not
# allowlisted fails this check, so a new host forces an explicit decision rather
# than being quietly assumed harmless.
NOT_DIALLED_HOSTS = {
    # Fetched by the provider's own server-side tooling, never by Bifrost.
    "example.com": "MCP server_url and web_fetch targets - dereferenced by the provider",
    "www.youtube.com": "Gemini video input - Google fetches the URL server-side",
    "en.wikipedia.org": "googleSearch excludeDomains filter value, not a URL anyone fetches",
    "placeholder.search.windows.net": "Azure AI Search placeholder in a request body; never resolved",
    # Deliberately unresolvable - negative tests assert the failure path.
    "bifrost.invalid": "reserved .invalid TLD; negative-path tests assert it fails to resolve",
    # Documentation links inside descriptions and comments.
    "anthropic.com": "doc link in a folder description",
    "www.anthropic.com": "doc link in a folder description",
    "platform.claude.com": "doc link in a folder description",
    "developers.openai.com": "doc link in a folder description",
    "docs.aws.amazon.com": "doc link in a folder description",
    "docs.x.ai": "doc link in a folder description",
    "schema.getpostman.com": "Postman collection schema declaration, not fetched at run time",
    # Identifiers that merely look like endpoints.
    "s3.amazonaws.com": "XML namespace URI in a ListBucketResult document; namespaces are names, not fetches",
    "www.googleapis.com": "the cloud-platform OAuth SCOPE string; the token exchange itself goes to oauth2.googleapis.com",
}

HOST_RE = re.compile(r"https?://([A-Za-z0-9._-]+\.[A-Za-z]{2,})")
# {{var}} placeholders resolve at run time from secrets; the concrete hosts they
# expand to (regional Bedrock/Vertex endpoints) cannot be known statically, so
# they are out of scope here and stay a manual review item.
TEMPLATED = re.compile(r"\{\{")


def hosts_in(path):
    if not os.path.exists(path):
        return set()
    with open(path, encoding="utf-8") as fh:
        raw = fh.read()
    found = set()
    for line in raw.splitlines():
        if TEMPLATED.search(line):
            # Strip only URLs whose AUTHORITY is templated -- those expand to hosts that
            # cannot be known statically. A literal host with a templated PATH
            # ("https://files.example.com/{{file_id}}") still dials a knowable host and
            # must stay in the census, or the validator passes a blocked-egress job that
            # harden-runner will then kill at the request. Excluding "/" from the span
            # before the placeholder is what draws that line.
            line = re.sub(r"https?://[^\s\"'/]*\{\{[^\s\"']*", " ", line)
        for m in HOST_RE.finditer(line):
            found.add(m.group(1))
    return found


# Provider endpoints Bifrost builds in Go rather than reading from config, which
# is why scanning the seeded config alone is not enough: discoveryengine
# (the Vertex reranker backend) is assembled in core/providers/vertex and appears
# in no config file, so it was missing from two workflows' allowlists at once.
# Restricted to the cloud-provider domains whose hostnames are constructed this
# way; anything else in that source tree is a doc link, not a dial.
GO_HOST_SUFFIXES = (".googleapis.com", ".amazonaws.com", ".api.aws")


def hosts_in_go(root):
    found = set()
    providers_dir = os.path.join(root, "core", "providers")
    for dirpath, _, filenames in os.walk(providers_dir):
        for name in filenames:
            if not name.endswith(".go") or name.endswith("_test.go"):
                continue
            with open(os.path.join(dirpath, name), encoding="utf-8", errors="ignore") as fh:
                text = fh.read()
            for m in HOST_RE.finditer(text):
                host = m.group(1)
                if host.endswith(GO_HOST_SUFFIXES):
                    found.add(host)
    return found


required = hosts_in(COLLECTION) | hosts_in(PROVIDER_CONFIG) | hosts_in_go(REPO_ROOT)
required -= set(NOT_DIALLED_HOSTS)
required = {h for h in required if h not in ("localhost",)}

failures = []
checked = 0

for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as fh:
        doc = yaml.safe_load(fh) or {}

    for job_name, job in (doc.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        steps = job.get("steps") or []

        runs_harness = any(
            any(marker in (step.get("run") or "") for marker in HARNESS_MARKERS)
            for step in steps
            if isinstance(step, dict)
        )
        if not runs_harness:
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
        allowed = {
            entry.rsplit(":", 1)[0]
            for entry in (with_block.get("allowed-endpoints") or "").split()
        }
        missing = sorted(h for h in required if h not in allowed)
        if missing:
            failures.append((path, job_name, missing))

for path, job_name, missing in failures:
    print(
        f"FAIL {path} :: job '{job_name}' runs the provider harness under "
        f"egress-policy: block but omits {', '.join(missing)}",
        file=sys.stderr,
    )

if failures:
    print(
        "\nEach host above appears in the provider harness collection or its seeded\n"
        "provider config. Either add it to that job's allowed-endpoints, or - if the\n"
        "provider dereferences it server-side and the runner never dials it - add it to\n"
        "NOT_DIALLED_HOSTS in this script with the reason.",
        file=sys.stderr,
    )
    sys.exit(1)

if checked == 0:
    print("OK: no harness-running blocked-egress jobs found.")
else:
    print(
        f"OK: all {checked} harness-running blocked-egress job(s) allowlist the "
        f"{len(required)} host(s) the harness dials."
    )
PY
