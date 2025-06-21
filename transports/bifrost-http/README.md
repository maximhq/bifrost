# Bifrost HTTP Transport

A high-performance HTTP transport for the Bifrost AI provider gateway using FastHTTP.

## 🛠️ Development

### Quick Start

**Note:** The main Makefile is located at the repository root. Run commands from the project root directory.

1. **Set up the development environment:**
   ```bash
   cd ../../  # Go to repository root
   make dev-full
   ```

2. **Start with hot reload:**
   ```bash
   make dev
   ```

3. **Start complete development environment (UI + API):**
   ```bash
   make dev-ui
   ```

### Available Commands

**From the repository root**, use `make help` to see all available commands. Key commands include:

| Command      | Description                                      |
|--------------|--------------------------------------------------|
| `dev`        | Start bifrost-http with hot reload using Air     |
| `dev-ui`     | Start complete development environment (UI + API) |
| `build`      | Build bifrost-http binary                        |
| `run`        | Build and run bifrost-http (no hot reload)      |
| `test-all`   | Run all tests (core, plugins, transports)       |
| `ui-build`   | Build UI for production (static export)        |
| `docker-build` | Build Docker image                             |
| `lint`       | Run Go linter                                   |

### Environment Variables

You can customize the development environment using these variables:

- `CONFIG_FILE`: Path to config file (default: `transports/config.example.json`)
- `PORT`: Server port (default: `8080`)
- `POOL_SIZE`: Connection pool size (default: `300`)
- `PLUGINS`: Comma-separated plugins to load (default: `maxim`)
- `PROMETHEUS_LABELS`: Labels for Prometheus metrics
- `BIFROST_UI_DEV`: Set to `true` to enable UI development proxy mode

**Example:**
```bash
make dev CONFIG_FILE=my-config.json PORT=3000 PLUGINS=maxim,other
```

### Hot Reload Development

The `make dev` command uses [Air](https://github.com/air-verse/air) to provide hot reloading. It automatically watches for changes in:

- `./` - Current bifrost-http directory
- `../../core/` - Core Bifrost functionality  
- `../../plugins/` - Plugin implementations

When files change, Air automatically rebuilds and restarts the server, providing a seamless development experience.

**Features:**
- 🔥 Hot reload on file changes
- 📁 Watches multiple directories
- 🚫 Excludes test files and build artifacts
- 🎨 Colored output for better visibility
- 📝 Build error logging

## 🎨 UI Integration

Bifrost HTTP includes a modern React-based web interface for configuration and monitoring.

### Development Modes

**1. Complete Development Environment (Recommended)**
```bash
# Starts both UI dev server and API server with proxy
make dev-ui
```
- Combined: `http://localhost:8080/ui` (proxies to UI dev server)
- API: `http://localhost:8080/v1/*`

**2. API-Only Development**
```bash
# Start API server with static UI files
make dev
```
- API: `http://localhost:8080`
- UI: `http://localhost:8080/ui` (static files)

### Production Deployment

```bash
# Build both API and UI
make prod-build

# Run with static UI files
make run
```

The UI will be served at `http://localhost:8080/ui` with static files from `ui/out/`.

### UI Features

- **Configuration Management**: JSON-based configuration editor with validation
- **Provider Setup**: Configure multiple AI providers (OpenAI, Anthropic, Azure, etc.)
- **Real-time Monitoring**: View server status and metrics
- **Responsive Design**: Mobile-first design using Shadcn UI components

## 🚀 Manual Usage

If you prefer to run without the Makefile:

### Prerequisites

- Go 1.23 or higher
- Air for hot reloading: `go install github.com/air-verse/air@latest`

### Running with Air

```bash
air -c .air.toml -- -config ../config.example.json -port 8080
```

### Building and Running

```bash
go build -o tmp/bifrost-http .
./tmp/bifrost-http -config ../config.example.json -port 8080
```

## 📡 API Endpoints

- `POST /v1/text/completions` - Text completion requests
- `POST /v1/chat/completions` - Chat completion requests  
- `POST /v1/mcp/tool/execute` - MCP tool execution
- `GET /metrics` - Prometheus metrics
- `GET /ui` - Web interface (serves React UI)
- `GET /ui/*` - UI static assets and routes

## 🔧 Configuration

See the parent directory's `config.example.json` for configuration examples. The HTTP transport supports:

- Multiple AI providers (OpenAI, Anthropic, Azure, etc.)
- Load balancing and failover
- MCP (Model Context Protocol) integration
- Prometheus monitoring
- Plugin system

## 🏗️ Architecture

The HTTP transport provides:

- **FastHTTP Server**: High-performance HTTP server
- **Provider Integrations**: Native API compatibility for major providers
- **Unified Interface**: OpenAI-compatible API surface
- **Hot Reload**: Development-friendly auto-restart
- **Monitoring**: Built-in Prometheus metrics
- **Plugin Support**: Extensible plugin architecture
- **Web Interface**: Modern React-based UI for configuration and monitoring 