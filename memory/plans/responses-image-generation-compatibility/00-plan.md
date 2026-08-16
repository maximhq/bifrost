# Responses image-generation compatibility

## Objective

Make Bifrost's `/v1/responses` schema accept OpenAI-compatible hosted image-generation tools in unary and streaming responses. The regression is a shared JSON-union failure: `action: "generate"` is currently decoded as an object-only action and fails before the response reaches either transport path.

The implementation will use named compile-time action values (`generate`, `edit`, and `auto`) instead of untyped string literals. It will preserve provider extensions and avoid changing the canonical `ResponsesImageGenerationCall` shape unless fixtures prove that an output field is otherwise lost.

## Evidence and constraints

- The local Codex provider reproduced the failure after Bifrost received image-generation stream events; the parser reported a mismatch while decoding scalar `"generate"`.
- The same custom Responses schema is used by the unary and streaming decoders, so the fix must be shared rather than duplicated in handlers.
- OpenAI's Responses reference documents request-side image-generation `action` values `generate`, `edit`, and `auto`, and response-side image-generation call/status/event types.
- Tests must be deterministic and must not commit credentials or depend on a live image result. A runtime gate may use the configured Codex provider with `gpt-5.6-luna` (or another available non-Sol model), never `gpt-5.6-sol`.

## Design

1. Add a named `ResponsesImageGenerationAction` type and constants for the three request actions.
2. Add an action field to `ResponsesToolImageGeneration`, with JSON round-trip coverage.
3. Extend the action union to decode both scalar actions and object actions. Scalar known values map to a typed image-generation action; unknown scalar/object values remain raw or safely ignored and must not silently become computer actions.
4. Keep existing computer, web-search, local-shell, and MCP action dispatch intact.
5. Exercise the shared decoder through focused schema tests, a provider/integration-level unary fixture, and streaming event fixtures for `in_progress`, `generating`, `partial_image`, and `completed`.
6. Add/extend an LLM scenario for image-generation Responses requests with both stream modes. The live runtime matrix uses `gpt-5.6-luna` or another non-Sol model when available.

## Test matrix

| Layer | Case | Expected result |
| --- | --- | --- |
| schema | request action `generate`, `edit`, `auto` | typed value survives decode and marshal |
| schema | scalar output action `"generate"` | no unmarshal error; typed action selected |
| schema | object output action `{ "type": "generate" }` | no unmarshal error; typed action selected |
| schema | unknown scalar/object action | no error and no computer-action coercion |
| schema | unrelated computer/web/local-shell/MCP action | existing union member selected |
| integration | unary completed image call with `result` | full Responses object decodes |
| integration | stream image events | all four image event variants decode |
| live runtime | `gpt-5.6-luna`, non-stream | Bifrost returns a completed image-generation call or a provider capability error, never schema mismatch |
| live runtime | `gpt-5.6-luna`, stream | stream reaches completion or a provider capability error, never schema mismatch |

## Verification

Run focused schema tests first, then package-level integration tests, then the relevant core/framework/transport test suites. Perform a final self-review of schema, streaming, integration, and test changes. The runtime gate is best-effort and records provider capability/network results separately from deterministic test results.
