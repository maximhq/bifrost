## 🐞 Fixed

- **GenAI streams no longer send SSE heartbeats.** The official `google-genai` Python SDK (and LangChain's `ChatGoogleGenerativeAI` on top of it) parses every non-`data:` stream line as error JSON, so the `: heartbeat` comment aborted `/genai` streams with `UnknownApiResponseError` at the first idle second. The delimited comment block introduced for the JavaScript SDK did not help Python. The typed GenAI route and Gemini/Vertex SSE passthrough now opt out of the heartbeat via the new `lib.SSEHeartbeatNone` framing and rely on reactive disconnect detection, the same trade-off the Bedrock route already makes. Every other route keeps its heartbeat unchanged.

Affected packages:
- transports/bifrost-http/lib
- transports/bifrost-http/integrations
