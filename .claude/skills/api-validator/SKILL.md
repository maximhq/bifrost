---
name: api-validator
description: Scan Bifrost HTTP controllers/handlers/integrations and validate OpenAPI API coverage, route methods/paths, parameters, request/response docs, and auth/security information. Use when asked to audit missing or incorrect APIs, compare controllers against docs/openapi, validate auth information, or fix API documentation drift. Invoked with /api-validator [scope] [--fix].
allowed-tools: Read, Grep, Glob, Bash, Edit, Write, Task, AskUserQuestion, TodoWrite
---

# API Validator

Audit Bifrost's HTTP API surface by scanning controller/handler route registrations, deriving the actual authentication behavior from middleware wiring, and comparing the result with `docs/openapi/openapi.yaml` and referenced OpenAPI path files.

Use this skill when the user asks for any of the following:
- "missing APIs"
- "incorrect APIs"
- "validate APIs"
- "check OpenAPI docs against controllers"
- "include auth information"
- "fix API docs"
- "audit route coverage"

Default behavior is **audit only**. Do not edit files unless the user explicitly asks for fixes or approves a proposed fix plan.

---

## Usage

```bash
/api-validator                         # Full audit: all handlers + OpenAPI
/api-validator management              # Audit only /api/*, /health, /ws, /metrics
/api-validator inference               # Audit /v1/* and provider integration APIs
/api-validator auth                    # Focus on effective OpenAPI security vs middleware auth
/api-validator <path-prefix>           # Audit one prefix, e.g. /api/governance or /openai
/api-validator --fix                   # Audit, present plan, then fix after approval
```

If scope is unclear, ask:

```text
Should I audit all APIs, only management APIs (/api/*), only inference/integration APIs (/v1/* and provider prefixes), or a specific path prefix?
```

---

## Source of truth

### Code route sources

Scan these files/directories as the controller source of truth:

| Area | Source | Notes |
|---|---|---|
| Server route wiring | `transports/bifrost-http/server/server.go` | `RegisterAPIRoutes`, `RegisterInferenceRoutes`, `RegisterUIRoutes`, direct `/metrics`, middleware lists |
| HTTP handlers/controllers | `transports/bifrost-http/handlers/*.go` | `RegisterRoutes` methods contain direct route registrations |
| SDK/provider integrations | `transports/bifrost-http/integrations/*.go` | `RouteConfig` factories and `GenericRouter.RegisterRoutes` register OpenAI/Anthropic/GenAI/Bedrock/Cohere/LiteLLM/LangChain/PydanticAI/Cursor/Passthrough routes |
| Auth middleware | `transports/bifrost-http/handlers/middlewares.go` | `APIMiddleware`, `InferenceMiddleware`, whitelists, realtime auth skips |
| Context auth extraction | `transports/bifrost-http/lib/ctx.go` | Virtual key and API key header extraction |
| Governance VK parser | `plugins/governance/utils.go` | Accepted virtual key headers for VK self-service endpoints |

### OpenAPI documentation sources

Scan these files as the documented API source of truth:

| Area | Source |
|---|---|
| Root spec/path map/security schemes | `docs/openapi/openapi.yaml` |
| Bundled output (generated, **do not edit**) | `docs/openapi/openapi.json` — produced by CI (`.github/workflows/openapi-bundle.yml`) on push to `main` |
| Inference paths | `docs/openapi/paths/inference/*.yaml` |
| Integration paths | `docs/openapi/paths/integrations/**/*.yaml` |
| Management paths | `docs/openapi/paths/management/*.yaml` |
| Schemas | `docs/openapi/schemas/**/*.yaml` |

`docs/openapi/openapi.json` is a build artifact. **Never edit it by hand and never regenerate it into the checked-in path.** The `OpenAPI Bundle` GitHub Actions workflow runs `python bundle.py` on push to `main` and commits the result as a separate `chore: regenerate openapi.json --skip-ci` commit. When validating locally, always bundle to `/tmp`, not into the repo.

---

## Workflow overview

