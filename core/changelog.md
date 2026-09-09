[fix]: preserve documents in Bedrock tool results [@michaeldunn9](https://github.com/michaeldunn9)
- feat: Claude Cowork proxy support: `claude-cowork` user agents resolve to the Claude Cowork app, and Anthropic text documents sent as base64 data URLs (`text/*`, JSON) are decoded into `text` document sources on the chat and Responses paths (#7012)
- fix: `CheckFirstStreamChunkForError` observes the request context while waiting for the first chunk, returning 499 on cancel and 504 on deadline instead of blocking the worker until the stream idle timeout (#6993)
- feat: add an opt-in stream throughput guard that retries or falls back before slow provider output becomes consumer-visible
