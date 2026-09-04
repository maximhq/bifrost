## ✨ Features

- **Virtual Key Rotation Cooldown** - New `client.vk_rotation_cooldown` setting (duration string, e.g. "5m"): after a rotation the previous key value keeps authenticating until the grace window expires. config.json VK sync now treats a changed value as an explicit rotation (with console warning) and recognizes the previously rotated-out value as "no change".

## 🐞 Fixed

- **Bedrock Null Content on Empty Assistant Messages** - An assistant message with no text and no tool calls no longer serializes as `content:null`, which Converse rejected outright (#2765)
- **GenAI streams no longer send SSE heartbeats.** The official `google-genai` Python SDK (and LangChain's `ChatGoogleGenerativeAI` on top of it) parses every non-`data:` stream line as error JSON, so the `: heartbeat` comment aborted `/genai` streams with `UnknownApiResponseError` at the first idle second. The delimited comment block introduced for the JavaScript SDK did not help Python. The typed GenAI route and Gemini/Vertex SSE passthrough now opt out of the heartbeat via the new `lib.SSEHeartbeatNone` framing and rely on reactive disconnect detection, the same trade-off the Bedrock route already makes. Every other route keeps its heartbeat unchanged.

Affected packages:
- transports/bifrost-http/lib
- transports/bifrost-http/integrations