1. **Preflight** -- Confirm scope, inspect git status, avoid edits unless approved.
2. **Extract actual API inventory** -- Scan route registrations and route config factories.
3. **Derive actual auth behavior** -- Trace middleware registration and handler-specific auth checks.
4. **Extract documented OpenAPI inventory** -- Resolve path refs and effective operation security.
5. **Compare inventories** -- Detect missing, stale, method/path, parameter, schema, and auth mismatches.
6. **Report findings** -- Present actionable tables with file/line references and recommended fixes.
7. **Fix with approval** -- If requested, update OpenAPI files or controller code after showing a plan.
8. **Validate** -- Re-bundle OpenAPI and run relevant tests/linters if available.

---

## Step 1: Preflight

Always start with:

```bash
git status --short
```

If the worktree is dirty, mention it and avoid overwriting unrelated changes.

Identify the scope:

```bash
# Route registration entry points
grep -R "func (.*RegisterRoutes" -n transports/bifrost-http/handlers transports/bifrost-http/integrations --include='*.go'

grep -n "func (s \*BifrostHTTPServer) RegisterAPIRoutes\|func (s \*BifrostHTTPServer) RegisterInferenceRoutes\|RegisterUIRoutes" transports/bifrost-http/server/server.go
```

If the user asked for `--fix`, still audit first and present a fix plan before editing.

---

## Step 2: Extract actual API inventory from controllers

Create an actual route inventory with this shape:

| Method | Path | Handler | Source | Route group | Registration condition | Actual auth class |
|---|---|---|---|---|---|---|
| `GET` | `/api/config` | `ConfigHandler.getConfig` | `handlers/config.go:76` | Management | Always | Admin/session API |

### 2a. Direct handler route registrations

Scan all handler route registration methods:

```bash
grep -R "\.GET\|\.POST\|\.PUT\|\.DELETE\|\.PATCH\|\.HEAD\|\.OPTIONS\|r.Handle" \
  -n transports/bifrost-http/handlers --include='*.go'
```

For a cleaner first pass:

```bash
for f in transports/bifrost-http/handlers/*.go; do
  if grep -q "RegisterRoutes" "$f"; then
    echo "--- $f"
    grep -nE 'func \(h .*RegisterRoutes|\.(GET|POST|PUT|DELETE|PATCH|HEAD|OPTIONS)\(|r\.Handle\(' "$f"
  fi
done
```

Record:
- HTTP method
- path pattern
- handler function
- file:line
- whether route is registered with `middlewares...`, custom middleware, or no middleware
- whether route registration is conditional

### 2b. Server-level and conditional route registration

Read `RegisterAPIRoutes`, `RegisterInferenceRoutes`, and the middleware-assembly block carefully. Locate them by symbol so the ranges don't drift as the file grows:

```bash
SERVER=transports/bifrost-http/server/server.go

# RegisterInferenceRoutes — inference route registrations.
# Window is generous (+60) so newly added integration handlers don't fall outside it.
INFER=$(grep -n "func (s \*BifrostHTTPServer) RegisterInferenceRoutes" "$SERVER" | head -1 | cut -d: -f1)
[ -n "$INFER" ] && sed -n "$((INFER)),$((INFER+60))p" "$SERVER"

# RegisterAPIRoutes — management/api route registrations (conditional plugin routes live here)
API=$(grep -n "func (s \*BifrostHTTPServer) RegisterAPIRoutes" "$SERVER" | head -1 | cut -d: -f1)
[ -n "$API" ] && sed -n "$((API)),$((API+120))p" "$SERVER"

# Middleware-assembly block — how apiMiddlewares / inferenceMiddlewares are composed,
# where AuthMiddleware.APIMiddleware()/InferenceMiddleware() are appended, and where
# TracingMiddleware / TransportInterceptorMiddleware are layered onto inference routes.
grep -n "apiMiddlewares\s*=\|inferenceMiddlewares\s*=\|AuthMiddleware\.APIMiddleware\|AuthMiddleware\.InferenceMiddleware\|TransportInterceptorMiddleware\|TracingMiddleware" "$SERVER"
```

Important conditional registrations:

| Condition | Routes affected |
|---|---|
| Governance plugin present | `GovernanceHandler.RegisterRoutes` (`/api/governance/*`) |
| Logging plugin present | `LoggingHandler.RegisterRoutes` (`/api/logs*`, `/api/mcp-logs*`) |
| Semantic cache plugin present | `CacheHandler.RegisterRoutes` (`/api/cache/*`) |
| Prompts plugin present | `PromptsHandler.RegisterRoutes` (`/api/prompt-repo/*`) |
| Prometheus plugin present | `/metrics` |
| Dev mode only | `/api/dev/pprof*` |
| OAuth metadata/per-user/consent | Registered without API auth middleware; public by design |
| UI catch-all | `/` and `/{filepath:*}`; do not compare as API docs unless user asks |

