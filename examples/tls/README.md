# Serving HTTPS directly from Bifrost

Helpers for running `bifrost-http` with its own certificate (`-tls-cert`,
`-tls-key`, `-tls-port`), for single hosts where a reverse proxy would only be
there to hold the certificate. A reverse proxy remains the recommended setup
for internet-facing and multi-replica deployments.

Full guide: https://docs.getbifrost.ai/deployment-guides/how-to/native-tls

| File | What it is |
|------|------------|
| `bifrost-cert.sh` | Obtains and renews a Let's Encrypt certificate with [lego](https://go-acme.github.io/lego/) (DNS-01 via Cloudflare and 150+ other DNS providers, or HTTP-01) and installs it where Bifrost reads it. Safe to run repeatedly. `bifrost-cert.sh install-lego` fetches a checksum-verified lego. |
| `cert.env.example` | Settings and DNS credentials for the script, as a systemd `EnvironmentFile`. |
| `systemd/bifrost-cert.service`, `systemd/bifrost-cert.timer` | Runs the script twice a day, sandboxed to the two directories it writes. |
| `systemd/bifrost-tls.conf` | Drop-in that points a `bifrost.service` at the certificate and lets a non-root user bind 443. |
| `docker/docker-compose.yml` | The official image serving HTTPS, with the certificate directory mounted. |
| `k8s/certificate.yaml` | cert-manager `ClusterIssuer` (Cloudflare DNS-01), `Certificate`, and the Deployment fields that mount it. |

Quick start on a systemd host, with DNS at Cloudflare:

```bash
sudo install -m 0755 bifrost-cert.sh /usr/local/bin/
sudo bifrost-cert.sh install-lego
sudo install -D -m 0600 cert.env.example /etc/bifrost/cert.env    # set domain and CF_DNS_API_TOKEN
sudo install -m 0644 systemd/bifrost-cert.service systemd/bifrost-cert.timer /etc/systemd/system/
sudo systemctl daemon-reload && sudo systemctl start bifrost-cert.service
sudo systemctl enable --now bifrost-cert.timer
sudo install -D -m 0644 systemd/bifrost-tls.conf /etc/systemd/system/bifrost.service.d/tls.conf
sudo systemctl daemon-reload && sudo systemctl restart bifrost
```

Bifrost re-reads the certificate files when they change, so renewals take
effect without a restart.
