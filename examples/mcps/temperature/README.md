# Temperature MCP Server

A simple Model Context Protocol (MCP) server that provides temperature information for popular cities around the world. This server exposes a single tool `get_temperature` that returns dummy temperature data for demonstration purposes.

## Features

- Single MCP tool: `get_temperature`
- Supports 20+ popular cities worldwide
- Returns temperature in Celsius or Fahrenheit
- Includes weather conditions
- Uses dummy/mock data (no external API calls)

## Installation

```bash
npm install
```

## Build

```bash
npm run build
```

## Usage

### Running the Server

The server runs on stdio transport (standard input/output) by default:

```bash
npm start
```

### Using with MCP Clients

This server can be used with any MCP-compatible client. Add it to your client configuration:

```json
{
  "mcpServers": {
    "temperature": {
      "command": "node",
      "args": ["/path/to/temperature-mcp/dist/index.js"]
    }
  }
}
```

### Available Tool

#### get_temperature

Get the current temperature for a popular city.

**Input:**
- `location` (string, required): The name of the city

**Example:**
```json
{
  "location": "New York"
}
```

**Output:**
```
Temperature in New York: 72°F
Condition: Partly Cloudy
```

## Supported Cities

The server provides temperature data for the following cities:

- New York, Los Angeles, San Francisco, Chicago (USA)
- London, Paris, Berlin, Moscow (Europe)
- Tokyo, Beijing, Shanghai, Hong Kong, Seoul, Singapore (Asia)
- Sydney (Australia)
- Dubai (Middle East)
- Mumbai (India)
- Toronto (Canada)
- Mexico City (Mexico)
- Rio de Janeiro (Brazil)

## Development

To run in development mode:

```bash
npm run dev
```

## Architecture

This server demonstrates:
- TypeScript MCP server implementation
- Tool registration and execution
- Input validation using Zod
- Stdio transport for communication
- Error handling and user-friendly messages

## Note

This server uses dummy data for demonstration purposes. In a production environment, you would integrate with a real weather API service.
