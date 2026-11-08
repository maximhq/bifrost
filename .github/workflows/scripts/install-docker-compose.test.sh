#!/usr/bin/env bash
set -euo pipefail

# Exercise the real installer offline: platform selection, release/checksum pairing,
# and fail-closed behavior before an executable can replace the destination.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"

cat > "$WORK/bin/uname" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  -s) echo "${TEST_OS:-Linux}" ;;
  -m) echo "$TEST_ARCH" ;;
esac
EOF
cat > "$WORK/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "$2" == "https://github.com/docker/compose/releases/download/v5.6.1/docker-compose-linux-${TEST_ARCH}" ]]
[[ "${TEST_DOWNLOAD_FAIL:-0}" == 0 ]]
printf 'verified compose fixture\n' > "$4"
EOF
cat > "$WORK/bin/sha256sum" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
read -r digest file
case "$TEST_ARCH" in
  x86_64) expected=47737f4236e6d3ccea7291995203f8f018058783b4c3a9469119811a0b5db8c0 ;;
  aarch64) expected=42cfa75f51ad0ccf290bad37899e0038a8c10a0069e85233ecc1d53e3ff1347d ;;
esac
[[ "$digest" == "$expected" && -s "$file" && "${TEST_BAD_CHECKSUM:-0}" == 0 ]]
EOF
chmod +x "$WORK/bin/"*

run() {
  PATH="$WORK/bin:$PATH" bash "$SCRIPT_DIR/install-docker-compose.sh" "$WORK/compose" > "$WORK/log" 2>&1
}

for arch in x86_64 aarch64; do
  export TEST_ARCH="$arch"
  if ! run; then
    cat "$WORK/log" >&2
    echo "FAIL: $arch did not use the security-fixed release and matching digest" >&2
    exit 1
  fi
  [[ -x "$WORK/compose" && "$(cat "$WORK/compose")" == 'verified compose fixture' ]]
  echo "ok - $arch installs v5.6.1 with its matching digest"
done

for failure in checksum download platform; do
  printf 'existing binary\n' > "$WORK/compose"
  export TEST_BAD_CHECKSUM=0 TEST_DOWNLOAD_FAIL=0 TEST_OS=Linux
  case "$failure" in
    checksum) export TEST_BAD_CHECKSUM=1 ;;
    download) export TEST_DOWNLOAD_FAIL=1 ;;
    platform) export TEST_OS=Darwin ;;
  esac
  if run; then
    echo "FAIL: installer accepted $failure failure" >&2
    exit 1
  fi
  [[ "$(cat "$WORK/compose")" == 'existing binary' ]]
  echo "ok - $failure failure preserves the existing binary"
done
echo '5 passed'
