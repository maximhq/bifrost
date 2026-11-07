# Railway template runbook

Railway templates are composed in the Railway dashboard, not from a file in this repository. This runbook is the exact composition so the published template matches the other one-click paths, plus what to do at release time.

## Compose the template

In the Maxim Railway workspace open **Settings → Templates → New Template** and add two services.

### Service 1: `Postgres`

- Source: the Railway **PostgreSQL** database service.
- Name it exactly `Postgres`; the reference variables below depend on that name.
- Attach the default volume Railway proposes.

### Service 2: `bifrost`

- Source: **Docker Image** `maximhq/bifrost:latest`. Each new template deploy pulls the newest release; operators pin a tag in the service settings afterwards.
- Health check path: `/health`.
- Replicas: 1. OSS replicas do not share live configuration.
- No custom start command. The image entrypoint reads the variables below.
- Variables (paste the JSON from [`../config.postgres.json`](../config.postgres.json) into `BIFROST_CONFIG_JSON`):

| Variable | Value |
|---|---|
| `APP_HOST` | `0.0.0.0` |
| `APP_PORT` | `8080` |
| `PORT` | `8080` |
| `BIFROST_CONFIG_JSON` | contents of `deploy/config.postgres.json` |
| `BIFROST_ENCRYPTION_KEY` | `${{secret(48)}}` |
| `BIFROST_SETUP_TOKEN` | `${{secret(40)}}` |
| `PG_HOST` | `${{Postgres.PGHOST}}` |
| `PG_PORT` | `${{Postgres.PGPORT}}` |
| `PG_USER` | `${{Postgres.PGUSER}}` |
| `PG_PASSWORD` | `${{Postgres.PGPASSWORD}}` |
| `PG_DATABASE` | `${{Postgres.PGDATABASE}}` |
| `PG_SSL_MODE` | `prefer` |

Add a public domain to the `bifrost` service in the template so deploys expose the dashboard immediately.

### Template metadata

- Name: `Bifrost`
- Description: `Bifrost is a blazing fast AI gateway unifying every model provider (OpenAI, Anthropic, Bedrock, Vertex, Mistral, Ollama…) behind one OpenAI-compatible API, with a built-in dashboard, logging, caching, and governance. This template deploys Bifrost with a Postgres backed config and log store.`
- Category: `AI/ML` (or `Developer Tools`)
- Readme link: `https://docs.getbifrost.ai/deployment-guides/platforms/railway`

## Publish

Publish from the Templates page. Publishing is what makes the template deployable through a button, lists it on the marketplace, and makes it eligible for Railway's open-source kickback. Copy the template code from the published URL (`https://railway.com/new/template/<code>`).

Paste the code into the three places that carry the button, replacing `RAILWAY_TEMPLATE_CODE`:

- `docs/quickstart/gateway/setting-up.mdx`
- `docs/deployment-guides/platforms/railway.mdx`
- `README.md`

The button markup is:

```markdown
[![Deploy on Railway](https://railway.com/button.svg)](https://railway.com/new/template/<code>?utm_medium=integration&utm_source=button&utm_campaign=bifrost)
```

## Verify a deploy

1. Deploy the template into a scratch project.
2. Wait for `bifrost` to report healthy, open its public domain, and create the first admin account with the value of `BIFROST_SETUP_TOKEN` from the service variables.
3. Add a provider key and a virtual key, send one chat completion, and confirm it appears under Logs.
4. Delete the scratch project.

## At release time

Nothing to update: the template tracks `maximhq/bifrost:latest`. If the image name or the required variables ever change, edit the template in the dashboard and republish.

## Enterprise

Deploy the same template, then in the `bifrost` service settings change the image to the registry path issued for your organization and add the registry credentials. The variables and the Postgres service stay as they are.
