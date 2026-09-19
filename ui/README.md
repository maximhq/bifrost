# Bifrost UI

A modern, production-ready web interface for the [Bifrost AI Gateway](https://github.com/maximhq/bifrost) - providing real-time monitoring, configuration management, and comprehensive observability for your AI infrastructure.

## Overview

Bifrost UI is a React + Vite + TanStack Router web dashboard that serves as the control center for your Bifrost AI Gateway. It provides an intuitive interface to monitor AI requests, configure providers, manage MCP clients, and analyze performance metrics.

### Key Features

- **Real-time Log Monitoring** - Live streaming dashboard with WebSocket integration
- **Provider Management** - Configure [15+ AI providers](https://docs.getbifrost.ai/quickstart/gateway/provider-configuration)
- **MCP Integration** - Manage [Model Context Protocol](https://docs.getbifrost.ai/features/mcp) clients for advanced AI capabilities
- **Plugin System** - Extend functionality with [custom plugins](https://docs.getbifrost.ai/plugins/getting-started)
- **Analytics Dashboard** - Request metrics, success rates, latency tracking, and token usage
- **Modern UI** - Dark/light mode, responsive design, and accessible components
- **Documentation Hub** - Built-in documentation browser and quick-start guides

## Quick Start

### Prerequisites

The UI is designed to work with the Bifrost HTTP transport backend. Get started with the complete setup:

**[Gateway Setup Guide →](https://docs.getbifrost.ai/quickstart/gateway/setting-up)**

### Development

```bash
# Install dependencies
npm install

# Start development server
npm run dev
```

The development server runs on `http://localhost:3000` and connects to your Bifrost HTTP transport backend (default: `http://localhost:8080`).

### Environment Variables

```bash
# Development only - customize Bifrost backend port
BIFROST_PORT=8080
```

## Architecture

### Technology Stack

- **Framework**: React 19 + Vite + TanStack Router
- **Language**: TypeScript
- **Styling**: Tailwind CSS + Radix UI components
- **State Management**: Redux Toolkit with RTK Query
- **Real-time**: WebSocket integration
- **HTTP Client**: Axios with typed service layer
- **Theme**: Dark/light mode support
- **i18n**: i18next + react-i18next (English default, plus zh-CN)

### Internationalization (i18n)

UI copy uses `i18next`. Source/fallback locale is **en**, with **zh-CN** also supported. The selection is persisted in `localStorage` under `bifrost.locale`.

#### Layout

| Path | Purpose |
| --- | --- |
| `lib/i18n/index.ts` | Init; exports `i18n` / `SUPPORTED_LOCALES` / `changeLocale` / `getLocale` |
| `lib/i18n/resources.ts` | Aggregates namespace JSON |
| `locales/en/*.json` | English (source) |
| `locales/zh-CN/*.json` | Simplified Chinese |

Namespaces include `common`, `shell`, `login`, `observability`, `models`, `mcp`, `governance`, and `config`.

#### Adding copy

1. Add the same key to `locales/en/<ns>.json` and `locales/zh-CN/<ns>.json`.
2. For a new namespace: create both JSON files and register it in `lib/i18n/resources.ts`.
3. In components:

```tsx
import { useTranslation } from "react-i18next";

const { t } = useTranslation("common");
return <button>{t("save")}</button>;

// Other namespaces
const { t: tShell } = useTranslation("shell");
```

Default namespace is `common`. `I18nextProvider` is mounted in `app/clientLayout.tsx`, so any Router child can use `useTranslation`.

#### Switching language

- UI: topbar `LanguageSwitcher` (English / 简体中文)
- Programmatically: `import { changeLocale, getLocale } from "@/lib/i18n"`, then `await changeLocale("zh-CN")`

### Integration Model

```
┌─────────────────┐    HTTP/WebSocket    ┌──────────────────┐
│   Bifrost UI    │ ◄─────────────────► │ Bifrost HTTP     │
│   (React+Vite)  │                     │ Transport (Go)   │
└─────────────────┘                     └──────────────────┘
        │                                        │
        │ Build artifacts                        │
        └────────────────────────────────────────┘
```

- **Development**: UI runs on port 3000, connects to Go backend on port 8080
- **Production**: UI built as static assets served directly by Go HTTP transport
- **Communication**: REST API + WebSocket for real-time features

## Features

### Real-time Log Monitoring

The main dashboard provides comprehensive request monitoring with live updates via WebSocket, advanced filtering, and detailed request/response inspection.

**[Learn More →](https://docs.getbifrost.ai/features/observability)**

### Provider Configuration

Manage all your AI providers from a unified interface with support for multiple API keys, custom network configuration, and provider-specific settings.

**[View All Providers →](https://docs.getbifrost.ai/quickstart/gateway/provider-configuration)**

### MCP Client Management

Model Context Protocol integration for advanced AI capabilities including tool integration and connection monitoring.

**[MCP Documentation →](https://docs.getbifrost.ai/features/mcp)**

### Plugin Ecosystem

Extend Bifrost with powerful plugins for observability, testing, caching, and custom functionality.

**Available Plugins:**

- [Maxim Logger](https://docs.getbifrost.ai/features/observability/maxim) - Advanced LLM observability
- [Response Mocker](https://docs.getbifrost.ai/features/plugins/mocker) - Mock responses for testing
- [Semantic Cache](https://docs.getbifrost.ai/features/semantic-caching) - Intelligent response caching
- [OpenTelemetry](https://docs.getbifrost.ai/features/observability/otel) - Distributed tracing

**[Plugin Development Guide →](https://docs.getbifrost.ai/plugins/getting-started)**

## Development

### Project Structure

```
ui/
├── app/                    # TanStack Router pages
│   ├── page.tsx           # Main logs dashboard
│   ├── config/            # Provider & MCP configuration
│   ├── docs/              # Documentation browser
│   └── plugins/           # Plugin management
├── components/            # Reusable UI components
│   ├── logs/             # Log monitoring components
│   ├── config/           # Configuration forms
│   └── ui/               # Base UI components (Radix)
├── hooks/                # Custom React hooks
├── lib/                  # Utilities and services
│   ├── store/            # Redux store and API slices
│   ├── types/            # TypeScript definitions
│   └── utils/            # Helper functions
└── scripts/              # Build and deployment scripts
```

### API Integration

The UI uses Redux Toolkit + RTK Query for state management and API communication with the Bifrost HTTP transport backend:

```typescript
// Example API usage with RTK Query
import { useGetLogsQuery, useCreateProviderMutation, getErrorMessage } from "@/lib/store";

// Get real-time logs with automatic caching
const { data: logs, error, isLoading } = useGetLogsQuery({ filters, pagination });

// Configure provider with optimistic updates
const [createProvider] = useCreateProviderMutation();

const handleCreate = async () => {
	try {
		await createProvider({
			provider: "openai",
			keys: [{ value: "sk-...", models: ["gpt-4"], weight: 1 }],
			// ... other config
		}).unwrap();
		// Success handling
	} catch (error) {
		console.error(getErrorMessage(error));
	}
};
```

### Component Guidelines

- **Composition**: Use Radix UI primitives for accessibility
- **Styling**: Tailwind CSS with CSS variables for theming
- **Types**: Full TypeScript coverage matching Go backend schemas
- **Error Handling**: Consistent error states and user feedback

### Adding New Features

1. **Backend Integration**: Add API endpoints to RTK Query slices in `lib/store/`
2. **Type Definitions**: Update types in `lib/types/`
3. **UI Components**: Build with Radix UI and Tailwind
4. **State Management**: Use RTK Query for API state, React hooks for local state
5. **Real-time Updates**: Integrate WebSocket events when applicable

## Configuration

### Provider Setup

The UI supports comprehensive provider configuration including API keys with model assignments, network settings, and provider-specific options.

**[Complete Provider Configuration Guide →](https://docs.getbifrost.ai/quickstart/gateway/provider-configuration)**

### Governance & Access Control

Configure virtual keys, budget limits, rate limiting, and team-based access control through the UI.

**[Governance Documentation →](https://docs.getbifrost.ai/features/governance)**

### Real-time Features

WebSocket connection provides live log streaming, connection status monitoring, automatic reconnection, and filtered real-time updates.

**[Observability Features →](https://docs.getbifrost.ai/features/observability)**

## Monitoring & Analytics

The dashboard provides comprehensive observability including request metrics, token usage tracking, provider performance analysis, error categorization, and historical trend analysis.

**[Performance Benchmarks →](https://docs.getbifrost.ai/benchmarking/getting-started)**

## Contributing

We welcome contributions! See our [Contributing Guide](https://docs.getbifrost.ai/contributing/setting-up-repo) for:

- Code conventions and style guide
- Development setup and workflow
- Adding new providers or features
- Plugin development guidelines

## Documentation

**Complete Documentation:** [https://docs.getbifrost.ai](https://docs.getbifrost.ai)

### Quick Links

- [Gateway Setup](https://docs.getbifrost.ai/quickstart/gateway/setting-up) - Get started in 30 seconds
- [Provider Configuration](https://docs.getbifrost.ai/quickstart/gateway/provider-configuration) - Multi-provider setup
- [MCP Integration](https://docs.getbifrost.ai/features/mcp) - External tool calling
- [Plugin Development](https://docs.getbifrost.ai/plugins/getting-started) - Build custom plugins
- [Architecture](https://docs.getbifrost.ai/architecture) - System design and internals

## Need Help?

**[Join our Discord](https://discord.gg/exN5KAydbU)** for community support and discussions.

Get help with:

- Quick setup assistance and troubleshooting
- Best practices and configuration tips
- Community discussions and support
- Real-time help with integrations

## Links

- **Main Repository**: [github.com/maximhq/bifrost](https://github.com/maximhq/bifrost)
- **HTTP Transport**: [../transports/bifrost-http](../transports/bifrost-http)
- **Documentation**: [docs.getbifrost.ai](https://docs.getbifrost.ai)
- **Website**: [getbifrost.ai](https://www.getbifrost.ai)

## License

Licensed under the Apache 2.0 License - see the [LICENSE](../LICENSE) file for details.

---

Built with ❤️ by [Maxim](https://github.com/maximhq)