- [feat]: DeepSeek web tools at `POST /v1/tools/search` and `POST /v1/tools/fetch`, routed to DeepSeek when a body carries no model [@jimaldon](https://github.com/jimaldon)
## ✨ Features

- **Virtual Key Metadata in config.json** - `governance.virtual_keys[].metadata` accepts string key/value pairs (for example `cost_center`, `owner`) for cost attribution. Keys use letters, digits, `.`, `_` and `-`; values are up to 512 characters; up to 50 entries [@jimseiwert](https://github.com/jimseiwert)
- **Spend by Virtual Key Metadata** - Each log entry now carries a snapshot of its virtual key's metadata, so logs and dashboard spend can be filtered with the existing metadata filters (for example `metadata_cost_center=cc-42`). On a key collision the virtual key's value overrides `x-bf-lh-*` headers, configured logging headers and `x-bf-dim-*` dimensions, so callers cannot spoof attribution [@jimseiwert](https://github.com/jimseiwert)
- **Virtual Key Metadata API** - Create and update virtual keys with a `metadata` object (on update, omit to keep, `{}` to clear); invalid metadata returns a 400 naming the key. The virtual key list accepts `metadata_<key>=<value>` filters (AND-ed) and `search` also matches metadata [@jimseiwert](https://github.com/jimseiwert)
- **Virtual Key Metadata in the UI** - The virtual key sheet has a Metadata editor (validated like the API, editable on access-profile-managed keys too), the details sheet lists entries with a "View spend" link to the filtered logs, and the virtual keys table has a metadata filter [@jimseiwert](https://github.com/jimseiwert)

## 🐞 Fixed

- **OpenAI-compatible xhigh Reasoning Effort** - `reasoning_effort: "xhigh"` is forwarded to vLLM, Ollama, SGL, and custom OpenAI-compatible providers instead of being rewritten to `high` by OpenAI model-name rules (thanks [@pranavthakur0-0](https://github.com/pranavthakur0-0)!) (#6193)
- **Request Body Memory Retention** - Pooled keep-alive connections no longer keep their largest-ever request-body buffer. fasthttp kept one buffer per connection at its high-water mark (and handed it to the next connection), so per-pod memory ratcheted up with large LLM payloads and never came back. Buffers above `server.body_buffer_pool_limit_bytes` (default 4 MiB) are now released after each request; larger bodies are still accepted up to `max_request_body_size_mb`. Set `0` for unlimited (the previous behaviour)
- **Async Raw Request Body** - Async jobs (`x-bf-async`) that forward the raw request body now copy it, instead of reading a buffer the next request on the same connection can overwrite

## 🗄️ Database Migrations

- `add_virtual_key_metadata_column` - adds the nullable `metadata` column to `governance_virtual_keys`. Existing keys keep NULL. Non-rollbackable: dropping the column would delete every key's metadata; older binaries ignore it.
