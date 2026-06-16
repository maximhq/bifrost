## ✨ Features

- **pprof Profiling Server** - Optional runtime profiling server gated by `BIFROST_PPROF_PORT`, with env-tunable block/mutex sampling rates and graceful shutdown alongside the main server.

## 🐞 Fixed

- **GenAI Raw Request Passthrough** - Native Vertex/Gemini batch and request bodies now follow the `x-model-provider` header and pass through verbatim only when Gemini or Vertex is explicitly selected, preventing a raw body from reaching a mismatched provider.
- **Tool Call Metadata Preservation** - `extra_content` on assistant tool calls (e.g. Gemini `thought_signature`) is now preserved across both streaming and non-streaming responses (thanks [@nghodkicisco](https://github.com/nghodkicisco)!).
