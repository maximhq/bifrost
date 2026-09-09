[fix]: preserve documents in Bedrock tool results [@michaeldunn9](https://github.com/michaeldunn9)
- feat: Claude Cowork proxy support: `claude-cowork` user agents resolve to the Claude Cowork app, and Anthropic text documents sent as base64 data URLs (`text/*`, JSON) are decoded into `text` document sources on the chat and Responses paths (#7012)
