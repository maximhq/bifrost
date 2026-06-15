# MCP Library - Community Catalog

This directory contains the community-curated catalog of [MCP (Model Context Protocol)](https://modelcontextprotocol.io) servers available in the Bifrost MCP Library.

Anyone can add an MCP server by editing [`servers.json`](./servers.json) and opening a pull request.

## Quick Start

### 1. Fork and clone the repository

```bash
git clone https://github.com/<your-username>/bifrost.git
cd bifrost
```

### 2. Add your server

Add one entry to the `servers` array in `community/mcp-library/servers.json`. Use the field reference below and copy the shape of nearby entries.

Bifrost generates the internal server slug from `name`. Do not add a `slug` field to catalog entries.

### 3. Check your change locally

```bash
jq empty community/mcp-library/servers.json
```

If you have `ajv-cli` installed, you can also validate against the schema:

```bash
npx --yes ajv-cli validate \
  -s community/mcp-library/schema.json \
  -d community/mcp-library/servers.json \
  --spec=draft7
```

### 4. Open a pull request

Target the `dev` branch. Use a descriptive title like `community: add <server-name> to MCP library`.

## Server Entry Reference

Each server entry is a JSON object in the `servers` array.

### HTTP or SSE Server

```json
{
  "name": "My Server",
  "description": "A short, factual description of what this server does.",
  "category": "Developer Tools",
  "connection_type": "http",
  "connection_url": "https://api.example.com/mcp",
  "auth_type": "headers",
  "required_header_keys": ["Authorization"],
  "icon_url": "https://example.com/icon.png",
  "docs_url": "https://docs.example.com",
  "publisher": "Your Name or Organization",
  "tags": ["api", "example"]
}
```

### STDIO Server

```json
{
  "name": "My STDIO Server",
  "description": "A local STDIO-based MCP server.",
  "category": "Developer Tools",
  "connection_type": "stdio",
  "stdio_config": {
    "command": "npx",
    "args": ["-y", "@example/mcp-server"],
    "envs": ["API_KEY"]
  },
  "auth_type": "none",
  "docs_url": "https://github.com/example/mcp-server",
  "publisher": "Your Name",
  "tags": ["local", "example"]
}
```

### Field Reference

| Field | Type | Required | Description |
| ----- | ---- | -------- | ----------- |
| `name` | `string` | yes | Human-readable display name. Bifrost derives the internal slug from this value. |
| `description` | `string` | no | Short summary of what the server does. |
| `category` | `string` | no | Grouping category used for filtering. Reuse an existing category when possible. |
| `connection_type` | `string` | yes | One of `http`, `stdio`, `sse`, or `inprocess`. |
| `connection_url` | `string` | for `http`/`sse` | The server endpoint URL. |
| `stdio_config` | `object` | for `stdio` | Launch configuration. See [`stdio_config` fields](#stdio_config-fields). |
| `auth_type` | `string` | no | One of `none`, `headers`, `oauth`, `per_user_oauth`, or `per_user_headers`. Defaults to `none` when omitted. |
| `required_header_keys` | `string[]` | no | Header names required for header-based authentication. Never include secret values. |
| `icon_url` | `string` | no | URL or local path to a square icon. Prefer PNG or SVG. |
| `docs_url` | `string` | no | Link to documentation, repository, or homepage. |
| `publisher` | `string` | no | Person or organization maintaining the server. |
| `tags` | `string[]` | no | Search and filtering tags. Keep them short and factual. |
| `metadata` | `object` | no | Additional key/value data. Use sparingly. |

#### `stdio_config` Fields

| Field | Type | Required | Description |
| ----- | ---- | -------- | ----------- |
| `command` | `string` | yes | Executable to launch, such as `npx`, `uvx`, `node`, or `python`. |
| `args` | `string[]` | no | Arguments passed to the command. |
| `envs` | `string[]` | no | Environment variable names the user must provide. Never include values. |

## Guidelines

### Do

- Add one server per pull request.
- Use a clear, stable `name`; changing it later changes Bifrost's generated internal slug.
- Write a concise, factual `description`.
- Include a `docs_url` so users can verify setup and requirements.
- Use `required_header_keys` and `stdio_config.envs` for key names only, never values.
- Verify the JSON parses before submitting.

### Don't

- Do not include secrets, tokens, API keys, credentials, private URLs, or personal data.
- Do not add duplicate servers; search `servers.json` first.
- Do not add a `slug` or `version` field.
- Do not add servers that require custom binaries unavailable through standard package managers or public installation instructions.
- Do not use promotional language or unverified claims.

## Categories

Reuse an existing category when possible:

| Category | Examples |
| -------- | -------- |
| `AI Tools` | Hugging Face, evaluation, observability for AI workflows |
| `Analytics` | Product analytics, BI, data notebooks |
| `Communication` | Slack, email, messaging |
| `Customer Support` | Intercom, tickets, support conversations |
| `Design` | Figma, Canva, diagrams, whiteboards |
| `Developer Tools` | GitHub, GitLab, hosting, docs, databases, code search |
| `E-Commerce` | Shopify, stores, inventory, orders |
| `Finance` | Payments, billing, accounting, market data |
| `Human Resources` | Hiring, recruiting, people analytics |
| `Legal` | Legal research and documents |
| `Lifestyle` | Events, travel, media, personal workflows |
| `Marketing` | SEO, campaigns, brand analytics |
| `Productivity` | Notion, Google Drive, calendars, tasks |
| `Project Management` | Jira, Linear, Asana, boards, issues |
| `Research` | Academic research, citations, scientific data |
| `Sales` | CRM, prospecting, enrichment |
| `Search` | Web search, research, content extraction |
| `Security` | Compliance, scanning, security findings |
| `Travel` | Flights, hotels, trip planning |

If none fit, use a short, descriptive new category and explain it in your PR description.

## Review Criteria

Maintainers may ask for changes or reject entries that are incomplete, unsafe, duplicative, hard to install, undocumented, or unrelated to MCP usage.

## How Syncing Works

1. You submit a PR adding your server to `servers.json`.
2. Maintainers review and merge it into the `dev` branch.
3. The Bifrost platform periodically fetches `servers.json` from the `dev` branch.
4. Bifrost derives an internal slug from `name`, upserts entries by that slug, and serves the catalog to users.

Changing the `name` of an existing entry can create a new internal slug. Only rename an existing server when that is intentional.

## Schema Validation

The [`schema.json`](./schema.json) file contains a [JSON Schema draft-07](https://json-schema.org) definition for `servers.json`. Use it locally when you want editor hints or manual validation.

## Questions?

- Open an [issue](https://github.com/maximhq/bifrost/issues/new) for questions or problems.
- See the [Bifrost docs](https://docs.getbifrost.ai) for general platform documentation.