### 2c. Inference and SDK integration routes

Direct unified inference routes live in `handlers/inference.go`, `handlers/asyncinference.go`, `handlers/mcpinference.go`, and `handlers/mcpserver.go`.

Provider/framework integration routes are registered through `integrations.GenericRouter` and route config factories.

Scan integration route config factories:

```bash
grep -R "func Create.*RouteConfigs\|func OpenAI.*Paths\|RouteConfig{" \
  -n transports/bifrost-http/integrations --include='*.go'
```

Key factories/routers:

| Integration | Code source |
|---|---|
| OpenAI | `integrations/openai.go` (`CreateOpenAIRouteConfigs`, list models, batch, files, containers, container files, realtime path helpers) |
| Anthropic | `integrations/anthropic.go` |
| GenAI/Gemini | `integrations/genai.go` |
| Bedrock | `integrations/bedrock.go` |
| Cohere | `integrations/cohere.go` |
| LiteLLM | `integrations/litellm.go` |
| LangChain | `integrations/langchain.go` |
| PydanticAI | `integrations/pydanticai.go` |
| Cursor | `integrations/cursor.go` |
| Passthrough | `integrations/passthrough.go` catch-all prefixes |
| Realtime WebSocket/WebRTC/client secrets | `handlers/wsresponses.go`, `handlers/wsrealtime.go`, `handlers/webrtc_realtime.go`, `handlers/realtime_client_secrets.go`, `integrations/openai.go` path helper functions |

**Do not rely only on direct `r.GET(...)` grep for integrations.** Many integration routes are built from `RouteConfig{ Path: pathPrefix + path, Method: ... }` inside loops.

### 2d. Handle dynamic/catch-all routes correctly

Some routes are intentionally catch-all in Go but represented as concrete operations in OpenAPI.

Examples:
- OpenAI Azure-style deployments: `pathPrefix + "/openai/deployments/{deploymentPath:*}"` dispatches by suffix in `GetHTTPRequestType`. Expand the suffixes documented in the switch (`chat/completions`, `responses`, `embeddings`, etc.) before comparing.
- Passthrough routers register `{path:*}` for provider passthrough prefixes. Treat these as catch-all passthrough support. Do not mark every possible downstream provider URL as missing OpenAPI unless the user explicitly wants passthrough documentation.
- UI catch-all `/{filepath:*}` is not an API endpoint.

Normalize path parameters for comparison:
- Exact path match first.
- Then normalized match where `{name}`, `{deployment-id}`, and `{deploymentPath:*}` become `{param}`.
- If normalized paths match but parameter names differ, report as a **path parameter naming mismatch**, not a missing route.

---

## Step 3: Derive actual auth behavior

Auth correctness is part of this skill. Always inspect actual middleware wiring instead of guessing from path names.

### 3a. Read middleware setup

Read the auth and middleware setup. Locate the blocks by symbol so the ranges don't drift as the files grow — auth derivation for every route depends on these functions being read correctly, so a stale range would poison the whole audit:

```bash
SERVER=transports/bifrost-http/server/server.go
MW_FILE=transports/bifrost-http/handlers/middlewares.go

# Middleware-assembly block in server.go — where apiMiddlewares / inferenceMiddlewares
# get AuthMiddleware.APIMiddleware()/InferenceMiddleware() / Tracing /
# TransportInterceptor appended.
MW=$(grep -n "apiMiddlewares := commonMiddlewares" "$SERVER" | head -1 | cut -d: -f1)
[ -n "$MW" ] && sed -n "$((MW)),$((MW+65))p" "$SERVER"

# InferenceMiddleware — auth wiring for /v1/* and provider integration routes.
INF=$(grep -n "func (m \*AuthMiddleware) InferenceMiddleware" "$MW_FILE" | head -1 | cut -d: -f1)
[ -n "$INF" ] && sed -n "$((INF)),$((INF+15))p" "$MW_FILE"

# APIMiddleware — auth wiring for /api/* management routes (whitelist + prefix
# whitelist + WebSocket dashboard handling lives here; function body is large).
API_MW=$(grep -n "func (m \*AuthMiddleware) APIMiddleware" "$MW_FILE" | head -1 | cut -d: -f1)
[ -n "$API_MW" ] && sed -n "$((API_MW)),$((API_MW+180))p" "$MW_FILE"
```

