- feat: VK provider config key_ids now supports ["*"] wildcard to allow all keys; empty key_ids denies all; handler resolves wildcard to AllowAllKeys flag without DB key lookups
- feat: add option to disable automatic MCP tool injection per request
- feat: virtual key MCP configs now act as an execution-time allow-list — tools not permitted by the VK are blocked at inference and MCP tool execution
- refactor: standardize empty array conventions in bifrost. Empty array means no tools/keys are allowed, ["*"] means all tools/keys are allowed.
- feat: add support for request level extra headers in MCP tool execution.
- fix: add support for `x-bf-mcp-include-clients` and `x-bf-mcp-include-tools` request headers to filter MCP tools/list response when using bifrost as an MCP gateway.
- refactor: parallelize model listing for providers to speed up startup time.
- fix: send back accumulated usage in MCP agent mode.
- feat: MCP edit UI now supports assigning virtual keys with per-tool access control directly from the MCP server edit sheet.
- feat: adds option to allow MCP clients to run on all virtual keys without explicit assignment.
- feat: add support for pricing overrides.

<Warning>
**v1.5.0 contains multiple breaking changes** to how Bifrost interprets empty arrays and wildcard values across Virtual Keys, provider keys, and MCP configurations. Existing deployments are protected by automatic database migrations — but any **new** configuration created after upgrading must follow the new semantics described below.
</Warning>

## What Changed (The Short Version)

v1.5.0 flips the meaning of empty arrays across all allow-list fields:

| What you write | v1.4.x meaning | v1.5.0 meaning |
|---|---|---|
| `[]` (empty array) | ✅ Allow **all** | ❌ Allow **none** (deny by default) |
| `["*"]` (wildcard) | Not applicable | ✅ Allow **all** |
| `["a", "b"]` | Only `a` and `b` | Only `a` and `b` (unchanged) |

**The old behavior was "allow all unless restricted." The new behavior is "deny all unless explicitly permitted."**

This affects:
1. Provider key `models` field — which models a key can serve
2. Virtual Key `provider_configs[].allowed_models` — which models a VK can use per provider
3. Virtual Key `provider_configs[].key_ids` — which API keys a VK can use (also renamed from `allowed_keys`)
4. Virtual Key `mcp_configs[].tools_to_execute` — which MCP tools a VK can execute
5. Virtual Key `provider_configs` itself — which providers a VK can access

There are also two additional structural changes:
- `allowed_keys` field renamed to `key_ids` in VK provider configs
- `weight` field is now optional (nullable) on VK provider configs

---

## Automatic Migration for Existing Data

<Note>
**If you are running Bifrost with a database** (SQLite or Postgres), all existing data is automatically migrated on startup. You do not need to manually update your database records.

The following automatic migrations run on upgrade:
- All provider keys with `models: []` are converted to `models: ["*"]`
- All virtual key provider configs with `allowed_models: []` are converted to `allowed_models: ["*"]`
- All virtual keys with no `provider_configs` get backfilled with all currently configured providers (with `allowed_models: ["*"]` and `key_ids: ["*"]`)
- All virtual keys with no `mcp_configs` get backfilled with all currently connected MCP clients (with `tools_to_execute: ["*"]`)
</Note>

<Warning>
**This migration is not revertible.** Although all migrations are correctly handled automatically, it is recommended to **make a backup copy of your config store database** before upgrading to v1.5.0-prerelease1. If anything goes wrong, a backup is the only way to restore your previous state.
</Warning>

**The automatic migration only protects your existing data.** If you also define your configuration through `config.json` or manage virtual keys via the API, you must update those manually using this guide.

---

## Breaking Change 1: Provider Key `models` Field

**Who is affected:** Anyone who configures provider keys in `config.json` or programmatically with the field `models` absent or set to `[]`.

### What changed

The `models` field on a provider key previously defaulted to "allow all" when empty. It now means "allow none." You must explicitly use `["*"]` to allow a key to serve all models.

