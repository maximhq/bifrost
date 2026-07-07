# Bifrost Model Parameters

A shared list of AI models with their prices, limits, features, **and the request parameters you can tune for each one** (temperature, max tokens, top-p, and so on). `data.json` holds one entry per model — the key is the model name and the value describes it.

## Purpose

This file is the single source of truth for what knobs Bifrost shows for each model. It goes live at **[getbifrost.ai/datasheet](https://www.getbifrost.ai/datasheet)** and is seeded to all consumers, so anything you add or fix here reaches everyone on the next sync from the `dev` branch. It builds on the [datasheet](../datasheet) by adding a `model_parameters` list that drives the tunable controls in the Bifrost UI — get it right and users see the correct options with sensible defaults and ranges.

## Example entry

```json
"gpt-4o": {
  "provider": "openai",
  "base_model": "gpt-4o",
  "mode": "chat",
  "max_input_tokens": 128000,
  "max_output_tokens": 16384,
  "max_tokens": 16384,
  "input_cost_per_token": 2.5e-06,
  "output_cost_per_token": 1e-05,
  "cache_read_input_token_cost": 1.25e-06,
  "supports_function_calling": true,
  "supports_vision": true,
  "supports_response_schema": true,
  "supports_tool_choice": true,
  "model_parameters": [
    {
      "id": "temperature",
      "label": "Temperature",
      "helpText": "What sampling temperature to use, between 0 and 2. Higher values make output more random; lower values make it more focused.",
      "type": "number",
      "default": 1,
      "range": { "min": 0, "max": 2, "step": 0.01 }
    },
    {
      "id": "stream",
      "label": "Stream",
      "helpText": "Whether the response is streamed incrementally or returned all at once.",
      "type": "boolean",
      "default": false
    },
    {
      "id": "stop",
      "label": "Stop",
      "helpText": "Custom text sequences that will cause the model to stop generating.",
      "type": "array",
      "array": { "type": "text", "minElements": 1, "maxElements": 4 }
    }
  ]
}
```

## Structure

The top level is one big object. Each key is the model name exactly as the provider calls it (e.g. `gpt-4o`, `claude-sonnet-4-5`, `ai21.j2-mid-v1`). Each value has the same pricing/limits/capability fields as the [datasheet](../datasheet) — see that README for those — **plus** a `model_parameters` list.

Prices are in **US dollars per unit**; small numbers use scientific notation (`2.5e-06` = `$0.0000025`). Only add a `supports_*` field when it's `true`.

### Model info fields

| Field | Required | Meaning |
|-------|:--------:|---------|
| `provider` | ☑ | Who serves the model — `openai`, `anthropic`, `bedrock`, `vertex_ai`, etc. |
| `base_model` | ☑ | The model family name, without any prefixes. |
| `mode` | ☑ | What the model does: `chat`, `completion`, `embedding`, `image_generation`, `audio_transcription`, etc. |
| `max_input_tokens` / `max_output_tokens` / `max_tokens` | ☐ | Context and output limits. |
| `input_cost_per_token` / `output_cost_per_token` | ☐ | Base per-token prices. |
| `cache_read_input_token_cost` / `cache_creation_input_token_cost` | ☐ | Prompt-cache prices. |
| `*_batches` / `*_priority` | ☐ | Price variants for batch and priority tiers. |
| `supports_*` | ☐ | Capability flags (`supports_vision`, `supports_function_calling`, …), added only when `true`. |
| `source` | ☐ | Link to the provider's official pricing/docs page. |

> For the full list of pricing and capability fields, see the [datasheet README](../datasheet/README.md). This file uses the same ones.

### `model_parameters` list

Each item describes one tunable request parameter shown in the UI. Fields on a parameter object:

| Field | Required | Meaning |
|-------|:--------:|---------|
| `id` | ☑ | The parameter name sent to the provider (e.g. `temperature`, `max_tokens`, `stop`). |
| `label` | ☑ | Human-friendly name shown in the UI. |
| `type` | ☑ | Control type — `number`, `boolean`, `select`, `text`, or `array`. |
| `helpText` | ☐ | Short explanation shown next to the control. |
| `default` | ☐ | Default value used when the user doesn't set one. |
| `range` | ☐ | For `number` types — `{ "min", "max", "step" }`. |
| `options` | ☐ | For `select` types — list of `{ "label", "value" }` choices (a choice may add `subFields` for nested inputs). |
| `array` | ☐ | For `array` types — element spec `{ "type", "minElements", "maxElements" }`. |
| `accesorKey` | ☐ | Key to read the value from when the control produces an object. |
| `disabled` / `disabledText` | ☐ | Hide/disable a control and explain why. |
| `required` / `hidden` / `multiple` | ☐ | Extra UI hints when needed. |

Common parameter `id`s across chat models: `stream`, `temperature`, `max_tokens`, `top_p`, `stop`, `n`, `frequency_penalty`, `presence_penalty`, `seed`, `logit_bias`, `logprobs`, `promptTools`, `reasoning_effort`, `response_format`.

When adding parameters to a new model, copy the shape from an existing model with the same `provider` and `mode` so labels, help text, defaults, and ranges stay consistent.

## General guidelines

- **Cite your source.** Add a `source` link for any pricing you add or change.
- **Match neighbors.** Reuse the exact field names, parameter `id`s, and units from similar entries — don't invent new names.
- **Keep defaults and ranges sane.** A `default` must fall inside its `range`; `max_tokens` ranges should respect the model's real `max_output_tokens`.
- **Don't duplicate.** Keys must be unique; check the model isn't already there before adding it.
- **Don't delete.** For retired models, set `is_deprecated: true` / `deprecation_date` instead of removing them — consumers may still reference them.
- **Keep diffs small.** Edit only the entries you're changing; don't reformat the whole file.
- **Keep it valid JSON.** Quick check: `python3 -m json.tool data.json > /dev/null`.

## Questions?

Not sure about a field, a parameter, a provider name, or how to price something? Open an issue on the Bifrost repo and we'll help.