Key logic:
- `apiMiddlewares` includes `AuthMiddleware.APIMiddleware()` in OSS when config store exists and auth middleware initializes.
- `inferenceMiddlewares` includes `AuthMiddleware.InferenceMiddleware()` in OSS when config store exists and auth middleware initializes.
- `OAuthMetadataHandler`, `PerUserOAuthHandler`, and `ConsentHandler` are registered without auth middleware.
- `/api/governance/virtual-keys/quota` is registered without `middlewares...` and performs virtual-key authentication inside the handler.
- Realtime transport endpoints have special skip behavior in `isRealtimeTransportEndpoint` and handler-level auth extraction.

### 3b. Actual auth classes

Classify every route into one of these actual auth classes:

| Auth class | Actual code behavior | OpenAPI expectation |
|---|---|---|
| Public | No auth middleware, or explicitly whitelisted in `APIMiddleware` | Operation must set `security: []` if root `security` is defined |
| Admin/session API | `APIMiddleware` protects route; accepts session Bearer token, Basic admin auth, and session cookie fallback. WebSocket dashboard also accepts `?ticket=`/legacy token/cookie. | `BearerAuth` and `BasicAuth`; mention cookie/ticket in description where relevant. Do **not** include `VirtualKeyAuth` unless handler accepts VK. Do **not** include `ApiKeyAuth` unless `x-api-key` is actually accepted for that route. |
| Inference API | `InferenceMiddleware` protects route unless `auth_config.disable_auth_on_inference` is true; context extraction supports virtual keys and direct provider/API key headers for inference flows. | Usually `BearerAuth`, `BasicAuth`, `VirtualKeyAuth`, `ApiKeyAuth` |
| Virtual-key self-service | Handler itself parses virtual key from `x-bf-vk`, `Authorization: Bearer sk-bf-*`, `x-api-key: sk-bf-*`, or `x-goog-api-key: sk-bf-*` | `VirtualKeyAuth`, `BearerAuth`, `ApiKeyAuth` (and document Google key header if exposed); no admin-only security |
| Realtime transport | WebSocket/WebRTC/client secret handlers capture auth headers/subprotocols and have special middleware bypasses | Validate per handler; do not assume standard admin or inference auth |
| Dev-only | Only registered when `handlers.IsDevMode()` | Document as dev-only or omit from public OpenAPI, depending existing convention |
| Conditional plugin | Only registered when a plugin is loaded | Document condition in description or mark conditional in report |

### 3c. Public and whitelisted routes to verify

The API auth middleware has a system whitelist and prefix whitelist. Verify it in code each time, but expect these routes/prefixes to be public when auth is enabled:

- `/health`
- `/api/session/login`
- `/api/session/is-auth-enabled`
- `/api/version`
- `/api/oauth/callback`
- `/api/oauth/*` (prefix whitelist in `APIMiddleware`)
- `/.well-known/oauth-protected-resource`
- `/.well-known/oauth-authorization-server`
- `/oauth/consent`
- `/oauth/consent/mcps`
- `/api/oauth/per-user/consent/*`
- `/api/dev/*` (dev-only)
- `/login`, `/favicon.ico`, `/assets/*` (UI/static; not API docs usually)
- SCIM OAuth routes if present in enterprise code

**Important:** OpenAPI root `security` applies to every operation unless the operation overrides it. If an endpoint is public, it must explicitly use:

```yaml
security: []
```

Otherwise the docs incorrectly show authentication as required.

### 3d. Virtual key/header auth details

For virtual-key behavior, inspect:

```bash
# Context auth extraction (virtual key + provider/API key headers)
sed -n '1,90p' plugins/governance/utils.go
grep -n "x-bf-vk\|x-goog-api-key\|sk-bf-\|VirtualKey\|ExtractVirtualKey" transports/bifrost-http/lib/ctx.go

# VK quota handler (locate by symbol so line numbers don't drift)
LINE=$(grep -n "func.*getVirtualKeyQuota" transports/bifrost-http/handlers/governance.go | head -1 | cut -d: -f1)
if [ -n "$LINE" ]; then
  sed -n "$((LINE)),$((LINE+45))p" transports/bifrost-http/handlers/governance.go
fi
```

