- fix: route parallel tool call argument deltas by id/index to prevent argument merging during streaming
- feat: add count tokens support for bedrock
- feat: add async rerank support
- fix: count tokens route fixed to match openai schema
  <Warning>
    **Breaking change.** The count tokens route has moved from `/v1/count_tokens` to `/v1/responses/input_tokens`, and the request body field has been renamed from incorrect `messages` to `input`. Please update your clients accordingly.
  </Warning>
- fix: added missing routing logic for bedrock integration
- fix: nil properties in tool function parameters handled