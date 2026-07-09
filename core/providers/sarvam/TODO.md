# Sarvam provider — deferred work

## Reasoning-token usage breakdown not reported

**Status:** not implemented, intentionally deferred pending a concrete need.

**What's missing:** `schemas.ChatCompletionTokensDetails.ReasoningTokens` (and `.ReasoningTokensCost`)
are always `0` for Sarvam responses. This is the same field the Anthropic
thinking-tokens usage fix (this repo, PR preceding this branch) populates
from `output_tokens_details.thinking_tokens`.

**Why:** Sarvam's `usage.completion_tokens_details` is always `null` in every
response observed (verified against both live responses and Sarvam's
published OpenAPI spec — `CompletionUsage.completion_tokens_details` is a
loosely-typed open object with no fixed schema, but Sarvam never populates
it). There is no other field carrying a reasoning-token count anywhere in
Sarvam's chat completion response — `ChatCompletionResponseMessage` pairs
`reasoning_content` (the raw text) with no accompanying count.

**What is correct today:** `usage.completion_tokens` / `usage.total_tokens`
already include reasoning-token consumption in the total (confirmed live: a
request that spent ~999 of 1000 completion tokens on reasoning reported
`completion_tokens: 1001`, matching the real total). Budget enforcement,
quota tracking, and cost totals that operate on the aggregate token count are
unaffected. What's missing is purely the *category breakdown* (reasoning vs.
output) for reporting/attribution — not the total itself.

**Considered and rejected (for now):** client-side estimation, i.e.
tokenizing `reasoning_content` ourselves to synthesize a `ReasoningTokens`
value Sarvam never sent. Rejected because:
- It would be an estimate against an unknown tokenizer (Sarvam doesn't
  publish one), so the number could be meaningfully wrong — arguably worse
  for a billing/governance field than reporting zero.
- No other provider without native `reasoning_tokens` gets this estimation
  treatment in Bifrost today; adding it only for Sarvam would be a one-off
  inconsistency, not a documented pattern.
- Out of the original scope (chat/TTS/STT wire compatibility) — this
  surfaced from a governance question during review, not a filed request.
- Adds a new tokenizer dependency for a single provider's cosmetic field.

**When to revisit:** if there's a concrete downstream need for
per-category (thinking vs. output) cost attribution/reporting specifically
for Sarvam usage, and an approximate number is acceptable. If so:
1. Pick a tokenizer approximation (e.g. reuse whatever the repo already
   vendors for other estimation needs, if any).
2. Populate `ChatCompletionTokensDetails.ReasoningTokens` from a token count
   of `message.reasoning_content` in `core/providers/sarvam/sarvam.go`'s
   chat path (would need a thin wrapper around
   `openai.HandleOpenAIChatCompletionRequest`'s response, since that call is
   currently a straight passthrough with no Sarvam-specific post-processing).
3. Clearly label the value as estimated (not authoritative) wherever it
   surfaces, so it isn't confused with a real provider-reported count.
