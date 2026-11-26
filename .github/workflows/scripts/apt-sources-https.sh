#!/usr/bin/env bash
set -euo pipefail

# Points the runner's Ubuntu apt sources at HTTPS. Run it before every
# `apt-get update` in CI.
#
# Runner images ship apt sources on plain HTTP. apt's GPG-signed InRelease and
# per-file SHA256 already stop a tampered package, but HTTP still shows anyone on
# the path what we install, lets them replay a stale (validly signed) InRelease
# to hide security updates, and forces a port-80 entry into every harden-runner
# allowlist. After this runs, apt needs only :443.
#
#   http://azure.archive.ubuntu.com  -> https://archive.ubuntu.com
#       The Azure-local mirror the amd64 images use serves no HTTPS at all (443
#       is closed), so it is swapped for Canonical's primary archive, which
#       carries the same pockets, -security included.
#   http://[azure.]ports.ubuntu.com  -> https://ports.ubuntu.com   (arm64)
#   http://archive|security.ubuntu.com -> https:// on the same host
#
# Some amd64 images point ubuntu.sources at `mirror+file:/etc/apt/apt-mirrors.txt`
# instead of a URL. That list puts http://azure.archive.ubuntu.com first and
# https://archive.ubuntu.com after it, so the Azure line is deleted rather than
# rewritten (a rewrite would only duplicate the archive entry). Left in, apt
# tries :80 first, harden-runner drops it, and every fetch stalls on a timeout.
#
# Any other http:// source on an ubuntu.com host is an error rather than a
# silent pass: it would reach for :80, which the allowlists no longer carry, and
# fail as an opaque mirror timeout. check-egress-allowlist.sh requires the
# matching :443 hosts and that every apt-using step runs this script first.
#
# APT_ROOT (default /etc/apt) and APT_SUDO (default sudo; empty runs unprivileged)
# exist for apt-sources-https.test.sh.

APT_ROOT="${APT_ROOT:-/etc/apt}"
APT_SUDO="${APT_SUDO-sudo}"

files=()
for f in "$APT_ROOT/sources.list" "$APT_ROOT"/sources.list.d/*.list "$APT_ROOT"/sources.list.d/*.sources; do
  [[ -f "$f" ]] && files+=("$f")
done
mirrors="$APT_ROOT/apt-mirrors.txt"
if [[ ${#files[@]} -eq 0 && ! -f "$mirrors" ]]; then
  echo "apt-sources-https: no apt sources under $APT_ROOT"
  exit 0
fi

# Written through a temp copy instead of sed -i so it behaves the same under GNU
# and BSD sed, and `cp` onto the existing file keeps its owner and mode.
tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

if [[ -f "$mirrors" ]]; then
  sed -E '/^[[:space:]]*http:\/\/azure\.archive\.ubuntu\.com([\/[:space:]]|$)/d' "$mirrors" > "$tmp"
  if ! cmp -s "$tmp" "$mirrors"; then
    ${APT_SUDO:+$APT_SUDO} cp "$tmp" "$mirrors"
    echo "apt-sources-https: dropped the Azure http mirror from $mirrors"
  fi
fi
for f in "${files[@]}"; do
  sed -E \
    -e 's#http://azure\.archive\.ubuntu\.com([/[:space:]]|$)#https://archive.ubuntu.com\1#g' \
    -e 's#http://(azure\.)?ports\.ubuntu\.com([/[:space:]]|$)#https://ports.ubuntu.com\2#g' \
    -e 's#http://(archive|security)\.ubuntu\.com([/[:space:]]|$)#https://\1.ubuntu.com\2#g' \
    "$f" > "$tmp"
  if ! cmp -s "$tmp" "$f"; then
    ${APT_SUDO:+$APT_SUDO} cp "$tmp" "$f"
    echo "apt-sources-https: switched $f to https"
  fi
done

[[ -f "$mirrors" ]] && files+=("$mirrors")

# Comment lines are skipped: stock sources.list carries "http://help.ubuntu.com"
# links in comments that apt never fetches.
if leftover="$(grep -HnE '^[^#]*http://[A-Za-z0-9.-]*ubuntu\.com' "${files[@]}")"; then
  echo "::error::apt sources still use http:// on an ubuntu.com host this script does not map to https:" >&2
  echo "$leftover" >&2
  exit 1
fi
