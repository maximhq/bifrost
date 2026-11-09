# Koyeb one-click deploy

Koyeb's "Deploy to Koyeb" button is a URL: every setting travels as a query parameter and Koyeb shows them on a review form before creating the service. Bifrost ships the button with the published image, the shared [`config.postgres.json`](../config.postgres.json) base64-encoded into `BIFROST_CONFIG_B64`, port `8080`, and three placeholder values the user replaces on the form:

| Variable | What to enter |
|---|---|
| `DATABASE_URL` | The connection string of a Koyeb Postgres database (`koyeb database create` or the dashboard), or any PostgreSQL 16+ reachable from Koyeb. The entrypoint derives `PG_*` from it |
| `BIFROST_ENCRYPTION_KEY` | A long random string. Never change it afterwards |
| `BIFROST_SETUP_TOKEN` | A random string used once to create the first admin account |

The button cannot create the database itself, so the Postgres step happens first.

## Button

```markdown
[![Deploy to Koyeb](https://www.koyeb.com/static/images/deploy/button.svg)](https://app.koyeb.com/deploy?type=docker&image=docker.io%2Fmaximhq%2Fbifrost%3Alatest&name=bifrost&service_type=web&instance_type=small&region=fra&ports=8080%3Bhttp%3B%2F&env[APP_HOST]=0.0.0.0&env[BIFROST_CONFIG_B64]=ewogICIkc2NoZW1hIjogImh0dHBzOi8vd3d3LmdldGJpZnJvc3QuYWkvc2NoZW1hIiwKICAiZW5jcnlwdGlvbl9rZXkiOiAiZW52LkJJRlJPU1RfRU5DUllQVElPTl9LRVkiLAogICJzZXR1cF90b2tlbiI6ICJlbnYuQklGUk9TVF9TRVRVUF9UT0tFTiIsCiAgImNsaWVudCI6IHsKICAgICJlbmZvcmNlX2F1dGhfb25faW5mZXJlbmNlIjogdHJ1ZQogIH0sCiAgImNvbmZpZ19zdG9yZSI6IHsKICAgICJlbmFibGVkIjogdHJ1ZSwKICAgICJ0eXBlIjogInBvc3RncmVzIiwKICAgICJjb25maWciOiB7CiAgICAgICJob3N0IjogImVudi5QR19IT1NUIiwKICAgICAgInBvcnQiOiAiZW52LlBHX1BPUlQiLAogICAgICAidXNlciI6ICJlbnYuUEdfVVNFUiIsCiAgICAgICJwYXNzd29yZCI6ICJlbnYuUEdfUEFTU1dPUkQiLAogICAgICAiZGJfbmFtZSI6ICJlbnYuUEdfREFUQUJBU0UiLAogICAgICAic3NsX21vZGUiOiAiZW52LlBHX1NTTF9NT0RFIgogICAgfQogIH0sCiAgImxvZ3Nfc3RvcmUiOiB7CiAgICAiZW5hYmxlZCI6IHRydWUsCiAgICAidHlwZSI6ICJwb3N0Z3JlcyIsCiAgICAiY29uZmlnIjogewogICAgICAiaG9zdCI6ICJlbnYuUEdfSE9TVCIsCiAgICAgICJwb3J0IjogImVudi5QR19QT1JUIiwKICAgICAgInVzZXIiOiAiZW52LlBHX1VTRVIiLAogICAgICAicGFzc3dvcmQiOiAiZW52LlBHX1BBU1NXT1JEIiwKICAgICAgImRiX25hbWUiOiAiZW52LlBHX0RBVEFCQVNFIiwKICAgICAgInNzbF9tb2RlIjogImVudi5QR19TU0xfTU9ERSIKICAgIH0KICB9Cn0K&env[BIFROST_ENCRYPTION_KEY]=CHANGE_ME_LONG_RANDOM_STRING&env[BIFROST_SETUP_TOKEN]=CHANGE_ME_RANDOM_STRING&env[DATABASE_URL]=CHANGE_ME_postgres%3A%2F%2Fuser%3Apassword%40host%3A5432%2Fdb%3Fsslmode%3Drequire)
```

## Regenerate the URL

The URL embeds the config document, so regenerate it whenever `deploy/config.postgres.json` changes:

```bash
python3 - <<'PY'
import base64, urllib.parse
cfg = base64.b64encode(open("deploy/config.postgres.json", "rb").read()).decode()
params = [
    ("type", "docker"), ("image", "docker.io/maximhq/bifrost:latest"), ("name", "bifrost"),
    ("service_type", "web"), ("instance_type", "small"), ("region", "fra"), ("ports", "8080;http;/"),
    ("env[APP_HOST]", "0.0.0.0"), ("env[BIFROST_CONFIG_B64]", cfg),
    ("env[BIFROST_ENCRYPTION_KEY]", "CHANGE_ME_LONG_RANDOM_STRING"),
    ("env[BIFROST_SETUP_TOKEN]", "CHANGE_ME_RANDOM_STRING"),
    ("env[DATABASE_URL]", "CHANGE_ME_postgres://user:password@host:5432/db?sslmode=require"),
]
print("https://app.koyeb.com/deploy?" + "&".join(f"{urllib.parse.quote(k, safe='[]')}={urllib.parse.quote(v, safe='')}" for k, v in params))
PY
```

Paste the result into `docs/deployment-guides/platforms/koyeb.mdx`, `docs/quickstart/gateway/setting-up.mdx` and `README.md`.

## Enterprise

Replace `image=docker.io%2Fmaximhq%2Fbifrost%3Alatest` with your organization's registry path. The image is private, so on the review form open the registry settings and add the registry secret (server, username, token) before you click deploy; a service created without it cannot pull the image and the first deployment fails. Koyeb stores the credential as a secret, never in the button URL.
