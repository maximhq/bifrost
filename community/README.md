# Community Contributions

This directory contains community-maintained data that powers parts of the Bifrost platform. Contributions are reviewed by maintainers and, once merged into the `dev` branch, are synced to the live platform.

## Available Catalogs

| Catalog | Description | Status |
| ------- | ----------- | ------ |
| [MCP Library](./mcp-library/) | Community-curated catalog of MCP servers | Active |

## How It Works

```
┌──────────────┐      PR       ┌──────────────┐     Merge      ┌──────────────┐
│ Contributor  │─────────────▶│ Maintainer   │──────────────▶│ dev branch   │
│ edits JSON   │              │ review       │               │ (stable)     │
└──────────────┘              └──────────────┘               └──────┬───────┘
                                                                     │
                                                            Periodic sync
                                                                     │
                                                              ┌──────▼───────┐
                                                              │   Bifrost    │
                                                              │   Platform   │
                                                              └──────────────┘
```

1. **Contribute** — Edit the relevant JSON file and open a pull request.
2. **Review** — Maintainers review your submission for accuracy, safety, and fit.
3. **Merge** — Once approved, your PR is merged into `dev`.
4. **Live** — Bifrost periodically reads the latest data from `dev` and serves it to all users.

## General Guidelines

- Follow each catalog's specific contributing guide before submitting.
- One logical change per pull request (e.g., one new MCP server per PR).
- Keep descriptions concise and factual.
- All contributions are subject to the project's [Code of Conduct](../CODE_OF_CONDUCT.md).

## Adding a New Catalog

If you'd like to propose a new community-maintained catalog, open a [feature request](https://github.com/maximhq/bifrost/issues/new?template=feature_request.yml) describing:

- What data the catalog would contain
- How it would be consumed by the platform
- Example entries
