#!/usr/bin/env bash
# Check every workflow against package mirrors and provider-harness destinations.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
exec bash "$SCRIPT_DIR/run-security-tool.sh" egresspolicy "$REPO_ROOT" "$@"
