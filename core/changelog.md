- fix: Gemini video reference fields map to instances [@vojthor](https://github.com/vojthor)
- fix: accept object-valued tool-call arguments (e.g. tool_search_call) on the Responses API streaming path
- fix: recover from idle-timeout timer-goroutine panic that could crash the process
- fix: deterministic MCP tool ordering for prompt cache stability (closes #2347)
- fix: pass through `gs://` image URLs on Vertex Gemini (closes #4402)

