# Bifrost UI

A modern, production-ready dashboard for the [Bifrost AI Gateway](https://github.com/maximhq/bifrost) - providing real-time monitoring, configuration management, and comprehensive observability for your AI infrastructure.

## 🌟 Overview

Bifrost UI is a Next.js-powered web dashboard that serves as the control center for your Bifrost AI Gateway. It provides an intuitive interface to monitor AI requests, configure providers, manage MCP clients, and extend functionality through plugins.

### Key Features

- **🔴 Real-time Log Monitoring** - Live streaming dashboard with WebSocket integration
- **⚙️ Provider Management** - Configure 8+ AI providers (OpenAI, Azure, Anthropic, Bedrock, etc.)
- **🔌 MCP Integration** - Manage Model Context Protocol clients for advanced AI capabilities
- **🧩 Plugin System** - Extend functionality with observability, testing, and custom plugins
- **📊 Analytics Dashboard** - Request metrics, success rates, latency tracking, and token usage
- **🎨 Modern UI** - Dark/light mode, responsive design, and accessible components
- **📚 Documentation Hub** - Built-in documentation browser and quick-start guides

## 🚀 Quick Start

### Development

```bash
# Install dependencies
npm install

# Start development server
npm run dev
```

The development server runs on `http://localhost:3000` and connects to your Bifrost HTTP transport backend (default: `http://localhost:8080`).

### Production Build

```bash
# Build and prepare for integration with Bifrost HTTP transport
npm run build
```

This creates a static export in the `out/` directory and automatically copies it to `../transports/bifrost-http/ui` for embedding in the Go binary.

### Environment Variables

```bash
# Development only - customize Bifrost backend port
NEXT_PUBLIC_BIFROST_PORT=8080
```

## 🏗️ Architecture

### Technology Stack

- **Framework**: Next.js 15 with App Router
- **Language**: TypeScript
- **Styling**: Tailwind CSS + Radix UI components
- **State Management**: React hooks and context
- **Real-time**: WebSocket integration
- **HTTP Client**: Axios with typed service layer
- **Theme**: Dark/light mode support

### Integration Model

```
┌─────────────────┐    HTTP/WebSocket    ┌──────────────────┐
│   Bifrost UI    │ ◄─────────────────► │ Bifrost HTTP     │
│   (Next.js)     │                     │ Transport (Go)   │
└─────────────────┘                     └──────────────────┘
        │                                        │
        │ Build artifacts                        │
        └────────────────────────────────────────┘
```

- **Development**: UI runs on port 3000, connects to Go backend on port 8080
- **Production**: UI built as static assets served directly by Go HTTP transport
- **Communication**: REST API + WebSocket for real-time features

## 📱 Features Deep Dive

### Real-time Log Monitoring

The main dashboard provides comprehensive request monitoring:

- **Live Updates**: WebSocket connection for real-time log streaming
- **Advanced Filtering**: Filter by providers, models, status, content, and time ranges
- **Request Analytics**: Success rates, average latency, total tokens usage
- **Detailed Views**: Full request/response inspection with syntax highlighting
- **Search**: Full-text search across request content and metadata

### Provider Configuration

Manage all your AI providers from a unified interface:

- **Supported Providers**: OpenAI, Azure OpenAI, Anthropic, AWS Bedrock, Cohere, Google Vertex AI, Mistral, Ollama, Groq, SGLang
- **Key Management**: Multiple API keys with weights and model assignments
- **Network Configuration**: Custom base URLs, timeouts, retry policies, proxy settings
- **Provider-specific Settings**: Azure deployments, Bedrock regions, Vertex projects
- **Concurrency Control**: Per-provider concurrency limits and buffer sizes

### MCP Client Management

Model Context Protocol integration for advanced AI capabilities:

- **Client Configuration**: Add, update, and delete MCP clients
- **Connection Monitoring**: Real-time status and health checks
- **Reconnection**: Manual and automatic reconnection capabilities
- **Tool Integration**: Seamless integration with MCP tools and resources

### Plugin Ecosystem

Extend Bifrost with powerful plugins:

- **Maxim Logger**: Advanced LLM observability and analytics
- **Response Mocker**: Mock responses for testing and development
- **Circuit Breaker**: Resilience patterns and failure handling
- **Custom Plugins**: Build your own with the plugin development guide

## 🛠️ Development

### Project Structure

```
ui/
├── app/                    # Next.js App Router pages
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
│   ├── api.ts            # Backend API service
│   ├── types/            # TypeScript definitions
│   └── utils/            # Helper functions
└── scripts/              # Build and deployment scripts
```

### API Integration

The UI communicates with the Bifrost HTTP transport backend through a typed API service:

```typescript
// Example API usage
import { apiService } from '@/lib/api'

// Get real-time logs
const [logs, error] = await apiService.getLogs(filters, pagination)

// Configure provider
const [result, error] = await apiService.createProvider({
  provider: 'openai',
  keys: [{ value: 'sk-...', models: ['gpt-4'], weight: 1 }],
  // ... other config
})
```

### Component Guidelines

- **Composition**: Use Radix UI primitives for accessibility
- **Styling**: Tailwind CSS with CSS variables for theming
- **Types**: Full TypeScript coverage matching Go backend schemas
- **Error Handling**: Consistent error states and user feedback

### Adding New Features

1. **Backend Integration**: Add API endpoints to `lib/api.ts`
2. **Type Definitions**: Update types in `lib/types/`
3. **UI Components**: Build with Radix UI and Tailwind
4. **State Management**: Use React hooks or context as needed
5. **Real-time Updates**: Integrate WebSocket events when applicable

## 🔧 Configuration

### Provider Setup

The UI supports comprehensive provider configuration:

```typescript
interface ProviderConfig {
  keys: Key[] // API keys with model assignments
  network_config: NetworkConfig // URLs, timeouts, retries
  meta_config?: MetaConfig // Provider-specific settings
  concurrency_and_buffer_size: {
    // Performance tuning
    concurrency: number
    buffer_size: number
  }
  proxy_config?: ProxyConfig // Proxy settings
}
```

### Real-time Features

WebSocket connection provides:

- Live log streaming
- Connection status monitoring
- Automatic reconnection
- Filtered real-time updates

## 📊 Monitoring & Analytics

The dashboard provides comprehensive observability:

- **Request Metrics**: Total requests, success rate, average latency
- **Token Usage**: Input/output tokens, total consumption tracking
- **Provider Performance**: Per-provider success rates and latencies
- **Error Analysis**: Detailed error categorization and troubleshooting
- **Historical Data**: Time-based filtering and trend analysis

## 🤝 Contributing

We welcome contributions! See our [Contributing Guide](https://github.com/maximhq/bifrost/tree/main/docs/contributing) for:

- Code conventions and style guide
- Development setup and workflow
- Adding new providers or features
- Plugin development guidelines

## 📚 Documentation

- **Quick Start**: [Get started in 30 seconds](https://github.com/maximhq/bifrost/tree/main/docs/quickstart)
- **Configuration**: [Complete setup guide](https://github.com/maximhq/bifrost/tree/main/docs/usage/http-transport/configuration)
- **API Reference**: [HTTP transport endpoints](https://github.com/maximhq/bifrost/tree/main/docs/usage/http-transport)
- **Architecture**: [Design and performance](https://github.com/maximhq/bifrost/tree/main/docs/architecture)

## 🔗 Links

- **Main Repository**: [github.com/maximhq/bifrost](https://github.com/maximhq/bifrost)
- **HTTP Transport**: [../transports/bifrost-http](../transports/bifrost-http)
- **Documentation**: [docs/](../docs/)
- **Website**: [getmaxim.ai](https://getmaxim.ai)

## 📄 License

Licensed under the same terms as the main Bifrost project. See [LICENSE](../LICENSE) for details.

---

_Built with ♥️ by [Maxim AI](https://getmaxim.ai)_