<Tabs>
<Tab title="config.json">

**Before (v1.4.x):**
```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "id": "key-openai-1",
          "value": "env.OPENAI_API_KEY",
          "models": []
        }
      ]
    }
  }
}
```
`models: []` → key served all models

**After (v1.5.0):**
```json
{
  "providers": {
    "openai": {
      "keys": [
        {
          "id": "key-openai-1",
          "value": "env.OPENAI_API_KEY",
          "models": ["*"]
        }
      ]
    }
  }
}
```
`models: ["*"]` → key serves all models

</Tab>
<Tab title="Specific Model List (unchanged)">

If you already specify explicit models, no change is needed:

```json
{
  "id": "key-openai-gpt4-only",
  "value": "env.OPENAI_API_KEY",
  "models": ["gpt-4o", "gpt-4o-mini"]
}
```

This behaves the same in both versions — only the listed models are served.

</Tab>
</Tabs>

### How to update

Search your `config.json` for any provider key that has `"models": []` or no `models` field at all, and add `"models": ["*"]` to restore the "allow all" behavior.

<Tip>
The `models` field on provider keys acts as a hard floor: even if a Virtual Key's `allowed_models` permits a model, the provider key must also have that model in its `models` list. If `models` is empty, the key will never serve any requests, regardless of Virtual Key settings.
</Tip>

---

## Breaking Change 2: Virtual Key `allowed_models` Field

**Who is affected:** Anyone who creates or updates Virtual Keys via `config.json`, the REST API, or the SDK with `allowed_models` absent or set to `[]`.

### What changed

The `allowed_models` field on a Virtual Key provider config previously defaulted to "allow all models for this provider" when empty. It now means "block all models from this provider." You must use `["*"]` to allow all models.

<Tabs>
<Tab title="config.json">

**Before (v1.4.x):**
```json
{
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-my-app",
        "provider_configs": [
          {
            "provider": "openai",
            "weight": 1.0
          }
        ]
      }
    ]
  }
}
```
Missing `allowed_models` → all OpenAI models allowed

**After (v1.5.0):**
```json
{
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-my-app",
        "provider_configs": [
          {
            "provider": "openai",
            "allowed_models": ["*"],
            "weight": 1.0
          }
        ]
      }
    ]
  }
}
```
`allowed_models: ["*"]` → all OpenAI models allowed

</Tab>
<Tab title="REST API">

**Before (v1.4.x):**
```bash
curl -X POST http://localhost:8080/api/governance/virtual-keys \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-app-key",
    "provider_configs": [
      {
        "provider": "openai",
        "weight": 1.0
      }
    ]
  }'
```
Missing `allowed_models` → all OpenAI models allowed

**After (v1.5.0):**
```bash
curl -X POST http://localhost:8080/api/governance/virtual-keys \
  -H "Content-Type: application/json" \
  -d '{
    "name": "my-app-key",
    "provider_configs": [
      {
        "provider": "openai",
        "allowed_models": ["*"],
        "weight": 1.0
      }
    ]
  }'
```

</Tab>
</Tabs>

---

## Breaking Change 3: Virtual Key Provider Configs — Deny-by-Default

**Who is affected:** Anyone creating Virtual Keys with no `provider_configs` array (or an empty one), expecting those keys to have unrestricted provider access.

### What changed

In v1.4.x, a Virtual Key with no `provider_configs` had access to all configured providers. In v1.5.0, a Virtual Key with no `provider_configs` blocks all providers by default.

**Before (v1.4.x):** Virtual Key with `provider_configs: []` → access to all providers

**After (v1.5.0):** Virtual Key with `provider_configs: []` → no provider access (all blocked)

### How to update

Every Virtual Key must now explicitly list the providers it is permitted to use. To allow access to all providers, add a config entry per provider with `"allowed_models": ["*"]`.

