## ✨ Features

- **File Scheme Pricing URLs** — Pricing source URLs now accept the `file://` scheme, allowing custom pricing data to be loaded from the local filesystem for air-gapped and self-hosted deployments (#4045)
- **Paginated Virtual Keys** — Virtual key fetching is now paginated to handle deployments with very large numbers of keys without loading them all at once (#3957)

## 🐞 Fixed

- **Bedrock Output Assessments** — Corrected the type of `outputAssessments` in Bedrock responses (#4028)
- **Text Completion Chunk Model** — Added the missing `Model` field to `TextCompletionChunkResponse` (#3970) (thanks [@kuishou68](https://github.com/kuishou68)!)
- **Orphaned Tool Results** — Orphaned tool results in the OpenAI to Anthropic conversion flow are no longer rejected by the Anthropic API (#3919)
- **MCP Inline stdio Env** — MCP stdio server configs now accept inline environment variable assignments (#3861) (thanks [@Shushmitaaaa](https://github.com/Shushmitaaaa)!)
- **Model Pool Pricing Reloads** — Non-pricing model pool entries are preserved across pricing reloads instead of being dropped (#3999)
