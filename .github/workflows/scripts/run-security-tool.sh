#!/usr/bin/env bash
# Workflow policies use a locked module; artifact tools use only the standard library.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tool="${1:?security tool is required}"
shift
export GOTOOLCHAIN=local GOWORK=off
case "$tool" in
  workflowpolicy|egresspolicy|prapproval|releaseapproval)
    cd "$SCRIPT_DIR/security"
    exec go run -mod=readonly "./$tool" "$@"
    ;;
  test)
    cd "$SCRIPT_DIR/security"
    exec go test -mod=readonly ./...
    ;;
  *)
    if [[ ! "$tool" =~ ^[a-z]+$ ]]; then echo 'Invalid security tool name' >&2; exit 1; fi
    export GO111MODULE=off
    exec go run "$SCRIPT_DIR/security/$tool/main.go" "$@"
    ;;
esac