```json
{
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-unrestricted",
        "provider_configs": [
          { "provider": "openai",    "allowed_models": ["*"], "key_ids": ["*"], "weight": 1.0 },
          { "provider": "anthropic", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1.0 },
          { "provider": "azure",     "allowed_models": ["*"], "key_ids": ["*"], "weight": 1.0 }
        ]
      }
    ]
  }
}
```

<Tip>
The automatic database migration handles this for you on existing virtual keys — it backfills all currently configured providers into any VK that has an empty `provider_configs`. However, any VK you create after upgrading (via API or config.json) must include explicit provider configs.
</Tip>

---

## Breaking Change 4: `allowed_keys` Renamed to `key_ids`

**Who is affected:** Anyone using the `allowed_keys` field in Virtual Key provider configs in `config.json` or the REST API.

### What changed

The field used to restrict which provider API keys a Virtual Key can use has been renamed from `allowed_keys` to `key_ids`. The semantics follow the same deny-by-default model as all other v1.5.0 whitelist fields:

| Value | v1.4.x behavior | v1.5.0 behavior |
|---|---|---|
| Field absent / `[]` | Allow all keys | **Deny all keys** |
| `["*"]` | Not applicable | Allow all keys (explicit wildcard) |
| `["key-1"]` | Only key-1 | Only key-1 (unchanged) |

<Note>
Unlike `allowed_models`, there is no automatic database migration for `key_ids`. The "allow all when empty" behavior is **not preserved** — an empty or omitted `key_ids` sets `allow_all_keys: false` internally, which blocks all key selection. You must explicitly use `["*"]` to restore allow-all behavior.
</Note>

<Tabs>
<Tab title="Allow all keys (most common)">

**Before (v1.4.x):** `allowed_keys` omitted or `[]` → all keys allowed
```json
{
  "provider_configs": [
    {
      "provider": "openai",
      "weight": 1.0
    }
  ]
}
```

**After (v1.5.0):** must use `["*"]` explicitly — omitting `key_ids` or leaving it `[]` now blocks all keys
```json
{
  "provider_configs": [
    {
      "provider": "openai",
      "key_ids": ["*"],
      "allowed_models": ["*"],
      "weight": 1.0
    }
  ]
}
```

</Tab>
<Tab title="Specific keys">

**Before (v1.4.x):**
```json
{
  "provider_configs": [
    {
      "provider": "openai",
      "allowed_keys": ["key-prod-001"],
      "weight": 1.0
    }
  ]
}
```

**After (v1.5.0):** rename the field, no value change needed
```json
{
  "provider_configs": [
    {
      "provider": "openai",
      "key_ids": ["key-prod-001"],
      "allowed_models": ["*"],
      "weight": 1.0
    }
  ]
}
```

</Tab>
<Tab title="REST API">

**Before (v1.4.x):**
```bash
curl -X PUT http://localhost:8080/api/governance/virtual-keys/{vk_id} \
  -d '{
    "provider_configs": [
      {
        "provider": "openai",
        "allowed_keys": ["key-prod-001"],
        "weight": 1.0
      }
    ]
  }'
```

**After (v1.5.0):**
```bash
curl -X PUT http://localhost:8080/api/governance/virtual-keys/{vk_id} \
  -d '{
    "provider_configs": [
      {
        "provider": "openai",
        "key_ids": ["key-prod-001"],
        "allowed_models": ["*"],
        "weight": 1.0
      }
    ]
  }'
```

</Tab>
</Tabs>

---

## Breaking Change 5: Virtual Key MCP `tools_to_execute` Field

**Who is affected:** Anyone configuring MCP tool filtering on Virtual Keys in `config.json` or the REST API.

### What changed

The `tools_to_execute` field on a Virtual Key MCP config previously defaulted to "allow all tools" when empty. It now means "block all tools from this client." You must use `["*"]` to allow all tools.

