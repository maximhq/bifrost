#!/bin/bash
# Ollama server command: ollama-serve <model tag> <served name>.
# Ollama has no server-side auth, so it listens on loopback and nginx on :8000 checks PROXY_API_KEY as a bearer token.
# nginx starts only after the model is pulled and loaded, so /v1/models answering on :8000 means the model is there.
set -euo pipefail
tag=$1
name=$2
export OLLAMA_HOST=127.0.0.1:11434

if ! command -v nginx >/dev/null || ! command -v curl >/dev/null; then
  printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod +x /usr/sbin/policy-rc.d
  apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends nginx curl >/dev/null
  rm -f /usr/sbin/policy-rc.d
fi
rm -f /etc/nginx/sites-enabled/default
cat > /etc/nginx/conf.d/ollama.conf <<EOF
server {
  listen 8000;
  client_max_body_size 64m;
  location / {
    if (\$http_authorization != "Bearer ${PROXY_API_KEY}") { return 401; }
    proxy_pass http://127.0.0.1:11434;
    # Ollama rejects non-local Host headers while bound to loopback.
    proxy_set_header Host localhost:11434;
    proxy_http_version 1.1;
    proxy_buffering off;
    proxy_read_timeout 1h;
    proxy_send_timeout 1h;
  }
}
EOF
nginx -t -q

ollama serve &
server=$!
until ollama list >/dev/null 2>&1; do
  kill -0 "$server" 2>/dev/null || exit 1
  sleep 1
done
echo "[ollama] pulling ${tag}"
ollama pull "$tag"
[ "$tag" = "$name" ] || ollama cp "$tag" "$name"
echo "[ollama] loading ${name}"
ollama run "$name" "" </dev/null >/dev/null 2>&1 || echo "[ollama] WARNING: failed to load ${name}; the first request will load it"
nginx
echo "[ollama] serving ${tag} as ${name} on :8000"
wait "$server"