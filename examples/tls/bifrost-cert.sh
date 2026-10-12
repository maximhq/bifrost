#!/usr/bin/env bash
# bifrost-cert.sh — obtain and renew a certificate for Bifrost's HTTPS listener.
#
# Wraps lego (https://go-acme.github.io/lego/), a single-binary ACME client, and
# installs the result where bifrost-http reads it (-tls-cert / -tls-key).
# Bifrost re-reads those files when they change, so renewals need no restart.
#
# Safe to run repeatedly, from cron or the systemd timer next to this script:
# it obtains a certificate the first time, renews it only when due, and only
# touches the installed files when the certificate actually changed.
#
#   bifrost-cert.sh install-lego            # optional: fetch a verified lego binary
#   CF_DNS_API_TOKEN=... bifrost-cert.sh -d llm.example.com
#
# Every option can also be set in the environment (or a systemd EnvironmentFile),
# shown in brackets below. DNS provider credentials are read from the
# environment under the names lego documents for that provider.
set -euo pipefail

LEGO_INSTALL_VERSION="${LEGO_INSTALL_VERSION:-5.5.2}"

usage() {
  cat <<'EOF'
Usage:
  bifrost-cert.sh [options] [-- extra lego run flags]
  bifrost-cert.sh install-lego [--version X.Y.Z] [--dir /usr/local/bin]

Options:
  -d, --domain NAME     Domain to certify; repeat for more names (first is the
                        primary). [BIFROST_CERT_DOMAINS, comma-separated]
  -m, --email EMAIL     Optional contact address for the ACME account, which
                        the CA may use for account and policy notices.
                        [BIFROST_CERT_EMAIL]
      --dns PROVIDER    Solve DNS-01 with a lego DNS provider (default:
                        cloudflare). Works for hosts that are not reachable
                        from the internet. See `lego dnshelp`. [BIFROST_CERT_DNS]
      --http [ADDR]     Solve HTTP-01 instead, listening on ADDR (default :80).
                        The host must be reachable on port 80 from the internet.
                        [BIFROST_CERT_HTTP=ADDR]
      --out DIR         Where to install tls.crt and tls.key, the paths to give
                        bifrost-http (default: /etc/bifrost/tls). [BIFROST_CERT_OUT]
      --state DIR       lego's account and certificate store (default:
                        /var/lib/bifrost-cert). Back it up. [BIFROST_CERT_STATE]
      --server URL      ACME directory URL or lego shortcode (default:
                        letsencrypt). [BIFROST_CERT_SERVER]
      --staging         Use Let's Encrypt staging; certificates are untrusted
                        but rate limits are generous. Try this first.
      --owner USER[:GROUP]
                        chown the installed files, for a Bifrost that does not
                        run as root (the Docker image runs as 1000:0).
                        [BIFROST_CERT_OWNER]
      --lego PATH       lego binary (default: lego on PATH). [LEGO_BIN]
  -h, --help            Show this help.

Exit status: 0 when a valid certificate is installed (renewed or not),
non-zero when it could not be obtained or did not pass the checks; in that
case the installed pair, if any, is left untouched.
EOF
}

log() { echo "bifrost-cert: $*" >&2; }
die() { log "error: $*"; exit 1; }

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

# install-lego downloads lego from its GitHub release and checks it against the
# release's published checksums before installing.
install_lego() {
  local version="$LEGO_INSTALL_VERSION" dir=/usr/local/bin
  while [ $# -gt 0 ]; do
    case "$1" in
      --version) version="${2:?--version needs a value}"; shift 2 ;;
      --dir) dir="${2:?--dir needs a value}"; shift 2 ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown install-lego option: $1" ;;
    esac
  done
  version="${version#v}"
  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;; Darwin) os=darwin ;; FreeBSD) os=freebsd ;;
    *) die "unsupported OS $(uname -s); download lego from https://github.com/go-acme/lego/releases" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; armv7l) arch=armv7 ;;
    *) die "unsupported architecture $(uname -m); download lego from https://github.com/go-acme/lego/releases" ;;
  esac
  local base="https://github.com/go-acme/lego/releases/download/v${version}"
  local tarball="lego_v${version}_${os}_${arch}.tar.gz" sums="lego_${version}_checksums.txt"
  # Global, not local: the EXIT trap runs after this function has returned.
  lego_tmp="$(mktemp -d)"; trap 'rm -rf "$lego_tmp"' EXIT
  local tmp="$lego_tmp"
  log "downloading lego v${version} for ${os}/${arch}"
  curl -fsSL -o "$tmp/$tarball" "$base/$tarball" || die "download failed: $base/$tarball"
  curl -fsSL -o "$tmp/$sums" "$base/$sums" || die "download failed: $base/$sums"
  local want got
  want="$(awk -v f="$tarball" '$2 == f {print $1}' "$tmp/$sums")"
  got="$(sha256 "$tmp/$tarball" | awk '{print $1}')"
  [ -n "$want" ] || die "$tarball is not listed in $sums"
  [ "$want" = "$got" ] || die "checksum mismatch for $tarball (want $want, got $got)"
  tar -xzf "$tmp/$tarball" -C "$tmp" lego
  install -d "$dir"
  install -m 0755 "$tmp/lego" "$dir/lego"
  log "installed $("$dir/lego" --version) at $dir/lego"
}

