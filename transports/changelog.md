## ✨ Features

- **First-Time Setup Token** - New installs now require a setup token before the initial dashboard setup can be completed, so a fresh instance is no longer open to anyone who can reach it (#7830)
- **Long-Context Fast Tier Pricing** - Added ultrafast and priority above-272k pricing columns so fast-tier requests over 272k tokens, including cache writes, bill at the published long-context rates (#7834, #7838)

## 🐞 Fixed

- **Route parsing bug fix** - Auth whitelist and temp-token scope checks fixes fasthttp routing bug
- **Code Mode Auto-Execute Allow List** - `tools_to_execute` and `tools_to_auto_execute` are now enforced at invocation time inside code mode, so indirect calls like `getattr(server, name)(...)` or a plugin tool rename cannot bypass them. Approved runs through `/v1/mcp/tool/execute` are bound only by `tools_to_execute` (#7833)
- **OpenAI service_tier Fast Billing** - Requests with `service_tier` fast now bill at the priority rates, and the tier is echoed back to clients (#7837)
- **Gemini to OpenAI Fallback** - Fallbacks from Gemini to OpenAI Responses now strip fields OpenAI rejects: item `status`, generated reasoning and function output IDs, function output `name`, and content signatures (#7835)
- **Guardrail Redaction Alignment** - Anthropic raw transform targets now match normalized guardrail ordinals when billing headers or MCP blocks are present, so redaction hits the right fields (#7808)

## 🗄️ Database Migrations

- **add_ultrafast_above_272k_pricing_columns** - Adds ultrafast above-272k pricing columns to the model pricing table. Reversible: rollback drops the added columns. Nullable additions, safe for rolling deploys.
- **add_priority_above_272k_cache_creation_pricing_column** - Adds the priority above-272k cache creation pricing column. Reversible: rollback drops the added column. Nullable addition, safe for rolling deploys.