Virtual key sources commonly include:
- `x-bf-vk`
- `Authorization: Bearer sk-bf-*`
- `x-api-key: sk-bf-*`
- `x-goog-api-key: sk-bf-*`

Do not document a header as an auth method unless the code for that route actually accepts it.

---

## Step 4: Extract documented OpenAPI inventory

### 4a. Read root OpenAPI path map

```bash
grep -n "^  /" docs/openapi/openapi.yaml
```

### 4b. Bundle OpenAPI to resolve refs

Prefer using the bundler so effective operations are easy to parse:

```bash
cd docs/openapi
python3 bundle.py --output /tmp/bifrost-openapi.json
cd -
```

If `PyYAML` is missing, report that bundling could not run and fall back to reading `docs/openapi/openapi.json` if present:

```bash
python3 - <<'PY'
import json
spec = json.load(open('docs/openapi/openapi.json'))
print(len(spec.get('paths', {})))
PY
```

### 4c. Print effective documented operations and security

Use this helper after bundling:

```bash
python3 - <<'PY'
import json
spec = json.load(open('/tmp/bifrost-openapi.json'))
root_security = spec.get('security')
methods = {'get','post','put','delete','patch','head','options','trace'}
for path, item in sorted(spec.get('paths', {}).items()):
    for method, op in sorted(item.items()):
        if method not in methods:
            continue
        effective_security = op.get('security', root_security)
        operation_id = op.get('operationId', '')
        tags = ','.join(op.get('tags', []))
        print(f"{method.upper():6} {path:70} security={effective_security} operationId={operation_id} tags={tags}")
PY
```

Record:
- Path
- Method
- `operationId`
- Tags
- Effective security (`operation.security` if present, otherwise root `security`)
- Parameters (`path`, `query`, `header`)
- Request body presence/content type
- Response status codes/content types

---

## Step 5: Compare actual vs documented APIs

Create comparison tables. Use exact route matches first, then normalized path-parameter matches.

### 5a. Coverage mismatches

Classify as:

| Type | Meaning |
|---|---|
| Missing in OpenAPI | Controller has a route, but `docs/openapi/openapi.yaml` has no matching path+method |
| Stale in OpenAPI | OpenAPI documents a path+method not found in OSS controllers/integrations |
| Method mismatch | Path exists in both, but methods differ |
| Path parameter mismatch | Same normalized path, different parameter names or wildcard behavior |
| Conditional route undocumented | Route exists only when plugin/dev mode enabled but docs do not mention the condition |
| Catch-all route undocumented | Passthrough/catch-all exists but docs omit it; usually low priority unless public API intended |

For stale routes, check whether they may be enterprise-only before calling them wrong. If the code is not in the OSS repo, report as:

```text
Documented route not found in OSS controllers. This may be enterprise-only; confirm expected source before removing.
```

### 5b. Auth mismatches

For every route, compare actual auth class to effective OpenAPI security.

Flag these issues:
- Public route inherits root auth because `security: []` is missing.
- Admin/session route documents `VirtualKeyAuth` but code does not accept VK.
- Admin/session route documents `ApiKeyAuth` but code does not accept `x-api-key` for that route.
- Virtual-key self-service route documents admin auth only or omits accepted VK headers.
- Inference route omits `VirtualKeyAuth` or `ApiKeyAuth` when the code accepts them.
- WebSocket route documents normal HTTP auth but code requires ticket/query/cookie/subprotocol behavior.
- Realtime route docs do not match handler-level auth parsing.

### 5c. Parameter mismatches

For each matched route, inspect handler code for:

```bash
# Path params
ctx.UserValue("...")

# Query params
ctx.QueryArgs().Peek("...")
ctx.QueryArgs().GetUintOrZero("...")
ctx.QueryArgs().VisitAll(...)

# Headers
ctx.Request.Header.Peek("...")
ctx.Request.Header.Cookie("...")
```

Compare with OpenAPI `parameters`:
- Missing path parameter
- Missing query parameter
- Required flag mismatch
- Type mismatch (`boolean`, `integer`, `string`, enum)
- Header parameter missing where auth/security scheme is not enough

