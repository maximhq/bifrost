[fix]: omit role from OpenAI Responses non-message items [@nettee](https://github.com/nettee)
- fix: reset `HasEmittedWebSearch` when recycling pooled Gemini responses stream state so grounded streaming requests keep emitting `web_search_call` items (closes #5113) [@fus3r](https://github.com/fus3r)
