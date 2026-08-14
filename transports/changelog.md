## ✨ Features

- **URL Sources Inlined for AWS-Hosted Claude** - URL-sourced images and documents are fetched and inlined on the native-Anthropic path, since Bedrock Mantle rejects `{"source":{"type":"url"}}`. Fetches go through the SSRF-safe dialer with a size cap, and a failed fetch aborts the request rather than silently dropping an attachment.
- **Quarterly Budgets for Customers** - Quarterly budgets are now supported by the customer entity, and by virtual key provider configs.
- **Fiscal Year Start in Budget Labels** - Budget UI labels surface the configured fiscal year start through a new `fiscalQuarterNote` helper, and `QuarterStartSelect` is relaid out to a horizontal label and preview row with a right-aligned select.
- **Flexible Entity Selector Width** - The entity selector accepts `contentClassName`, so callers can widen or constrain its dropdown instead of being pinned to the default width.

## 🐞 Fixed

- **WebSocket Writes After Disconnect** - A broadcast racing a client disconnect could panic on a nil connection or deliver to an unrelated client's socket, because fasthttp recycles the hijacked connection as soon as the upgrade handler returns. Clients now carry an explicit closed flag and a close that blocks until in-flight writes finish.
- **Realtime Heartbeat Panic on Disconnect** - `stopHeartbeat` waits for the heartbeat goroutine to exit before the upgrade handler returns. A ping already inside `WriteMessage` would dereference a recycled connection, and unlike the broadcast path there is no recover, so the panic took down the whole process.
- **Stop Sequences Dropped for Nova and Titan** - Bedrock's Converse camelCase `stopSequences` now maps to the neutral `stop` parameter alongside Anthropic's `stop_sequences`. 81 catalog rows were silently losing `stop`, so the provider ran to `end_turn` instead of stopping.
- **Reasoning Replay Rejected on Chat-Shaped Requests** - `/v1/chat/completions` and `/v1/messages` carry replayed reasoning on `reasoning_details`, but the fail-soft strip only handled Responses-shaped items. A router that switched models mid-conversation returned "messages.N.content.0: Invalid `signature` in `thinking` block" to the client instead of retrying without the signature.
- **Thinking Signatures on Responses Content Blocks** - Signatures are stripped off content blocks, not just `encrypted_content` on the reasoning item. A message could need the strip with `encrypted_content` already absent, and only reasoning items are dropped when nothing survives, so an ordinary message keeps its own content.
- **Reasoning Content Rejected by OpenAI and Azure Models** - `reasoning.content` is no longer sent to non-gpt-oss reasoning models, which cap the array at zero entries and reject a populated one with "Invalid 'input[N].content': array too long". Replayed Anthropic thinking blocks were hitting this; `summary` and `encrypted_content` already carry everything those models accept.
- **Reasoning Effort Cleared for Current Grok Models** - The rule substring-matched "grok-3-mini", so `grok-4.5`, `grok-4.6` and `grok-4.20-multi-agent` silently lost `reasoning_effort` and answered at the wrong reasoning depth, cost and latency. Replaced with an exact-match deny-list that normalizes routing prefixes, `-latest` and xAI's 4-digit date suffixes.
- **xhigh Reasoning Effort Downgraded for Grok** - The shared OpenAI-dialect normalizer downgraded `xhigh` to `high` before the xAI compat pass ran, losing the value even with the deny-list corrected. `grok-4.5` still downgrades on purpose, matching xAI's documented upstream coercion.
- **Empty Structured-Output Streams** - `content_part.added`, `output_text.delta`, `output_text.done` and `content_part.done` are emitted when a tool-based structured-output call is reassembled into a message on the Responses streaming path. Only `output_item.added` and `done` were emitted, so consumers reading incremental events saw a stream with no text while tokens were billed. Affects Vertex, Bedrock Mantle and Azure Claude.
- **HTTP 529 Rotating Credentials** - Anthropic's `overloaded_error` is treated as a transient server error. It reflects capacity across all callers rather than anything about the credential, so the same key is retried with backoff instead of being rotated away.

## 🗄️ Database Migrations

- No new database migrations in this release.