### 5d. Request body mismatches

For routes with request bodies:

1. Find the Go request struct used by the handler.
2. Compare `json` tags and validation logic to OpenAPI schema.
3. Check required fields and nullable/optional behavior.
4. Check content type handling (`application/json`, multipart, `text/event-stream`, raw SDP, etc.).

Useful searches:

```bash
grep -n "type .*Request struct" transports/bifrost-http/handlers/<handler>.go
grep -n "json.Unmarshal\|sonic.Unmarshal\|parse.*Multipart\|ContentType" transports/bifrost-http/handlers/<handler>.go transports/bifrost-http/integrations/*.go
```

### 5e. Response/status mismatches

Inspect handler responses:

```bash
grep -n "SendJSON\|SendError\|SendBifrostError\|SetStatusCode\|Status" transports/bifrost-http/handlers/<handler>.go
```

Compare with OpenAPI responses:
- Missing success status
- Missing error status
- Wrong content type
- Response schema does not match fields actually returned
- Streaming response missing `text/event-stream`
- WebSocket response should document `101` upgrade where applicable

---

## Step 6: Report format

Always present the audit in this structure:

````markdown
## API validation report

### Scope
- **Scope audited:** <all / management / inference / prefix>
- **Controller sources:** <files/directories scanned>
- **OpenAPI sources:** <files scanned / bundled spec>
- **Bundle status:** <success/failure + command>

### Summary
| Category | Count |
|---|---:|
| Actual routes found | N |
| Documented operations found | N |
| Missing in OpenAPI | N |
| Stale or not found in OSS controllers | N |
| Method/path mismatches | N |
| Auth/security mismatches | N |
| Parameter/schema/status mismatches | N |

### Missing APIs in OpenAPI
| Method | Path | Handler/source | Actual auth | Registration condition | Recommended doc file |
|---|---|---|---|---|---|

### Documented APIs not found in controllers
| Method | Path | OpenAPI source | Effective auth | Notes |
|---|---|---|---|---|

### Method/path mismatches
| Actual | Documented | Source | Issue | Recommended fix |
|---|---|---|---|---|

### Auth/security mismatches
| Method | Path | Actual auth from code | OpenAPI effective security | Source | Recommended fix |
|---|---|---|---|---|---|

### Parameter/request/response mismatches
| Method | Path | Area | Code source | OpenAPI source | Issue | Recommended fix |
|---|---|---|---|---|---|---|

### Conditional routes
| Method | Path | Condition | Should be documented? | Notes |
|---|---|---|---|---|

### Recommended fix plan
1. <Specific file edit or controller change>
2. <Specific file edit or controller change>

### Validation commands
```bash
cd docs/openapi && python3 bundle.py --output /tmp/bifrost-openapi.json
# plus any relevant go tests or OpenAPI lint commands
```

**Proceed with fixes?** (yes / no / modify plan)
````

If no issues are found, still report the route count, auth matrix used, and commands run.

---

## Step 7: Fix mode

Only enter fix mode after the user asks for `--fix` or approves the proposed plan.

### 7a. Fix priority

Prefer fixes in this order:

1. **OpenAPI documentation fixes** when controllers are correct and docs are missing/stale.
2. **Controller route fixes** only if docs reflect the intended API and code is actually wrong.
3. **Auth behavior fixes** only after explicit confirmation, because changing middleware/handler auth can be breaking.

### 7b. OpenAPI edit rules

When adding or fixing docs:
- Edit YAML sources only. **Never edit `docs/openapi/openapi.json`** — it is regenerated and committed by the `OpenAPI Bundle` GitHub Actions workflow on push to `main`.
- Do not run `python3 bundle.py` with `--output docs/openapi/openapi.json` (or with no `--output`, which defaults to that path). Always bundle to `/tmp` for local validation.
- Add path mapping in `docs/openapi/openapi.yaml`.
- Add or update the relevant file in `docs/openapi/paths/<area>/`.
- Add/update schemas in `docs/openapi/schemas/<area>/` when needed.
- Use unique `operationId` values.
- Keep tags consistent with existing docs.
- Public operations must set `security: []` if root security exists.
- Inference operations should include `VirtualKeyAuth` when virtual keys are accepted.
- Management admin operations should not claim `VirtualKeyAuth` unless code accepts it.
- Document plugin/dev/enterprise-only conditions in `description`.

