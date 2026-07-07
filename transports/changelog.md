> This prerelease is based on [v1.6.3](https://docs.getbifrost.ai/changelogs/v1.6.3) - see that changelog for the full baseline.

## ✨ Features

- **ChatGPT Passthrough** - Added a ChatGPT passthrough route on the OpenAI integration with dedicated request handling
- **User-Agent Tracking** - Track user agents on LLM and MCP logs, with custom user-agent mapping and dashboard dimension rankings
- **Edge Agent MCP Log Ingestion** - MCP tool logs observed by the Bifrost Edge agent can now be ingested with device, app key, decision, and source attribution
- **Edge Fallback Pages** - Added fallback pages for Bifrost Edge control views (config, devices, inventory) backed by governance resolver support
- **Agent Handover View** - Added an agent handover page with seeded end-to-end data support

## 🐞 Fixed

- **API Auth Bypass** - Stopped `/api/devices` bypassing auth via the `/api/dev` prefix
- **Bedrock Error Types** - Surface the AWS exception type (`X-Amzn-Errortype`) on non-streaming Bedrock error responses instead of dropping it
