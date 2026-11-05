# Coding-agent integration sources

Configuration contracts checked October 28, 2026. These references support the
CLI implementation; they do not change the gateway's API or documentation.

## Pi

Tested with `@earendil-works/pi-coding-agent` 1.4.0 (`pi`).

- Upstream: https://github.com/earendil-works/pi
- Models: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/models.md
- Settings: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/settings.md
- Authentication: https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/providers.md

Use `PI_CODING_AGENT_DIR` or `~/.pi/agent`. Register Bifrost's OpenAI-compatible
endpoint as `providers.bifrost` in `models.json`, set `defaultProvider` and
`defaultModel` in `settings.json`, and store the credential in `auth.json` under
`bifrost` with `type: api_key`. Saved auth overrides model-config credentials.
The model API is `openai-completions`; `authHeader: true` sends a bearer key.
The temporary launch directory retains access to native sessions and resources.

## Antigravity CLI

Tested with `agy` 1.4.8.

- Install/auth: https://antigravity.google/docs/cli/install/
- Settings: https://antigravity.google/docs/settings?tab=cli
- CLI reference: https://antigravity.google/docs/cli/reference/
- Settings schema: https://antigravity.google/schemas/v1/settings.json

`~/.gemini/antigravity-cli/settings.json` must select `modelProvider: gemini`.
The native CLI reads `GEMINI_API_KEY` and `GOOGLE_GEMINI_BASE_URL` from the process
environment. Persistent configure therefore combines native JSON settings with a
scoped Bash/Zsh function; temporary launch supplies environment variables directly.
Custom models use `customModelsConfig.customModels`, indexed by the selected model,
with `modelName`, `label`, and `toolFormatterType: TOOL_FORMATTER_TYPE_NONE`.

The published custom-model examples embed a base URL in `gemini-api://...`.
With 1.4.8 and `GOOGLE_GEMINI_BASE_URL`, our native fixture demonstrated that the
suffix is treated as the model ID: embedding the endpoint creates an incorrect
request path. We use `gemini-api://provider/model` with the endpoint solely in the
environment, verified against `/genai/v1beta/models/provider/model:streamGenerateContent`.
The same fixture observes background helper requests to native default models;
Bifrost does not override the CLI's helper-model policy.

## Regression checks

`cli/internal/harness/additionalagents_test.go` covers native file merging,
credential precedence, scoped shell quoting, alias resolution, temporary launch
cleanup, exclusive settings access, and preservation of native edits.
`cli/scripts/testconfigure.py` runs the built Bifrost executable for configure,
unconfigure, aliases and argument forwarding. Its opt-in native test executes both
installed CLIs against a local streaming HTTP fixture and checks model routing and
credentials without reaching a paid inference endpoint.

The native headless test also found that agy 1.4.8 does not reliably apply the
saved `model` without `--model`. Bifrost launch and the managed shell function
therefore pass the saved model explicitly; user-supplied arguments still follow.

## Project attribution contracts

Checked November 6, 2026:

- Claude Code custom headers: https://code.claude.com/docs/en/llm-gateway-connect#send-additional-headers
- Claude Desktop `inferenceCustomHeaders` and helper arguments: https://claude.com/docs/third-party/claude-desktop/configuration
- Desktop helper JSON and refresh lifecycle: https://claude.com/docs/third-party/claude-desktop/credential-helper
- Codex provider `http_headers`: https://developers.openai.com/codex/config-reference
- OpenCode provider `options.headers`: https://opencode.ai/docs/providers#custom-provider
- Pi provider headers: https://github.com/earendil-works/pi/tree/main/packages/coding-agent/docs
- Gemini custom-header parser: https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/utils/customHeaderUtils.ts

These are client configuration contracts, not proof of live Desktop acceptance.
Desktop helper output is cached and supplies no documented Claude Project identifier;
a static project selection must not be advertised as per-conversation switching.
