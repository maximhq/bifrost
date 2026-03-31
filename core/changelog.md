[fix]: bedrock streaming - retry stale/closed connections by classifying transport errors as IsBifrostError:false [@KTS-o7](https://github.com/KTS-o7)
- fix: fixed timeout status code handling across all providers
- fix: preserve cached provider metadata on cross-provider cache hits
- fix: prevent reasoning text from leaking into Gemini response content
- feat: added Anthropic beta headers support
