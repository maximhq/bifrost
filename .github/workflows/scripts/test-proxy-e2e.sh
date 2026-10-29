#!/usr/bin/env bash
set -euo pipefail

# CI runner for the proxy e2e flow (tests/e2e/api/runners/proxy/run-proxy-matrix.sh):
# real provider calls through squid (http://), stunnel + squid (https://) and
# microsocks (socks5://), for the global, per-provider and environment proxy modes.
#
# The workflow installs squid, stunnel4 and microsocks and exports the provider
# secrets. This script turns on strict egress, so the gateway can reach nothing but
# loopback and a request that skips the proxy fails instead of passing, and hands off
# to the matrix runner. Exit code is the runner's: any failed cell fails the job.

if command -v readlink >/dev/null 2>&1 && readlink -f "$0" >/dev/null 2>&1; then
  SCRIPT_DIR="$(dirname "$(readlink -f "$0")")"
else
  SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd -P)"
fi
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd -P)"
cd "$REPO_ROOT"

export PROXY_E2E_STRICT_EGRESS="${PROXY_E2E_STRICT_EGRESS:-1}"
exec bash "$REPO_ROOT/tests/e2e/api/runners/proxy/run-proxy-matrix.sh"
