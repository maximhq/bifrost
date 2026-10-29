#!/usr/bin/env bash
set -euo pipefail

# Exercise the real installer offline: per-component skip-when-present, the
# packages each component asks apt for, and the Docker key pin failing closed
# before Docker's repo is ever added. sudo is a stub that records each command
# instead of running it, so nothing here touches the host.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin" "$WORK/base"

# Only these real tools are visible, so the host's own jq/gcc/docker cannot leak in.
for tool in awk bash cat grep mktemp rm seq; do ln -s "$(command -v "$tool")" "$WORK/base/$tool"; done

printf 'VERSION_CODENAME=jammy\n' > "$WORK/os-release"
GOOD_FPR=9DC858229FC7DD38854AE2D88D81803C0EBFCD88

cat > "$WORK/base/sudo" <<'EOF'
#!/usr/bin/env bash
echo "sudo $*" >> "$TEST_LOG"
[[ "$1" == tee ]] && cat >> "$TEST_LOG"
exit 0
EOF
# Like the real cache, the match is followed by a long tail, so a reader that
# stops at the first match (grep -q) SIGPIPEs this writer under pipefail.
cat > "$WORK/base/ldconfig" <<'EOF'
#!/usr/bin/env bash
[[ "${TEST_HAS_LIBATOMIC:-0}" == 1 ]] && echo "libatomic.so.1 (libc6,AArch64) => /lib/aarch64-linux-gnu/libatomic.so.1"
for i in $(seq 1 20000); do echo "libstub$i.so.1 (libc6,AArch64) => /lib/aarch64-linux-gnu/libstub$i.so.1"; done
EOF
cat > "$WORK/base/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$2" == "https://download.docker.com/linux/ubuntu/gpg" ]]
printf 'key fixture\n' > "$4"
EOF
cat > "$WORK/base/gpg" <<'EOF'
#!/usr/bin/env bash
for fpr in $TEST_KEY_PRIMARIES; do
  echo "pub:-:4096:1:8D81803C0EBFCD88:1487788586:::-:::scESA::::::23::0:"
  echo "fpr:::::::::${fpr}:"
  echo "sub:-:4096:1:7EA0A9C3F273FCD8:1487792064:::::s::::::23:"
  echo "fpr:::::::::D3306A018370199E527AE7997EA0A9C3F273FCD8:"
done
EOF
cat > "$WORK/base/dpkg" <<'EOF'
#!/usr/bin/env bash
echo arm64
EOF
# A docker stub whose buildx/compose subcommands answer only when the full set is installed.
cat > "$WORK/docker-full" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$WORK/docker-bare" <<'EOF'
#!/usr/bin/env bash
[[ "$1" == version ]]
EOF
cat > "$WORK/present" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$WORK/base/"* "$WORK/docker-full" "$WORK/docker-bare" "$WORK/present"

passed=0
# run <case-name> <tools on PATH> -- <components>...
run() {
  local tools="$1"; shift
  rm -rf "${WORK:?}/bin" && mkdir -p "$WORK/bin"
  for tool in $tools; do
    case "$tool" in
      docker) ln -s "$WORK/docker-full" "$WORK/bin/docker" ;;
      docker-bare) ln -s "$WORK/docker-bare" "$WORK/bin/docker" ;;
      *) ln -s "$WORK/present" "$WORK/bin/$tool" ;;
    esac
  done
  export TEST_LOG="$WORK/log"
  : > "$TEST_LOG"
  PATH="$WORK/bin:$WORK/base" OS_RELEASE="$WORK/os-release" \
    "$BASH" "$SCRIPT_DIR/install-arm64-runner-deps.sh" "$@" > "$WORK/out" 2>&1
}
fail() { cat "$WORK/out" "$TEST_LOG" >&2; echo "FAIL: $1" >&2; exit 1; }
ok() { echo "ok - $1"; passed=$((passed + 1)); }

export TEST_HAS_LIBATOMIC=1 TEST_KEY_PRIMARIES="$GOOD_FPR"
run "docker jq gh make gcc g++" node docker build jq gh || fail "full image errored"
[[ ! -s "$TEST_LOG" ]] || fail "full image still called sudo"
ok "a full runner image installs nothing"

export TEST_HAS_LIBATOMIC=0
run "" node || fail "node errored"
grep -qx "sudo apt-get install -y --no-install-recommends libatomic1" "$TEST_LOG" || fail "node did not install exactly libatomic1"
ok "node installs libatomic1 when ldconfig lacks it"

run "make gcc" build jq gh || fail "build errored"
grep -qx "sudo apt-get install -y --no-install-recommends make gcc g++ libc6-dev jq gh" "$TEST_LOG" || fail "build/jq/gh package list wrong"
ok "build installs the toolchain when any of make/gcc/g++ is missing, plus jq and gh"

run "docker-bare" docker || fail "docker errored"
grep -qx "sudo install -D -m 0644 .* /etc/apt/keyrings/docker.asc" "$TEST_LOG" || fail "key not installed"
grep -qx "deb \[arch=arm64 signed-by=/etc/apt/keyrings/docker.asc\] https://download.docker.com/linux/ubuntu jammy stable" "$TEST_LOG" || fail "repo line wrong"
grep -qx "sudo apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin" "$TEST_LOG" || fail "docker packages wrong"
grep -qx "sudo chmod 666 /var/run/docker.sock" "$TEST_LOG" || fail "socket not opened"
ok "docker without buildx/compose adds the pinned repo and installs the full set"

for bad in "0000000000000000000000000000000000000000" "$GOOD_FPR 1111111111111111111111111111111111111111"; do
  export TEST_KEY_PRIMARIES="$bad"
  if run "docker-bare" docker; then fail "accepted key with primaries [$bad]"; fi
  ! grep -q "docker.list\|docker-ce" "$TEST_LOG" || fail "repo added despite bad key [$bad]"
  grep -q "do not match $GOOD_FPR" "$WORK/out" || fail "no fingerprint error for [$bad]"
done
ok "a wrong or extra primary key fails before Docker's repo is added"

export TEST_KEY_PRIMARIES="$GOOD_FPR"
if run "" nodejs; then fail "accepted unknown component"; fi
[[ ! -s "$TEST_LOG" ]] || fail "unknown component still called sudo"
ok "an unknown component fails without installing anything"

echo "$passed passed"
