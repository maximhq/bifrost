# Bifrost Model Datasheet

A shared list of AI models with their prices, limits, and features. `data.json` holds one entry per model — the key is the model name and the value describes it.

## Purpose

This datasheet is the single source of truth for model info in Bifrost. It goes live at **[getbifrost.ai/datasheet](https://www.getbifrost.ai/datasheet)** and is seeded to all consumers, so anything you add or fix here reaches everyone on the next sync from the `dev` branch. Keeping it accurate means correct pricing, limits, and capabilities everywhere Bifrost is used.

## Example entry

```json
"claude-sonnet-4-5": {
  "provider": "anthropic",
  "base_model": "claude-sonnet-4-5",
  "mode": "chat",
  "max_input_tokens": 200000,
  "max_output_tokens": 64000,
  "max_tokens": 64000,
  "input_cost_per_token": 0.000003,
  "output_cost_per_token": 0.000015,
  "input_cost_per_token_above_200k_tokens": 0.000006,
  "output_cost_per_token_above_200k_tokens": 0.0000225,
  "cache_read_input_token_cost": 3e-7,
  "cache_creation_input_token_cost": 0.00000375,
  "cache_creation_input_token_cost_above_1hr": 0.000006,
  "supports_function_calling": true,
  "supports_vision": true,
  "supports_reasoning": true,
  "supports_prompt_caching": true,
  "supports_pdf_input": true,
  "supports_tool_choice": true,
  "supports_response_schema": true,
  "supports_computer_use": true,
  "supports_assistant_prefill": true
}
```

## Structure

The top level is one big object. Each key is the model name exactly as the provider calls it (e.g. `claude-sonnet-4-5`, `writer.palmyra-x5-v1:0`, `aiml/dall-e-3`). Each value uses the fields below.

Prices are in **US dollars per unit** (per token, per image, per second). Small numbers use scientific notation — `3e-7` means `$0.0000003`. Only add a `supports_*` field when it's `true`; leave it out otherwise.

### Core

| Field | Required | Meaning |
|-------|:--------:|---------|
| `provider` | ☑ | Who serves the model — `anthropic`, `openai`, `bedrock`, `vertex_ai`, `openrouter`, etc. |
| `base_model` | ☑ | The model family name, without any prefixes. |
| `mode` | ☑ | What the model does: `chat`, `completion`, `embedding`, `rerank`, `image_generation`, `image_edit`, `audio_transcription`, `audio_speech`, `video_generation`, `responses`, `ocr`, `search`, `moderation`, `realtime`. |
| `source` | ☐ | Link to the provider's official pricing/docs page you got the numbers from. |

### Limits

| Field | Required | Meaning |
|-------|:--------:|---------|
| `max_input_tokens` | ☐ | Largest prompt the model accepts. |
| `max_output_tokens` | ☐ | Most tokens the model can return. |
| `max_tokens` | ☐ | Overall token cap (often same as max output). |

### Pricing

| Field | Required | Meaning |
|-------|:--------:|---------|
| `input_cost_per_token` | ☐ | Cost per input (prompt) token. |
| `output_cost_per_token` | ☐ | Cost per output (completion) token. |
| `cache_read_input_token_cost` | ☐ | Cost per token read from prompt cache. |
| `cache_creation_input_token_cost` | ☐ | Cost per token to write to prompt cache. |
| `cache_creation_input_token_cost_above_1hr` | ☐ | Cache-write cost for the longer (1hr+) cache tier. |
| `*_above_200k_tokens` / `*_above_128k_tokens` | ☐ | Long-context price tiers — same cost fields with a higher rate once the context passes that size. |
| `input_cost_per_image` / `output_cost_per_image` | ☐ | Per-image cost, for image models. |
| `output_cost_per_second` | ☐ | Per-second cost, for audio/video models. |
| `input_cost_per_audio_token` / `output_cost_per_audio_token` | ☐ | Per-token cost for audio. |
| `search_context_cost_per_query` | ☐ | Cost per web-search query, broken down by context size (low/medium/high). |

### Capabilities (add only when `true`)

| Field | Required | Meaning |
|-------|:--------:|---------|
| `supports_function_calling` | ☐ | Can call tools/functions. |
| `supports_tool_choice` | ☐ | Caller can force which tool is used. |
| `supports_vision` | ☐ | Accepts image input. |
| `supports_pdf_input` | ☐ | Accepts PDF input. |
| `supports_audio_input` / `supports_audio_output` | ☐ | Accepts / produces audio. |
| `supports_video_input` | ☐ | Accepts video input. |
| `supports_reasoning` | ☐ | Has a thinking/reasoning mode. |
| `supports_prompt_caching` | ☐ | Supports prompt caching. |
| `supports_response_schema` | ☐ | Can return structured JSON to a schema. |
| `supports_web_search` | ☐ | Can search the web. |
| `supports_computer_use` | ☐ | Supports computer/agent use. |
| `supports_assistant_prefill` | ☐ | Lets you prefill the assistant's reply. |
| `supports_system_messages` | ☐ | Accepts a system prompt. |

Some models use extra fields for their type (e.g. `output_vector_size` for embeddings, `deprecation_date`/`is_deprecated` for retired models, `metadata.notes` for caveats). When in doubt, copy the shape of an existing entry with the same `provider` and `mode`.

## General guidelines

- **Cite your source.** Add a `source` link for any pricing you add or change.
- **Match neighbors.** Reuse the exact field names and units from similar entries — don't invent new names.
- **Don't duplicate.** Keys must be unique; check the model isn't already there before adding it.
- **Don't delete.** For retired models, set `is_deprecated: true` / `deprecation_date` instead of removing them — consumers may still reference them.
- **Keep diffs small.** Edit only the entries you're changing; don't reformat the whole file.
- **Keep it valid JSON.** Quick check: `python3 -m json.tool data.json > /dev/null`.

## Questions?

Not sure about a field, a provider name, or how to price something? Open an issue on the Bifrost repo and we'll help.
