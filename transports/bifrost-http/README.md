# Bifrost HTTP Transport

A high-performance HTTP transport for the Bifrost AI provider gateway using FastHTTP.

## 🛠️ Development

### Quick Start

1. **Set up the development environment:**
   ```bash
   make dev-full
   ```

2. **Start with hot reload:**
   ```bash
   make dev
   ```

3. **Start the UI development server (in another terminal):**
   ```bash
   make ui-dev
   ```

### Available Commands

Use `make help` to see all available commands:

| Command      | Description                                      |
|--------------|--------------------------------------------------|
| `dev`        | Start bifrost-http with hot reload using Air     |
| `build`      | Build bifrost-http binary                        |
| `run`        | Build and run bifrost-http (no hot reload)      |
| `install-air`| Install Air for hot reloading                    |
| `clean`      | Clean build artifacts and temporary files       |
| `test`       | Run tests for bifrost-http                      |
| `ui-dev`     | Start UI development server                     |
| `ui-build`   | Build UI for production (static export)        |
| `ui-install` | Install UI dependencies                         |
| `dev-full`   | Set up full development environment             |
| `quick-start`| Quick start with example config and maxim plugin|
| `prod-build` | Build both API and UI for production           |

### Environment Variables

You can customize the development environment using these variables:

- `CONFIG_FILE`: Path to config file (default: `../config.example.json`)
- `PORT`: Server port (default: `8080`)
- `POOL_SIZE`: Connection pool size (default: `300`)
- `PLUGINS`: Comma-separated plugins to load (default: `maxim`)
- `PROMETHEUS_LABELS`: Labels for Prometheus metrics

**Example:**
```bash
make dev CONFIG_FILE=../my-config.json PORT=3000 PLUGINS=maxim,other
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