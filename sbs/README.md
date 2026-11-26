# SBS fork of Bifrost

Bifrost `transports/v2.6.0` plus MetaMCP's MCP features and page structure, so Bifrost can replace MetaMCP as the MCP gateway.

## What the fork adds

| Area | Where |
|---|---|
| **Endpoints**: `/mcp/<name>` pointing at a namespace (re-pointable), auth per endpoint (virtual key, or public acting as a chosen key), `?api_key=`, OpenAPI on/off | `transports/bifrost-http/handlers/sbsmcp.go`, table `sbs_mcp_endpoints`, API `/api/sbs/mcp/endpoints` |
| **Tool overrides** per namespace (name, title, description, annotations, including custom keys) | `MCPToolSpec.Overrides` in `framework/configstore/tables/virtualmcp.go`; applied by hooks in `handlers/mcpserver.go`; API `PUT /api/sbs/mcp/virtual-mcps/{id}/overrides` |
| **OpenAPI export**: `GET /mcp/<name>/openapi.json`, `POST /mcp/<name>/tools/<tool>` | `sbsmcp.go` (loops back through `/mcp/<name>`, so auth and governance apply) |
| **Reset tool timeout on progress**, capped by a max total | `core/mcp/progresstimeout.go`; env `SBS_MCP_RESET_TIMEOUT_ON_PROGRESS`, `SBS_MCP_MAX_TOTAL_TIMEOUT` (seconds, default 1800) |
| **`${VAR}` in STDIO env values**, resolved from Bifrost's environment | `core/mcp/clientmanager.go` (`ExpandStdioEnvRefs`) |
| **MetaMCP import** (one-time, operator command) | `transports/bifrost-http/sbsimport/`, run as `/app/main sbs-import-metamcp -dsn ...` |
| **UI**: MetaMCP-style MCP section (Servers, Namespaces, Endpoints, Inspector, Live logs), server and namespace detail pages, JSON import and export | `ui/app/workspace/mcp-{servers,namespaces,endpoints,inspector}`, `ui/components/sbs/`, `ui/lib/sbs/` |
| **Patched mcp-go** (custom tool annotations, a global notification hook) | `sbs/mcp-go`, see `sbs/mcp-go/SBS_PATCH.md` |
| **Runtime image** with `uv`/`uvx`, `node`/`npx`, `python3` and `git`, for STDIO MCP servers | `sbs/Dockerfile.runtime` |

Upstream files carry only small hook lines marked `SBS fork`, so rebasing onto a newer Bifrost stays manageable.

## Build

```sh
sbs/build.sh sbs/bifrost:v2.6.0-sbs.N   # reports version v2.6.0+sbs.N
```

Everything runs in containers. Go modules, the build cache and `node_modules` live in the volumes `sbs_gomod`, `sbs_gocache` and `sbs_ui_node_modules`.

## Test

Go: run the MCP suites with the patched mcp-go, using the same `go work` setup as `build.sh`:

```sh
go test ./core/mcp/... ./transports/bifrost-http/handlers -run 'MCP|SBS|VirtualMCP'
```

Before deploying, test end to end on a copy of the data volume.

## Import from MetaMCP

```sh
docker compose stop bifrost
docker run --rm --network mcp_ai-net -e SBS_METAMCP_DSN=postgres://user:pass@metamcp-postgres:5432/metamcp_db?sslmode=disable \
  -e BIFROST_ENCRYPTION_KEY -v bifrost_data:/app/data -v ./checkmk.pem:/import/checkmk.pem:ro \
  --entrypoint /app/main sbs/bifrost:v2.6.0-sbs.N sbs-import-metamcp \
  -ca checkmk=/import/checkmk.pem -insecure-host 192.168.1.169 -grant-vk test [-dry-run]
docker compose up -d bifrost
```

The import is idempotent: anything already imported, or already present by name, is kept and reported.
