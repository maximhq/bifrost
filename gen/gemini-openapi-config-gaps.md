# Gemini OpenAPI config gaps — scope note

Source: `memory/gemlive/knowledge/gemini-openapi.yml` vs `core/providers/gemini/types.go`
(schema-sync findings, `gemini-findings-2026-07-07.md`).

## Fields wired (this branch)

All 5 live on structs already used by exposed Gemini provider endpoints
(`ChatCompletion`/`ChatCompletionStream` via `chat.go`, `Responses`/`ResponsesStream` via
`responses.go`, and the shared `GenerateContentResponse` envelope used by Speech/
Transcription/batch results too). No new endpoint or handler required — confirmed by
checking that `GenerationConfig`, `GoogleSearch`, and `GenerateContentResponse` are only
ever built/parsed inside the existing provider methods, never behind a separate code path.

- `GenerationConfig.ResponseFormat` (`*ResponseFormatConfig`)
- `GenerationConfig.TranslationConfig` (`*TranslationConfig`)
- `GenerationConfig.EnableEnhancedCivicAnswers` (`*bool`)
- `GenerateContentResponse.ModelStatus` (`*ModelStatus`)
- `GoogleSearch.SearchTypes` (`*SearchTypes`)

Also wired: `extra_params` extraction for `translation_config`, `enable_enhanced_civic_answers`,
`response_format` in both `chat.go` and `responses.go`, following the existing
`safety_settings`/`cached_content`/`labels` pattern (typed via a new generic
`safeExtractGeminiStruct[T]` JSON round-trip helper in `types.go`).

`response_json_schema` (schema-sync finding) was stale — `GenerationConfig.ResponseJSONSchema`
already existed; no action taken.

## Deliberately left as `extra_params`-only (no canonical/cross-provider mapping)

None of these 5 fields have an OpenAI-canonical equivalent to auto-populate from:

- `SearchTypes` — OpenAI's web-search tool (`ResponsesToolWebSearch`/`ChatWebSearchOptions`)
  has no image-vs-web search mode concept.
- `ResponseFormatConfig.Audio`/`.Image` — no OpenAI Responses/Chat concept for non-text
  output format. (`.Text` already has canonical coverage via the older `ResponseMIMEType`/
  `ResponseSchema` fields Bifrost already maps from `response_format`.)
- `TranslationConfig`, `EnableEnhancedCivicAnswers` — no OpenAI equivalent at all.
- `ModelStatus` — response-only, no canonical response field; surfaces via
  `ExtraFields.RawResponse` once captured.

Adding canonical cross-provider fields for these would require `core/schemas` changes —
out of scope here (Gemini-types-only wiring). Flagged as a possible future follow-up, not
actioned.
