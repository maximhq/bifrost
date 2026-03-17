## ✨ Features

- **Plugin Sequencing** — Added plugin execution ordering with placement and priority controls for custom plugins relative to built-in plugins
- **Groq Speech** — Added speech synthesis (TTS) and transcription (STT) support for Groq provider
- **Gemini Model Metadata** — Added support for Gemini metadata endpoint (/v1beta/models/{model}) (thanks [@Vaibhav701161](https://github.com/Vaibhav701161)!)
- **Wildcard Header Forwarding** — Added wildcard pattern support in header forwarding configuration
- **Log Metadata Columns** — Added metadata columns in logs and filters for richer observability
- **Prompt Caching Improvements** — Preserved JSON key ordering for LLM prompt caching using byte-level operations
- **Azure GCC Cloud Support** — Added Azure cloud environment configuration for GCC High and DoD environments
- **Connection Pool Management** — Added connection lifetime limits and optimized pool behavior to prevent stale connections

## 🐞 Fixed

- **MCP Tool Headers** — Fixed MCP tools not passing required headers to the MCP server
- **MCP Tool Call Detection** — Fixed tool calls not being detected in MCP agent mode when providers return "stop" finish reason
- **Gemini Finish Reason** — Fixed Gemini models not returning correct "tool_calls" finish reason
- **Prompt Cascade Deletion** — Fixed manual cascade deletion for prompt entities

## 🔒 Security

- **Container Base Image** — Upgraded Node and Alpine base images to include latest security patches
