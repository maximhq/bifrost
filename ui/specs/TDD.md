# Technical Design Document (TDD)
## Bifrost UI — Control Plane

**Version:** 1.0  
**Date:** 2026-04-05  
**Status:** Draft  
**References:** [SRS.md](SRS.md)

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Tech Stack & Build Configuration](#2-tech-stack--build-configuration)
3. [Directory Structure](#3-directory-structure)
4. [Application Bootstrapping](#4-application-bootstrapping)
5. [Routing & Page Structure](#5-routing--page-structure)
6. [State Management](#6-state-management)
7. [API Layer — RTK Query](#7-api-layer--rtk-query)
8. [WebSocket Architecture](#8-websocket-architecture)
9. [Authentication Flow](#9-authentication-flow)
10. [Enterprise vs OSS Build System](#10-enterprise-vs-oss-build-system)
11. [Component Architecture](#11-component-architecture)
12. [Validation System](#12-validation-system)
13. [Key Utilities & Patterns](#13-key-utilities--patterns)
14. [Data Types & Schemas](#14-data-types--schemas)
15. [Testing Strategy](#15-testing-strategy)

---

## 1. Architecture Overview

Bifrost UI is a **Next.js 15 static export** that serves as the control plane for the Bifrost AI proxy gateway. It is built once and embedded inside the Go binary's HTTP transport (`transports/bifrost-http/ui/`).

```
Browser
  └── Next.js Static SPA (SSR shell → CSR)
        ├── Redux Store (RTK Query + slices)
        ├── WebSocket Context (real-time streams)
        └── REST API  ──► Go Proxy Backend (:8080)
```

**Key architectural decisions:**

| Decision | Rationale |
|----------|-----------|
| `output: "export"` (static) | Embedded into Go binary; no Node.js runtime needed at deploy time |
| RTK Query as API layer | Cache invalidation, tag-based refetching, optimistic updates without boilerplate |
| Enterprise feature via Webpack alias | Single codebase, two builds — OSS shows fallback views, enterprise gets real implementations |
| Global WebSocket singleton | Prevents reconnects across page navigations; components subscribe to typed channels |
| `Validator` class over library | Uniform inline validation without depending on Zod in every form (Zod used only for provider config schemas) |

---

## 2. Tech Stack & Build Configuration

### 2.1 Core Dependencies

| Category | Library | Version |
|----------|---------|---------|
| Framework | Next.js | 15.5 |
| UI runtime | React | 19.2 |
| Language | TypeScript | 5.9 |
| Styling | Tailwind CSS | 4.1 |
| Accessible primitives | Radix UI | various |
| Icon set | Phosphor Icons | 2.1 |
| State / API | Redux Toolkit + RTK Query | 2.8 |
| UI component state | Zustand | 5.0 |
| Forms | React Hook Form + Zod | 7.62 / 4.2 |
| Tables | TanStack Table | 8.21 |
| Code editor | Monaco Editor | 0.52 |
| Charts | Recharts | 3.1 |
| Drag-and-drop | dnd-kit | 0.3 |
| Toast notifications | Sonner | 2.0 |
| URL state | nuqs | 2.8 |
| Date utils | date-fns + moment | 4.1 / 2.30 |
| Streaming markdown | streamdown | 2.3 |
| Query builder UI | react-querybuilder | 8.11 |

### 2.2 Build Configuration (`next.config.ts`)

```
output: "export"          → Produces static files in /out
trailingSlash: true       → Required for static hosting compatibility
distDir: "out"            → Output directory
generateBuildId: "build"  → Deterministic build ID for embedded binary
```

**Dev proxy rewrite:** `GET /api/:path*` → `http://localhost:8080/api/:path*` (development only, not emitted to static output).

**Webpack aliases:**

| Alias | OSS target | Enterprise target |
|-------|-----------|------------------|
| `@enterprise` | `app/_fallbacks/enterprise` | `app/enterprise` |
| `@schemas` | `app/_fallbacks/enterprise/lib/schemas` | `app/enterprise/lib/schemas` |

Build variant is detected at build time via `fs.existsSync("app/enterprise")`. The `NEXT_PUBLIC_IS_ENTERPRISE` env var is injected for runtime branching in client code.

### 2.3 Build Scripts

| Script | Action |
|--------|--------|
| `npm run dev` | Next.js dev server with hot reload |
| `npm run build` | Static export → fix paths → copy to `../transports/bifrost-http/ui` |
| `npm run build-enterprise` | Static export + fix paths only (enterprise keeps separate copy step) |
| `npm run fix-paths` | Post-processing script to fix static asset paths for the Go file server |

---

## 3. Directory Structure

```
ui/
├── app/                          # Next.js App Router pages
│   ├── layout.tsx                # Root HTML shell (Geist fonts, suppressHydrationWarning)
│   ├── page.tsx                  # Root redirect → /workspace/dashboard
│   ├── clientLayout.tsx          # Client-side providers (Redux, WebSocket, Theme)
│   ├── globals.css               # Tailwind base + CSS variables (light/dark tokens)
│   ├── login/                    # Authentication pages
│   ├── pprof/                    # Go pprof profiling UI
│   ├── workspace/                # All authenticated pages
│   │   ├── layout.tsx            # Workspace shell (sidebar + header + auth guard)
│   │   ├── dashboard/
│   │   ├── providers/
│   │   ├── logs/
│   │   ├── routing-rules/
│   │   ├── virtual-keys/
│   │   ├── governance/
│   │   ├── rbac/
│   │   ├── guardrails/
│   │   ├── pii-redactor/
│   │   ├── plugins/
│   │   ├── mcp-settings/
│   │   ├── mcp-registry/
│   │   ├── mcp-logs/
│   │   ├── mcp-tool-groups/
│   │   ├── mcp-auth-config/
│   │   ├── prompt-repo/
│   │   ├── model-limits/
│   │   ├── model-catalog/
│   │   ├── observability/
│   │   ├── adaptive-routing/
│   │   ├── alert-channels/
│   │   ├── custom-pricing/
│   │   ├── cluster/
│   │   ├── audit-logs/
│   │   ├── scim/
│   │   └── config/               # Tabbed proxy configuration
│   │       ├── proxy/
│   │       ├── performance-tuning/
│   │       ├── client-settings/
│   │       ├── security/
│   │       ├── api-keys/
│   │       ├── large-payload/
│   │       ├── observability/
│   │       ├── caching/
│   │       ├── pricing-config/
│   │       └── mcp-gateway/
│   └── _fallbacks/               # OSS stubs for enterprise features
│       └── enterprise/
│           ├── components/       # Fallback "Contact Us" views per feature
│           └── lib/              # Stub store slices, schemas, contexts
│
├── components/                   # Shared UI components
│   ├── ui/                       # Primitive components (button, input, dialog, etc.)
│   ├── table/                    # TanStack Table wrappers + column drag-drop
│   ├── filters/                  # Filter popover component
│   ├── chat/                     # Chat/completion playground
│   ├── prompts/                  # Prompt template editor
│   ├── header.tsx                # Top navigation bar
│   ├── sidebar.tsx               # Collapsible navigation sidebar
│   └── provider.tsx              # Client-side provider tree
│
├── hooks/                        # Custom React hooks
│   ├── useWebSocket.tsx          # WebSocket context + provider
│   ├── useTablePageSize.ts       # ResizeObserver-based dynamic page size
│   ├── useDebounce.ts            # Debounce hook
│   ├── useCopyToClipboard.ts
│   ├── useStoreSync.tsx          # Syncs Redux state from server fetch
│   └── use-mobile.ts
│
├── lib/
│   ├── store/                    # Redux store
│   │   ├── store.ts              # configureStore with dynamic enterprise injection
│   │   ├── slices/               # Redux slices (app, provider, plugin)
│   │   └── apis/                 # RTK Query endpoint definitions
│   ├── types/                    # TypeScript interfaces matching Go backend schemas
│   ├── constants/                # Static maps (providers, CEL fields, model placeholders)
│   ├── utils/                    # Pure utility functions
│   ├── schemas/                  # Zod schemas for form validation
│   └── utils.ts                  # cn() (clsx + tailwind-merge)
│
└── scripts/
    └── fix-paths.js              # Post-build path rewriting for Go embedding
```

---

## 4. Application Bootstrapping

### 4.1 Provider Tree

The root `layout.tsx` renders a minimal HTML shell. All client-side state is initialized in `clientLayout.tsx`:

```
<ReduxProvider store={store}>
  <WebSocketProvider path="/ws">
    <ThemeProvider>
      <ProgressBar />
      <Toaster />
      {children}
    </ThemeProvider>
  </WebSocketProvider>
</ReduxProvider>
```

### 4.2 Workspace Layout Auth Guard

`app/workspace/layout.tsx` enforces authentication. On mount it calls the session API endpoint; a 401 response redirects to `/login`. All workspace pages render inside a `<Sidebar> + <Header> + <main>` shell.

### 4.3 Feature Flags

The `appSlice` stores three runtime feature flags:

| Flag | Controls |
|------|---------|
| `enableMCP` | MCP sidebar entries and pages |
| `enableCaching` | Caching config tab |
| `enableLogging` | Logging config and log pages |

These are populated from the backend config on initial load via `useStoreSync`.

---

## 5. Routing & Page Structure

Next.js App Router with file-system routes. Every workspace route is a Server Component shell that renders a `"use client"` view component.

### 5.1 Route Map

| Path | Feature | Enterprise-only |
|------|---------|----------------|
| `/` | Redirect to `/workspace/dashboard` | — |
| `/login` | Authentication | — |
| `/workspace/dashboard` | Real-time metrics overview | — |
| `/workspace/providers` | LLM provider CRUD | — |
| `/workspace/logs` | Request log viewer | — |
| `/workspace/routing-rules` | CEL routing rule builder | — |
| `/workspace/virtual-keys` | API key management | — |
| `/workspace/governance` | User management | — |
| `/workspace/rbac` | Role-based access control | Yes |
| `/workspace/guardrails` | Content safety config | Yes |
| `/workspace/pii-redactor` | PII detection rules | Yes |
| `/workspace/plugins` | Plugin management | — |
| `/workspace/mcp-settings` | MCP server config | — |
| `/workspace/mcp-registry` | MCP server registry | — |
| `/workspace/mcp-logs` | MCP call logs | — |
| `/workspace/mcp-tool-groups` | MCP tool bundling | Yes |
| `/workspace/mcp-auth-config` | MCP OAuth config | Yes |
| `/workspace/prompt-repo` | Prompt versioning | — |
| `/workspace/model-limits` | Token/request quotas | — |
| `/workspace/model-catalog` | Model registry | — |
| `/workspace/observability` | Metrics & tracing | — |
| `/workspace/adaptive-routing` | AI-driven routing | Yes |
| `/workspace/alert-channels` | Notification config | Yes |
| `/workspace/custom-pricing` | Cost per token config | — |
| `/workspace/cluster` | Multi-node status | Yes |
| `/workspace/audit-logs` | Admin audit trail | Yes |
| `/workspace/scim` | Identity provisioning | Yes |
| `/workspace/config/*` | Proxy configuration tabs | Partial |
| `/pprof` | Go profiling UI | — |

### 5.2 Config Tab Sub-routes

`/workspace/config/` uses a tabbed layout with child routes:

`proxy` · `performance-tuning` · `client-settings` · `security` · `api-keys` · `large-payload` · `observability` · `caching` · `pricing-config` · `mcp-gateway`

---

## 6. State Management

### 6.1 Layer Summary

| Layer | Tool | Scope | Contents |
|-------|------|-------|----------|
| Server/remote state | RTK Query (`baseApi`) | Global | All API data with cache + tag invalidation |
| Global app state | Redux (`appSlice`) | Global | Auth status, sidebar, theme, feature flags, notifications |
| Provider config state | Redux (`providerSlice`) | Global | Currently edited provider form state |
| Plugin state | Redux (`pluginSlice`) | Global | Enabled/disabled plugin list |
| Enterprise state | Redux (dynamic injection) | Global | Enterprise-specific slices added at runtime |
| Component/UI state | Zustand | Local | Form dialogs, modals, ephemeral UI |
| Real-time streams | WebSocket Context | Global | Log/metric stream subscriptions |
| URL query state | nuqs | Route-scoped | Log filters, pagination params |

### 6.2 Redux Store (`lib/store/store.ts`)

```typescript
configureStore({
  reducer: {
    api: baseApi.reducer,      // RTK Query cache
    app: appReducer,           // UI + session state
    provider: providerReducer, // Provider form state
    plugin: pluginReducer,     // Plugin toggles
    ...enterpriseReducers,     // Injected at build time
  },
  middleware: [...defaultMiddleware, baseApi.middleware],
})
```

Enterprise reducers and APIs are injected via `require("@enterprise/lib/store/slices")` wrapped in a `try/catch`. If the enterprise module is absent (OSS build), the fallback stub exports empty objects.

### 6.3 `appSlice` — Global App State

Key state fields:

| Field | Type | Purpose |
|-------|------|---------|
| `sidebarCollapsed` | `boolean` | Sidebar open/closed |
| `theme` | `"light" \| "dark" \| "system"` | Theme preference |
| `isInitializing` | `boolean` | Shows full-page loader during boot |
| `isOnline` | `boolean` | Network connectivity flag |
| `currentUser` | `{ id, name, email } \| null` | Authenticated user |
| `notifications` | `Notification[]` | In-app notification queue (max 50) |
| `settings` | `{ autoRefresh, refreshInterval, maxLogEntries, defaultPageSize }` | User preferences |
| `features` | `{ enableMCP, enableCaching, enableLogging }` | Runtime feature flags |
| `globalError` | `{ message, code, timestamp } \| null` | App-level error |

---

## 7. API Layer — RTK Query

### 7.1 Base API Setup (`lib/store/apis/baseApi.ts`)

`createApi` is instantiated once with:

- **`reducerPath: "api"`** — single RTK Query cache namespace
- **`baseQuery`** — `fetchBaseQuery` with `credentials: "include"` (session cookie auth) + `Content-Type: application/json`
- **`baseQueryWithRefresh`** — enterprise wraps this to handle OAuth token refresh; OSS passes through
- **`baseQueryWithErrorHandling`** — handles 401 (redirect to login), network errors, and normalizes error shapes to `BifrostErrorResponse`

### 7.2 Cache Tags

All entities are managed via RTK Query tag invalidation. Defined tags:

`Logs` · `MCPLogs` · `Providers` · `MCPClients` · `Config` · `CacheConfig` · `VirtualKeys` · `Teams` · `Customers` · `Budgets` · `RateLimits` · `UsageStats` · `DebugStats` · `HealthCheck` · `DBKeys` · `Models` · `BaseModels` · `ModelConfigs` · `ProviderGovernance` · `Plugins` · `SCIMProviders` · `User` · `Guardrails` · `ClusterNodes` · `Users` · `GuardrailRules` · `Roles` · `Resources` · `Operations` · `Permissions` · `APIKeys` · `OAuth2Config` · `RoutingRules` · `MCPToolGroups` · `AuditLogs` · `UserGovernance` · `LargePayloadConfig` · `Folders` · `Prompts` · `Versions` · `Sessions`

### 7.3 API Module Files

Each domain has its own file that calls `baseApi.injectEndpoints(...)`:

| File | Domain |
|------|--------|
| `providersApi.ts` | Provider CRUD, model listings, model datasheets |
| `logsApi.ts` | Request log queries |
| `mcpApi.ts` | MCP server CRUD |
| `mcpLogsApi.ts` | MCP call log queries |
| `configApi.ts` | Proxy configuration read/write |
| `governanceApi.ts` | Users, teams, virtual keys |
| `pluginsApi.ts` | Plugin enable/disable |
| `promptsApi.ts` | Prompt and version CRUD |
| `routingRulesApi.ts` | Routing rule CRUD |
| `sessionApi.ts` | Session validation, WS ticket issuance |
| `devApi.ts` | Debug/profiling endpoints |

### 7.4 Error Shape

All API errors are normalized to:

```typescript
interface BifrostErrorResponse {
  error: { message: string }
}
```

The `getErrorMessage(error)` helper extracts the message string for display in toast notifications.

---

## 8. WebSocket Architecture

**File:** `hooks/useWebSocket.tsx`

### 8.1 Design

A single global WebSocket connection is maintained across the app lifetime. A module-level `globalWsRef` prevents reconnects when components remount during navigation.

```
WebSocketProvider
  ├── Manages single WebSocket to /ws?ticket=<one-time-token>
  ├── Heartbeat ping every 25 seconds
  ├── Exponential backoff reconnect: 0.5s → 1s → 2s → 4s → 8s → 16s → 32s (cap)
  └── Message routing via Map<channel, Set<handler>>
```

### 8.2 Ticket-based Auth

Before connecting, the provider fetches a short-lived ticket from `POST /session/ws-ticket` (cookie-authenticated). The ticket is appended as `?ticket=<value>`. This avoids putting session cookies in the WS URL (which would be logged). If the ticket fetch fails, the connection is attempted without it (cookie fallback).

### 8.3 Subscription API

```typescript
const { subscribe, send, isConnected } = useWebSocket()

// Subscribe to a typed channel
useEffect(() => {
  const unsubscribe = subscribe("logs", (data) => { /* handle */ })
  return unsubscribe
}, [subscribe])
```

Wildcard subscriptions via `subscribe("*", handler)` receive all messages regardless of channel.

### 8.4 Message Format

Incoming messages: `{ type: string, payload: any }`. The `type` field is used as the routing key.

---

## 9. Authentication Flow

### 9.1 OSS (Cookie-based)

1. Unauthenticated request → 401 from backend → `clearAuthStorage()` → `window.location.href = "/login"`
2. Login form submits credentials → backend sets `HttpOnly` session cookie
3. All subsequent API calls use `credentials: "include"` — no client-side token storage

### 9.2 Enterprise (OAuth 2.0)

Enterprise builds wrap `baseQuery` with `createBaseQueryWithRefresh`. On 401, the `tokenManager` attempts a silent token refresh using a stored refresh token before redirecting to login.

OAuth tokens and refresh tokens are managed by `clearOAuthStorage()` on logout. The `IS_ENTERPRISE` constant gates this code path.

### 9.3 Auth State in Redux

`appSlice.currentUser` holds `{ id, name, email }`. Populated by `useStoreSync` on boot via the session API. Set to `null` on logout (`resetAppState()`).

---

## 10. Enterprise vs OSS Build System

### 10.1 Webpack Alias Pattern

```
@enterprise → app/enterprise (enterprise build)
           → app/_fallbacks/enterprise (OSS build)
```

Every page that uses an enterprise feature imports from `@enterprise/...`. The fallback module exports the same interface but renders a "Contact Us" / upgrade prompt view.

### 10.2 Fallback Structure

`app/_fallbacks/enterprise/components/<feature>/` contains a single `<Feature>View.tsx` rendering a generic upgrade prompt. The fallback `lib/` directory exports:

- Empty Redux slices (`reducers: {}`, `EnterpriseState: {}`)
- Stub `rbacContext` with no-op permission checks
- Pass-through `createBaseQueryWithRefresh` (returns the base query unchanged)
- Empty `clearOAuthStorage()` and `tokenManager`

### 10.3 Runtime Detection

```typescript
const IS_ENTERPRISE = process.env.NEXT_PUBLIC_IS_ENTERPRISE === "true"
```

Used in `baseApi.ts` to decide whether to call `clearOAuthStorage()` and in feature-gated UI rendering.

---

## 11. Component Architecture

### 11.1 Primitive UI Components (`components/ui/`)

All primitives wrap Radix UI for ARIA compliance. CVA (`class-variance-authority`) manages variants. Key components:

| Component | Description |
|-----------|-------------|
| `button.tsx` | Variants: `default`, `destructive`, `outline`, `secondary`, `ghost`, `link`; Sizes: `default`, `sm`, `lg`, `icon`; `isLoading` prop shows `<Loader2>` spinner; `dataTestId` prop |
| `input.tsx` | Controlled text input with error state styling |
| `dialog.tsx` | Modal dialog via Radix Dialog |
| `form.tsx` / `formField.tsx` | React Hook Form integration with inline validation display |
| `codeEditor.tsx` | Monaco Editor wrapper for CEL/JSON/prompt editing |
| `envVarInput.tsx` | Dual-mode input: literal value or `env.VAR_NAME` reference |
| `multiSelect.tsx` | Multi-select with async search support |
| `datePickerWithRange.tsx` | Date range picker for log filters |
| `configSyncAlert.tsx` | Banner showing backend config sync status |
| `headersTable.tsx` | Key-value table for HTTP headers |

### 11.2 Table System (`components/table/`)

Built on TanStack Table v8:

- **`useColumnConfig`** — persists column visibility, order, and pin state to localStorage per table ID
- **`draggableColumnHeader`** — dnd-kit drag-and-drop for column reordering
- **`columnPinning`** — sticky column logic
- **`useTablePageSize`** (in `hooks/`) — `ResizeObserver` on the table container; recalculates page size on resize with 150ms debounce

Page size formula:
```
pageSize = max(floor((containerHeight - headerHeight - statusRowHeight) / rowHeight), 5)
```
Constants: `rowHeight = 48px`, `headerHeight = 44px`, `statusRowHeight = 48px`, `minPageSize = 5`.

### 11.3 Sidebar (`components/sidebar.tsx`)

Collapsible left navigation. Sidebar state (`collapsed`) is stored in `appSlice`. Enterprise-only items are conditionally rendered based on `IS_ENTERPRISE`. Feature-flagged items (MCP, caching, logging) check `appSlice.features`.

### 11.4 Filter Popover (`components/filters/filterPopover.tsx`)

Reusable filter popover used by log and MCP log tables. Filter state is managed via `nuqs` URL search params for sharable filter URLs.

---

## 12. Validation System

**File:** `lib/utils/validation.ts`

### 12.1 `Validator` Class

Chainable validation DSL:

```typescript
new Validator([
  Validator.required(name),
  Validator.minLength(name, 3),
  Validator.url(baseUrl),
  Validator.custom(keys.length > 0, "At least one key required"),
]).isValid()       // boolean
.getErrors()       // string[]
.getFirstError()   // string | undefined
```

Built-in static validators:

| Method | Validates |
|--------|-----------|
| `required(value)` | Not `null`, `undefined`, `""`, or `0` |
| `email(value)` | RFC 5321 pattern |
| `url(value)` | Starts with `http://` or `https://` |
| `minLength(value, n)` | String length ≥ n |
| `maxLength(value, n)` | String length ≤ n |
| `minValue(value, n)` | Numeric ≥ n |
| `maxValue(value, n)` | Numeric ≤ n |
| `pattern(value, regex, msg)` | Regex match |
| `arrayMinLength(arr, n)` | Array length ≥ n |
| `arrayMaxLength(arr, n)` | Array length ≤ n |
| `arrayUnique(arr)` | No duplicates |
| `arraysEqual(a, b)` | Deep equality |
| `custom(bool, msg)` | Arbitrary predicate |
| `all(rules)` | First failing rule wins |

### 12.2 Field-level Validation

`validateField(value, config, touched)` returns `{ isValid, message, showTooltip }`. Tooltips display only after a field is touched (or always if `showAlways: true`).

### 12.3 Specialized Validators

| Function | Purpose |
|----------|---------|
| `isRedacted(value)` | Detects backend-redacted keys (`xxxx****...xxxx` or `env.*`) |
| `isValidOrigin(origin)` | CORS origin: `*`, exact `http(s)://host[:port]`, or `https://*.domain.com` |
| `isValidRedisAddress(addr)` | `host:port`, `[ipv6]:port`, `redis://`, `rediss://` |
| `isValidVertexAuthCredentials(value)` | Redacted, `env.*`, or service account JSON with required fields |
| `isValidDeployments(value)` | Object or JSON string with at least one key |
| `isValidJSON(value)` | `JSON.parse` without throwing |

---

## 13. Key Utilities & Patterns

### 13.1 `formatBytes` (`lib/utils/strings.ts`)

Converts raw byte counts to human-readable strings with one decimal place:

```typescript
formatBytes(1536) // → "1.5 KB"
formatBytes(0)    // → "0 B"
```

### 13.2 `cn()` (`lib/utils.ts`)

`clsx` + `tailwind-merge` combined. Used throughout for conditional className composition.

### 13.3 Provider-specific Constants (`lib/constants/config.ts`)

- `ModelPlaceholders` — per-provider model name hints for form inputs
- `isKeyRequiredByProvider` — map of provider names to whether an API key is mandatory
- `PROVIDER_SUPPORTED_REQUESTS` — map of provider → supported request types (chat, embedding, image, etc.)
- `isRequestTypeDisabled(providerType, requestType)` — checks against the above map

### 13.4 CEL Routing Utilities (`lib/constants/`, `lib/utils/`)

| File | Contents |
|------|----------|
| `celFieldsRouting.ts` | Available CEL field definitions (model, headers, metadata, user) |
| `celOperatorsRouting.ts` | CEL operator definitions per field type |
| `celConverterRouting.ts` | Converts visual query builder state to CEL expression strings |
| `routingRules.ts` | Type definitions and sorting for routing rule display |

### 13.5 `useDebounce` Hook

```typescript
const debouncedValue = useDebounce(searchInput, 300)
```

Used on all search/filter inputs to prevent excessive API calls (≤300ms per SRS PERF-05).

### 13.6 `useCopyToClipboard`

Returns a `copy(text)` function and `isCopied` state (auto-resets after 2s). Used in log detail views and key display.

### 13.7 URL State (`nuqs`)

Log filter parameters (provider, model, status, time range, etc.) are managed via `nuqs` search params. This makes filter state shareable via URL and survives page refresh without additional persistence.

---

## 14. Data Types & Schemas

### 14.1 Provider Types (`lib/types/config.ts`)

Provider configuration uses a discriminated structure per provider type:

| Type | Extra Config |
|------|-------------|
| Standard (OpenAI, Anthropic, etc.) | `value: EnvVar` (API key) |
| `azure` | `AzureKeyConfig` (endpoint, deployments map, API version, OAuth fields) |
| `vertex` | `VertexKeyConfig` (project ID, region, auth credentials / service account JSON) |
| `bedrock` | `BedrockKeyConfig` (access key, secret key, session token, region, ARN, S3 batch config) |
| `replicate` | `ReplicateKeyConfig` (deployment map) |
| `vllm` | `VLLMKeyConfig` (URL, model name) |

**`EnvVar`** is a union type:

```typescript
{ value: string; env_var: string; from_env: boolean }
```

When `from_env: true`, the backend reads the value from the named environment variable.

### 14.2 API Key Redaction

After a key is saved, the backend returns a redacted value:
- Keys > 8 chars: `<first4>` + 24 asterisks + `<last4>` (32 chars total)
- Keys ≤ 8 chars: all asterisks

The UI detects this pattern via `isRedacted()` to avoid re-submitting redacted values as new keys.

### 14.3 Provider Config Types (Governance, `lib/types/governance.ts`)

Includes `DBKey`, `Team`, `Customer`, `Budget`, `RateLimit`, `UsageStat` matching Go backend schemas.

---

## 15. Testing Strategy

### 15.1 Unit Tests (Vitest)

Framework: Vitest 4.0. Configuration in `vitest.config.ts`.

Coverage targets (per SRS MAINT-01): all utility functions in `lib/utils/` must have corresponding unit tests. Current test file location: to be placed co-located or in a `__tests__/` subdirectory.

Priority test targets:
- `Validator` class all static methods
- `isRedacted`, `isValidOrigin`, `isValidRedisAddress`
- `formatBytes`, `celConverterRouting`
- `useTablePageSize` hook
- `getErrorMessage` helper

### 15.2 Integration / E2E

E2E test specs are located in `ui/specs/` (separate from unit tests). Framework: TBD (see `specs/` directory).

### 15.3 Test Utilities

- `dataTestId` prop on `Button` component renders as `data-testid` for selector-stable test targeting.
- All destructive action dialogs use Radix `AlertDialog` — testable via accessible role queries.

---

*This TDD is derived from direct source code analysis of `/ui/` as of 2026-04-05.*