Additionally, the Virtual Key `mcp_configs` list itself now acts as a strict allow-list:
- **No `mcp_configs`** → all MCP tools blocked for this VK
- **`mcp_configs` with entries** → only the listed clients and tools are accessible

<Tabs>
<Tab title="config.json">

**Before (v1.4.x):**
```json
{
  "mcp_configs": [
    {
      "mcp_client_name": "my-tools-server",
      "tools_to_execute": []
    }
  ]
}
```
Empty `tools_to_execute` → all tools from `my-tools-server` allowed

**After (v1.5.0):**
```json
{
  "mcp_configs": [
    {
      "mcp_client_name": "my-tools-server",
      "tools_to_execute": ["*"]
    }
  ]
}
```
`["*"]` → all tools from `my-tools-server` allowed

</Tab>
<Tab title="REST API">

**Before (v1.4.x):**
```bash
curl -X PUT http://localhost:8080/api/governance/virtual-keys/{vk_id} \
  -d '{
    "mcp_configs": [
      { "mcp_client_name": "billing-client", "tools_to_execute": [] }
    ]
  }'
```

**After (v1.5.0):**
```bash
curl -X PUT http://localhost:8080/api/governance/virtual-keys/{vk_id} \
  -d '{
    "mcp_configs": [
      { "mcp_client_name": "billing-client", "tools_to_execute": ["*"] }
    ]
  }'
```

</Tab>
</Tabs>

**MCP tool filtering semantics at a glance:**

| `mcp_configs` | `tools_to_execute` | Result |
|---|---|---|
| Not configured | N/A | All MCP tools blocked |
| `[{ client: "X" }]` | `[]` | All tools from X blocked |
| `[{ client: "X" }]` | `["*"]` | All tools from X allowed |
| `[{ client: "X" }]` | `["tool-a"]` | Only `tool-a` from X allowed |
| `[{ client: "X" }, { client: "Y" }]` | `["*"]` / `["*"]` | All tools from X and Y allowed; all other clients blocked |

---

## Breaking Change 6: `weight` Field is Now Optional

**Who is affected:** Anyone programmatically processing or constructing Virtual Key provider config objects via the API.

### What changed

The `weight` field on a Virtual Key provider config was previously a required `float64`. It is now an optional nullable value (`*float64`). The new semantics:

- **`weight: 0.5`** — provider participates in weighted load balancing
- **`weight: null` or omitted** — provider is configured and accessible but is **excluded from weighted routing** (it can still be used for direct `provider/model` requests or fallbacks)

This allows you to configure a provider on a VK for fallback purposes without including it in the normal weighted selection pool.

**API response change:** The `weight` field may now be `null` in API responses. Update any client code that assumes `weight` is always a number.

```json
{
  "provider_configs": [
    {
      "provider": "openai",
      "allowed_models": ["*"],
      "weight": 0.8
    },
    {
      "provider": "anthropic",
      "allowed_models": ["*"],
      "weight": null
    }
  ]
}
```
In this example: 100% of weighted traffic goes to OpenAI. Anthropic is still reachable via `anthropic/claude-3-5-sonnet-20241022` direct routing or as a manual fallback, but won't receive any traffic from weighted load balancing.

---

## New Validation: WhiteList Rules

v1.5.0 introduces the `WhiteList` type, which enforces two new validation rules on all allow-list fields. The API now returns **HTTP 400** if either rule is violated.

### Rule 1: Wildcard cannot be mixed with other values

`["*"]` is only valid when it is the sole element. Using it alongside specific values is a validation error.

```json
// ❌ Invalid — rejected with 400
{ "allowed_models": ["*", "gpt-4o"] }

// ✅ Valid
{ "allowed_models": ["*"] }

// ✅ Valid
{ "allowed_models": ["gpt-4o", "gpt-4o-mini"] }
```

### Rule 2: No duplicate values

