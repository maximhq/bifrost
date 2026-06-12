<Warning>
The `disable_auth_on_inference` (`DisableAuthOnInference`) config field, deprecated in v1.4.0, has been completely removed. Use `enforce_auth_on_inference` (`EnforceAuthOnInference`) instead, which enforces API key authentication on inference endpoints.
</Warning>

## ✨ Features

- **Vertex AI Files & Batches API** - Added support for the Vertex AI Files and Batches endpoints
- **Vault Backends for Secrets** - Sensitive config fields can now be stored in AWS Secrets Manager, GCP Secret Manager, or HashiCorp Vault as an alternative to AES encryption
- **Per-Alias Provider Overrides** - Key aliases now support alias-level Azure endpoint/API version/Anthropic version, Bedrock region/ARN, Vertex project/region, and Replicate deployments-endpoint overrides
- **MCP Server Library** - New browsable MCP server catalog with background sync, search and filters, install sheet, custom entries with soft-delete, and a multi-harness agent connect sheet
- **Complexity Router** - Route requests by prompt complexity using `complexity_tier` CEL expressions with a configurable analyzer (config UI, DB, and API included)
- **Per-Model Usage in Quota API** - The virtual key quota API now reports usage broken down per model
- **OTEL HTTP Metrics & Span Filtering** - The OTEL connector now emits HTTP-level metrics, and plugin spans can be filtered per connector via `plugin_span_filters`
- **Canonical Model Name in Logs** - Added `canonical_model_name` and `alias_model_family` columns to logs, and request metadata is now included in object-storage log exports
- **Routing Audit Trail** - Responses and errors now carry `RoutingInfo` extra fields with a retry/fallback audit trail from the core routing engine
- **`key_ids` in Provider Config** - Providers can be scoped to specific keys via the new `key_ids` field in the config schema and Helm chart
- **Datadog Env Vars in Helm** - Added support for DD environment variables in the Helm chart
- **Anthropic Fable Compatibility** - Added support for Anthropic Fable models, including fast mode pricing fixes

## 🐞 Fixed

- “Allow All” in vk provider config now properly  routes to all allowed models in key configurations
- **Postgres Logstore Filters** - Fixed metadata filters and pagination `total_count` for the Postgres logstore (thanks [@zbloss](https://github.com/zbloss)!)
- **Vertex Embeddings API Key Auth** - The Vertex Embedding method now supports API key authentication (thanks [@TransactCharlie](https://github.com/TransactCharlie)!)
- **Bedrock Cohere Usage** - Cohere embed/rerank usage on Bedrock is now filled from the response header (thanks [@Alishark14](https://github.com/Alishark14)!)
- **OpenAI File Upload** - Fixed `expires_at` fields in OpenAI file uploads
- **Virtual Key Handling** - Generate a UUID when a virtual key is created without an ID, propagate the VK in GenAI file upload sessions, stamp the VK tool allowlist when the `include-clients` filter is present, and enforce the VK tool-grant boundary on caller-provided `x-bf-mcp-include-*` headers
- **Governance Log Mappings** - Fixed teams and customers name mappings on logs, the customer FK column issue, and added a unique-name constraint migration on the customer table
- **DeepSeek v4 Reasoning** - Fixed max reasoning effort handling for DeepSeek v4
- **Gemini Tool Responses** - Fixed parts handling in Gemini tool responses
- **OpenRouter Cache Control** - `cache_control` blocks are now preserved in OpenRouter chat requests
- **Trace Attributes** - Refactored tracers to correctly set trace-level attributes
- **Provider Config Preservation** - Use the in-file provider config when preserving a failed provider config instead of the existing runtime config
