#!/usr/bin/env bash
set -euo pipefail

# Exercise apt-sources-https.sh against fixture apt trees shaped like the two
# runner images: the arm64 Ubuntu 22.04 base image (one-line sources.list on
# ports.ubuntu.com) and the amd64 Ubuntu 24.04 image (deb822 ubuntu.sources on
# the Azure-local mirror, plus third-party repos). Runs unprivileged.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

passed=0
fail() { cat "$WORK/out" >&2; echo "FAIL: $1" >&2; exit 1; }
ok() { echo "ok - $1"; passed=$((passed + 1)); }
run() { APT_ROOT="$WORK/apt" APT_SUDO="" "$BASH" "$SCRIPT_DIR/apt-sources-https.sh" > "$WORK/out" 2>&1; }
reset() { rm -rf "$WORK/apt" && mkdir -p "$WORK/apt/sources.list.d"; }

reset
cat > "$WORK/apt/sources.list" <<'EOF'
## See http://help.ubuntu.com/community/UpgradeNotes for how to upgrade
deb http://ports.ubuntu.com/ubuntu-ports jammy main restricted
deb http://azure.ports.ubuntu.com/ubuntu-ports jammy-updates main
deb http://ports.ubuntu.com/ubuntu-ports jammy-security main
EOF
run || fail "arm64 sources errored"
grep -qx "deb https://ports.ubuntu.com/ubuntu-ports jammy main restricted" "$WORK/apt/sources.list" || fail "ports not switched"
grep -qx "deb https://ports.ubuntu.com/ubuntu-ports jammy-updates main" "$WORK/apt/sources.list" || fail "azure.ports not mapped to ports over https"
grep -qx "## See http://help.ubuntu.com/community/UpgradeNotes for how to upgrade" "$WORK/apt/sources.list" || fail "comment rewritten"
ok "arm64 one-line sources move to https://ports.ubuntu.com and comments are left alone"

reset
cat > "$WORK/apt/sources.list.d/ubuntu.sources" <<'EOF'
Types: deb
URIs: http://azure.archive.ubuntu.com/ubuntu/
Suites: noble noble-updates noble-backports noble-security
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF
cat > "$WORK/apt/sources.list.d/microsoft-prod.list" <<'EOF'
deb [arch=amd64,arm64,armhf] https://packages.microsoft.com/ubuntu/24.04/prod noble main
EOF
cp "$WORK/apt/sources.list.d/microsoft-prod.list" "$WORK/ms.before"
run || fail "amd64 sources errored"
grep -qx "URIs: https://archive.ubuntu.com/ubuntu/" "$WORK/apt/sources.list.d/ubuntu.sources" || fail "azure.archive not mapped to https://archive.ubuntu.com"
cmp -s "$WORK/ms.before" "$WORK/apt/sources.list.d/microsoft-prod.list" || fail "third-party repo touched"
ok "amd64 deb822 Azure mirror moves to https://archive.ubuntu.com and other repos are untouched"

cp "$WORK/apt/sources.list.d/ubuntu.sources" "$WORK/ubuntu.before"
run || fail "second run errored"
cmp -s "$WORK/ubuntu.before" "$WORK/apt/sources.list.d/ubuntu.sources" || fail "second run changed the file"
! grep -q "switched" "$WORK/out" || fail "second run reported a switch"
ok "running twice is a no-op"

reset
cat > "$WORK/apt/sources.list.d/ubuntu.sources" <<'EOF'
Types: deb
URIs: mirror+file:/etc/apt/apt-mirrors.txt
Suites: noble noble-updates noble-backports noble-security
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
EOF
printf 'http://azure.archive.ubuntu.com/ubuntu/\tpriority:1\nhttps://archive.ubuntu.com/ubuntu/\tpriority:2\nhttps://security.ubuntu.com/ubuntu/\tpriority:3\n' > "$WORK/apt/apt-mirrors.txt"
run || fail "mirror list errored"
! grep -q "azure.archive" "$WORK/apt/apt-mirrors.txt" || fail "azure http mirror still listed"
grep -q "^https://archive.ubuntu.com/ubuntu/" "$WORK/apt/apt-mirrors.txt" || fail "archive mirror dropped"
grep -q "^https://security.ubuntu.com/ubuntu/" "$WORK/apt/apt-mirrors.txt" || fail "security mirror dropped"
ok "mirror+file list drops the Azure http mirror and keeps the https mirrors"

reset
printf 'http://us.archive.ubuntu.com/ubuntu/\tpriority:1\n' > "$WORK/apt/apt-mirrors.txt"
if run; then fail "accepted an unmapped http ubuntu mirror in the mirror list"; fi
grep -q "us.archive.ubuntu.com" "$WORK/out" || fail "error does not name the leftover mirror"
ok "an unmapped http:// mirror in the mirror list fails loudly"

reset
printf 'deb http://security.ubuntu.com/ubuntu noble-security main\n' > "$WORK/apt/sources.list"
run || fail "security sources errored"
grep -qx "deb https://security.ubuntu.com/ubuntu noble-security main" "$WORK/apt/sources.list" || fail "security not switched"
ok "security.ubuntu.com stays on its host over https"

reset
printf 'deb http://us.archive.ubuntu.com/ubuntu noble main\n' > "$WORK/apt/sources.list"
if run; then fail "accepted an unmapped http ubuntu mirror"; fi
grep -q "us.archive.ubuntu.com" "$WORK/out" || fail "error does not name the leftover source"
ok "an unmapped http:// ubuntu.com mirror fails loudly"

rm -rf "$WORK/apt" && mkdir -p "$WORK/apt"
run || fail "empty apt root errored"
ok "no apt sources is a no-op"

echo "$passed passed"