```json
// ❌ Invalid — rejected with 400
{ "allowed_models": ["gpt-4o", "gpt-4o"] }

// ✅ Valid
{ "allowed_models": ["gpt-4o", "gpt-4o-mini"] }
```

These rules apply to: `allowed_models`, `key_ids`, `models` (on provider keys), `tools_to_execute`, `tools_to_auto_execute`, and `allowed_extra_headers`.

---

## Complete Before/After Reference

### Provider Key (config.json)

**Before:**
```json
{
  "providers": {
    "openai": {
      "keys": [
        { "id": "key-1", "value": "env.OPENAI_API_KEY", "weight": 1 }
      ]
    }
  }
}
```

**After:**
```json
{
  "providers": {
    "openai": {
      "keys": [
        { "id": "key-1", "value": "env.OPENAI_API_KEY", "weight": 1, "models": ["*"] }
      ]
    }
  }
}
```

---

### Simple Virtual Key (config.json)

**Before:**
```json
{
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-simple",
        "provider_configs": [
          { "provider": "openai", "weight": 1.0 }
        ]
      }
    ]
  }
}
```

**After:**
```json
{
  "governance": {
    "virtual_keys": [
      {
        "id": "vk-simple",
        "provider_configs": [
          { "provider": "openai", "allowed_models": ["*"], "key_ids": ["*"], "weight": 1.0 }
        ]
      }
    ]
  }
}
```

---

### Virtual Key with Key Restrictions (config.json)

**Before:**
```json
{
  "provider_configs": [
    {
      "provider": "openai",
      "allowed_keys": ["key-prod-001"],
      "weight": 0.5
    }
  ]
}
```

**After:**
```json
{
  "provider_configs": [
    {
      "provider": "openai",
      "key_ids": ["key-prod-001"],
      "allowed_models": ["*"],
      "weight": 0.5
    }
  ]
}
```

---

### Virtual Key with MCP Tools (config.json)

**Before:**
```json
{
  "mcp_configs": [
    { "mcp_client_name": "tools-server", "tools_to_execute": [] }
  ]
}
```

**After:**
```json
{
  "mcp_configs": [
    { "mcp_client_name": "tools-server", "tools_to_execute": ["*"] }
  ]
}
```

---

### Full Virtual Key (REST API)

**Before:**
```bash
curl -X POST http://localhost:8080/api/governance/virtual-keys \
  -H "Content-Type: application/json" \
  -d '{
    "name": "prod-key",
    "provider_configs": [
      {
        "provider": "openai",
        "allowed_keys": ["key-prod-001"],
        "weight": 0.7
      },
      {
        "provider": "anthropic",
        "weight": 0.3
      }
    ],
    "mcp_configs": [
      { "mcp_client_name": "my-mcp", "tools_to_execute": [] }
    ]
  }'
```

**After:**
```bash
curl -X POST http://localhost:8080/api/governance/virtual-keys \
  -H "Content-Type: application/json" \
  -d '{
    "name": "prod-key",
    "provider_configs": [
      {
        "provider": "openai",
        "key_ids": ["key-prod-001"],
        "allowed_models": ["*"],
        "weight": 0.7
      },
      {
        "provider": "anthropic",
        "key_ids": ["*"],
        "allowed_models": ["*"],
        "weight": 0.3
      }
    ],
    "mcp_configs": [
      { "mcp_client_name": "my-mcp", "tools_to_execute": ["*"] }
    ]
  }'
```

---

## Breaking Change 7: Compact plugin support two new modes

**Who is affected:** Anyone using the `compact` plugin.

### What changed

The `compact` plugin now supports two new modes:

- Chat to responses fallback: If Chat completion API is hit for models that only support Responses API, the chat completion routed via the responses api.
- OpenAI Compatible paramseters dropping: If a model does not support any standard OpenAI compatible model parameter, it is dropped.

