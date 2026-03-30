Bifrost

## Context

Bifrost is a high-performance AI gateway written in Go. One of its features is it routes requests to LLM providers (OpenAI, Anthropic, Bedrock, etc.) and already has a routing rules system using **CEL (Common Expression Language)** that evaluates runtime expressions against request context variables like `budget_used`, `team_name`, `headers["x-tier"]` etc.
