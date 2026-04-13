## ✨ Features

- **OAuth MCP** - add next-step hints to OAuth MCP client creation response
- **Azure passthrough** - added azure passthrough support
- **272k token tier** - add 272k token tier pricing support in pricing
- **Flex and priority tier support** - added flex and priority tier support in pricing

## 🐞 Fixed

- **Streaming Post-Hook Race** — Fix race condition where fasthttp RequestCtx could be recycled before transport post-hooks complete in streaming goroutines; eagerly captures request/response snapshots before handler returns
- **Async User Values** — Propagate user values through all async inference handlers and job submissions
- **Trace Completer Safety** — Refactor trace completer to accept transport logs as parameter instead of reading from potentially recycled context
- **Async Log Store Exceptions** — Fix exception handling in async log store jobs
- **Model Alias Tracking** — Split ModelRequested into OriginalModelRequested and ResolvedModelUsed for accurate model alias resolution tracking
- **MCP Tool Discovery** — Add discovered tools and tool name mapping columns to MCP clients