### 7c. Controller edit rules

When fixing Go routes:
- Edit only after the user explicitly approves controller behavior changes.
- Preserve middleware class intentionally; do not move a route between `apiMiddlewares`, `inferenceMiddlewares`, and no middleware without approval.
- Add/update tests for changed route registration or auth behavior.
- Run module-specific tests from `transports/`:

```bash
cd transports && go test ./bifrost-http/handlers ./bifrost-http/server ./bifrost-http/integrations
```

If this command is too broad or slow, run the specific package/test that covers the edited route.

### 7d. Validate after edits

After OpenAPI changes:

```bash
cd docs/openapi
python3 bundle.py --output /tmp/bifrost-openapi.json
python3 bundle.py --format yaml --output /tmp/bifrost-openapi.yaml
cd -
```

Then inspect duplicates and security:

```bash
python3 - <<'PY'
import json, collections
spec=json.load(open('/tmp/bifrost-openapi.json'))
ids=[]
for path,item in spec.get('paths',{}).items():
  for method,op in item.items():
    if method in {'get','post','put','delete','patch','head','options','trace'}:
      oid=op.get('operationId')
      if oid: ids.append(oid)
for oid,count in collections.Counter(ids).items():
  if count>1:
    print('DUPLICATE operationId', oid, count)
PY
```

Report all validation results.

---

## Common Bifrost route groups

Use this as a checklist; always verify against current code.

### Management handlers

| Handler | Expected prefixes |
|---|---|
| `HealthHandler` | `/health` |
| `ConfigHandler` | `/api/config`, `/api/version`, `/api/proxy-config`, `/api/pricing/force-sync` |
| `ProviderHandler` | `/api/providers`, `/api/keys`, `/api/models` |
| `MCPHandler` | `/api/mcp/*` |
| `PluginsHandler` | `/api/plugins*` |
| `SessionHandler` | `/api/session/*` |
| `OAuthHandler` | `/api/oauth/callback`, `/api/oauth/config/{id}/*` |
| `OAuthMetadataHandler` | `/.well-known/oauth-*` |
| `PerUserOAuthHandler` | `/api/oauth/per-user/*` |
| `ConsentHandler` | `/oauth/consent*`, `/api/oauth/per-user/consent/*` |
| `GovernanceHandler` | `/api/governance/*` |
| `LoggingHandler` | `/api/logs*`, `/api/mcp-logs*` |
| `PromptsHandler` | `/api/prompt-repo/*` |
| `CacheHandler` | `/api/cache/*` |
| `WebSocketHandler` | `/ws` |
| `DevPprofHandler` | `/api/dev/pprof*` |

### Inference handlers

| Handler/router | Expected prefixes |
|---|---|
| `CompletionHandler` | `/v1/*` core inference |
| `AsyncHandler` | `/v1/async/*` |
| `MCPInferenceHandler` | `/v1/mcp/tool/execute` |
| `MCPServerHandler` | `/mcp` |
| `IntegrationHandler` | `/openai`, `/anthropic`, `/genai`, `/bedrock`, `/cohere`, `/litellm`, `/langchain`, `/pydanticai`, `/cursor`, passthrough prefixes |
| Realtime handlers | `/v1/responses` WS, `/v1/realtime*`, `/openai/...` aliases |

---

## Mandatory rules

- **Always scan code first.** Do not trust existing OpenAPI docs as source of truth.
- **Always compute effective OpenAPI security.** Operation security overrides root security; missing operation security inherits root security.
- **Always include file:line references** for code and docs findings.
- **Do not report UI catch-all routes as missing APIs.**
- **Do not report passthrough catch-all routes as missing docs unless public documentation is intended.**
- **Do not remove documented routes that may be enterprise-only without confirmation.**
- **Do not edit controllers for auth changes without explicit approval.**
- **Do not claim a route accepts virtual keys, API keys, cookies, tickets, or Basic auth unless code confirms it.**
- **Public endpoints must use `security: []` in OpenAPI when root security is defined.**
- **Never edit or regenerate `docs/openapi/openapi.json` locally.** It is a CI-generated artifact produced by `.github/workflows/openapi-bundle.yml` on push to `main`. Edit only the YAML sources; bundle to `/tmp` for validation.
- **After fixes, run the OpenAPI bundler and report validation results.**