The option `enable_litellm_fallback` is removed and replaced with:
- `compat.convert_text_to_chat`: Enable text completion to chat completion fallback (original behavior)
- `compat.convert_chat_to_responses`: Enable chat completion to responses fallback
- `compat.should_drop_darams`: Enable OpenAI Compatible parameters dropping

The following fields are removed or added to the response:
- `extra_fields.litellm_compat` is removed
- `extra_fields.dropped_compat_plugin_params` returns which parameters were dropped by this plugin
- `extra_fields.converted_request_type` returns the type of convert to (NOTE: If the request is a streaming request, this will still be base request type. For streaming chat completions, converted_request_type will be "chat_completions" and not "chat_completion_stream")

---

## Quick Migration Checklist

Use this checklist when upgrading to v1.5.0:

<Steps>
<Step title="Update provider key models in config.json">
Find every provider key in your `config.json` that has `"models": []` or no `models` field. Add `"models": ["*"]` to each.
</Step>

<Step title="Add allowed_models to every VK provider config">
Find every `provider_configs` entry in your Virtual Keys (in `config.json` and any automation that calls the API). Add `"allowed_models": ["*"]` if you want all models, or list specific models you want to allow.
</Step>

<Step title="Ensure every VK has at least one provider config">
Any Virtual Key with `"provider_configs": []` or no `provider_configs` will block all traffic. Add the providers you want to allow.
</Step>

<Step title="Rename allowed_keys to key_ids and set explicit values">
Search your `config.json` and API calls for `"allowed_keys"` and rename it to `"key_ids"`. Then:
- If `allowed_keys` was omitted or `[]` (previously meaning "allow all"), change to `"key_ids": ["*"]` — an empty or omitted `key_ids` now **blocks all keys**.
- If `allowed_keys` listed specific key IDs, keep the same values — only the field name changes.
</Step>

<Step title="Update tools_to_execute for MCP configs">
Change any `"tools_to_execute": []` to `"tools_to_execute": ["*"]` if you want to allow all tools. Ensure every VK that needs MCP access has at least one `mcp_configs` entry.
</Step>

<Step title="Handle nullable weight in API consumers">
If you parse the Virtual Key API response in your code, update the `weight` field handler to accept `null` values in addition to numbers.
</Step>

<Step title="Fix any invalid WhiteList values">
Check that none of your lists mix `"*"` with other values (e.g., `["*", "gpt-4o"]`), and that none have duplicate entries. These will now be rejected with HTTP 400.
</Step>
</Steps>

---

## Troubleshooting

### All requests are returning 403/blocked after upgrade

This usually means a provider key has `models: []` or a Virtual Key has no `provider_configs`, or a provider config has `allowed_models: []`. Check the Bifrost logs — a blocked request will log which rule denied it.

**Fix:** Follow the checklist above. Ensure `models: ["*"]` on provider keys and `allowed_models: ["*"]` on VK provider configs.

### MCP tools are not being injected / tool calls are blocked

The VK needs an `mcp_configs` entry for the MCP client, and that entry needs `"tools_to_execute": ["*"]` (or a specific tool list).

**Fix:** Add or update the `mcp_configs` on the Virtual Key.

### API returning 400 on virtual key create/update

The most common cause is a whitelist validation failure — either `["*", "gpt-4o"]` mixing a wildcard with a specific value, or a duplicate value in a list.

**Fix:** Use only `["*"]` alone or a list of specific values without duplicates.

### Requests fail with "no keys available" or key selection errors after upgrade

A provider config with `key_ids` omitted or set to `[]` now **blocks all keys** (`allow_all_keys: false`, no specific keys configured). This is different from `allowed_models` — there is no automatic migration for `key_ids`.

**Fix:** Add `"key_ids": ["*"]` to any provider config that previously had `allowed_keys: []` or no `allowed_keys` field.

### Existing keys work fine but newly created keys are blocked

The automatic migration only updates existing data. New keys created after upgrade must follow the new semantics. Use `["*"]` for all allow-list fields where you want unrestricted access.