if [ "${1:-}" = "install-lego" ]; then
  shift; install_lego "$@"; exit 0
fi

domains=()
IFS=',' read -r -a domains <<< "${BIFROST_CERT_DOMAINS:-}"
cli_domains=()
email="${BIFROST_CERT_EMAIL:-}"
dns="${BIFROST_CERT_DNS:-}"
http="${BIFROST_CERT_HTTP:-}"
out="${BIFROST_CERT_OUT:-/etc/bifrost/tls}"
state="${BIFROST_CERT_STATE:-/var/lib/bifrost-cert}"
server="${BIFROST_CERT_SERVER:-letsencrypt}"
owner="${BIFROST_CERT_OWNER:-}"
lego="${LEGO_BIN:-lego}"
extra=()

while [ $# -gt 0 ]; do
  case "$1" in
    -d|--domain) cli_domains+=("${2:?$1 needs a value}"); shift 2 ;;
    -m|--email) email="${2:?$1 needs a value}"; shift 2 ;;
    --dns) dns="${2:?--dns needs a provider}"; http=""; shift 2 ;;
    --http)
      dns=""
      if [ $# -gt 1 ] && [ "${2#-}" = "$2" ]; then http="$2"; shift 2; else http=":80"; shift; fi ;;
    --out) out="${2:?--out needs a value}"; shift 2 ;;
    --state) state="${2:?--state needs a value}"; shift 2 ;;
    --server) server="${2:?--server needs a value}"; shift 2 ;;
    --staging) server="letsencrypt-staging"; shift ;;
    --owner) owner="${2:?--owner needs a value}"; shift 2 ;;
    --lego) lego="${2:?--lego needs a value}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    --) shift; extra=("$@"); break ;;
    *) usage >&2; die "unknown option: $1" ;;
  esac
done
[ ${#cli_domains[@]} -gt 0 ] && domains=("${cli_domains[@]}")
clean=()
for d in "${domains[@]}"; do d="${d// /}"; [ -n "$d" ] && clean+=("$d"); done
domains=("${clean[@]+"${clean[@]}"}")

[ ${#domains[@]} -gt 0 ] || { usage >&2; die "at least one --domain is required"; }
[ -n "$dns" ] || [ -n "$http" ] || dns=cloudflare
command -v "$lego" >/dev/null 2>&1 || die "lego not found; run '$0 install-lego' or see https://go-acme.github.io/lego/installation/"
command -v openssl >/dev/null 2>&1 || die "openssl is required to check the certificate before installing it"

primary="${domains[0]}"
# lego names its files after the primary domain, with a leading wildcard
# replaced by an underscore.
name="${primary/#\*/_}"

args=(run --accept-tos --server "$server" --path "$state")
[ -n "$email" ] && args+=(--email "$email")
for d in "${domains[@]}"; do args+=(--domains "$d"); done
if [ -n "$dns" ]; then args+=(--dns "$dns"); else args+=(--http --http.address "$http"); fi

umask 077
install -d -m 0700 "$state"
log "obtaining or renewing ${domains[*]} via ${dns:+DNS-01 ($dns)}${http:+HTTP-01 ($http)} from $server"
"$lego" "${args[@]}" "${extra[@]+"${extra[@]}"}" || die "lego could not obtain or renew the certificate; the installed pair, if any, is unchanged"

crt="$state/certificates/$name.crt"
key="$state/certificates/$name.key"
[ -s "$crt" ] && [ -s "$key" ] || die "lego reported success but $crt or $key is missing"

# Check the pair before it goes anywhere near Bifrost.
pub_of_crt="$(openssl x509 -in "$crt" -noout -pubkey | openssl pkey -pubin -outform DER | sha256 | awk '{print $1}')"
pub_of_key="$(openssl pkey -in "$key" -pubout -outform DER | sha256 | awk '{print $1}')"
[ "$pub_of_crt" = "$pub_of_key" ] || die "the key does not match the certificate"
sans="$(openssl x509 -in "$crt" -noout -ext subjectAltName 2>/dev/null)"
for d in "${domains[@]}"; do
  grep -qF "DNS:$d" <<< "$sans" || die "the certificate does not cover $d"
done
openssl x509 -in "$crt" -noout -checkend 86400 >/dev/null || die "the certificate expires within a day"

install -d -m 0755 "$out"
if cmp -s "$crt" "$out/tls.crt" && cmp -s "$key" "$out/tls.key"; then
  log "installed certificate is current (expires $(openssl x509 -in "$out/tls.crt" -noout -enddate | cut -d= -f2))"
  exit 0
fi

# Stage next to the targets so the renames are atomic, then swap the key first
# and the certificate second: Bifrost keeps serving the old pair until the two
# files match again.
install -m 0600 "$key" "$out/.tls.key.new"
install -m 0644 "$crt" "$out/.tls.crt.new"
if [ -n "$owner" ]; then
  chown "$owner" "$out/.tls.key.new" "$out/.tls.crt.new"
  chmod 0640 "$out/.tls.key.new"
fi
mv -f "$out/.tls.key.new" "$out/tls.key"
mv -f "$out/.tls.crt.new" "$out/tls.crt"
log "installed $out/tls.crt and $out/tls.key (expires $(openssl x509 -in "$out/tls.crt" -noout -enddate | cut -d= -f2)); Bifrost picks them up within a minute"
