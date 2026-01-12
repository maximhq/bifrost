#!/usr/bin/env node

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
import * as fs from "fs";

// Dummy temperature data for popular locations
const TEMPERATURE_DATA: Record<string, { temperature: number; unit: string; condition: string }> = {
  "new york": { temperature: 72, unit: "F", condition: "Partly Cloudy" },
  "london": { temperature: 15, unit: "C", condition: "Rainy" },
  "tokyo": { temperature: 22, unit: "C", condition: "Clear" },
  "paris": { temperature: 18, unit: "C", condition: "Cloudy" },
  "sydney": { temperature: 25, unit: "C", condition: "Sunny" },
  "dubai": { temperature: 35, unit: "C", condition: "Hot and Sunny" },
  "singapore": { temperature: 30, unit: "C", condition: "Humid" },
  "mumbai": { temperature: 32, unit: "C", condition: "Humid and Partly Cloudy" },
  "los angeles": { temperature: 75, unit: "F", condition: "Sunny" },
  "san francisco": { temperature: 62, unit: "F", condition: "Foggy" },
  "chicago": { temperature: 68, unit: "F", condition: "Windy" },
  "toronto": { temperature: 18, unit: "C", condition: "Clear" },
  "berlin": { temperature: 16, unit: "C", condition: "Cloudy" },
  "moscow": { temperature: 10, unit: "C", condition: "Cold" },
  "beijing": { temperature: 20, unit: "C", condition: "Clear" },
  "shanghai": { temperature: 24, unit: "C", condition: "Partly Cloudy" },
  "hong kong": { temperature: 28, unit: "C", condition: "Humid" },
  "seoul": { temperature: 19, unit: "C", condition: "Clear" },
  "mexico city": { temperature: 22, unit: "C", condition: "Sunny" },
  "rio de janeiro": { temperature: 28, unit: "C", condition: "Tropical" },
};

// Tool input schemas
const GetTemperatureSchema = z.object({
  location: z.string().describe("The name of the city (e.g., 'New York', 'London', 'Tokyo')"),
});

const EchoSchema = z.object({
  text: z.string().describe("The text to echo back"),
});

const CalculatorSchema = z.object({
  operation: z.enum(["add", "subtract", "multiply", "divide"]).describe("The operation to perform"),
  x: z.number().describe("First number"),
  y: z.number().describe("Second number"),
});

const GetWeatherSchema = z.object({
  location: z.string().describe("The location to get weather for"),
});

const SearchSchema = z.object({
  query: z.string().describe("The search query"),
});

const GetTimeSchema = z.object({
  timezone: z.string().optional().describe("The timezone (optional)"),
});

const ReadFileSchema = z.object({
  path: z.string().describe("The file path to read"),
});

const DelaySchema = z.object({
  seconds: z.number().describe("Number of seconds to delay"),
});

const ThrowErrorSchema = z.object({
  error_message: z.string().describe("The error message to throw"),
});

// Create the MCP server
const server = new Server(
  {
    name: "temperature-server",
    version: "1.0.0",
  },
  {
    capabilities: {
      tools: {},
    },
  }
);

// Handler for listing available tools
server.setRequestHandler(ListToolsRequestSchema, async () => {
  return {
    tools: [
      {
        name: "get_temperature",
        description: "Get the current temperature for a popular city. Supports major cities worldwide.",
        inputSchema: {
          type: "object",
          properties: {
            location: {
              type: "string",
              description: "The name of the city (e.g., 'New York', 'London', 'Tokyo')",
            },
          },
          required: ["location"],
        },
      },
      {
        name: "echo",
        description: "Echoes back the provided text",
        inputSchema: {
          type: "object",
          properties: {
            text: {
              type: "string",
              description: "The text to echo back",
            },
          },
          required: ["text"],
        },
      },
      {
        name: "calculator",
        description: "Performs basic arithmetic operations",
        inputSchema: {
          type: "object",
          properties: {
            operation: {
              type: "string",
              enum: ["add", "subtract", "multiply", "divide"],
              description: "The operation to perform",
            },
            x: {
              type: "number",
              description: "First number",
            },
            y: {
              type: "number",
              description: "Second number",
            },
          },
          required: ["operation", "x", "y"],
        },
      },
      {
        name: "get_weather",
        description: "Get weather information for a location (alias for get_temperature)",
        inputSchema: {
          type: "object",
          properties: {
            location: {
              type: "string",
              description: "The location to get weather for",
            },
          },
          required: ["location"],
        },
      },
      {
        name: "search",
        description: "Performs a search operation",
        inputSchema: {
          type: "object",
          properties: {
            query: {
              type: "string",
              description: "The search query",
            },
          },
          required: ["query"],
        },
      },
      {
        name: "get_time",
        description: "Gets the current time",
        inputSchema: {
          type: "object",
          properties: {
            timezone: {
              type: "string",
              description: "The timezone (optional)",
            },
          },
        },
      },
      {
        name: "read_file",
        description: "Reads a file from the filesystem",
        inputSchema: {
          type: "object",
          properties: {
            path: {
              type: "string",
              description: "The file path to read",
            },
          },
          required: ["path"],
        },
      },
      {
        name: "delay",
        description: "Delays execution for specified seconds",
        inputSchema: {
          type: "object",
          properties: {
            seconds: {
              type: "number",
              description: "Number of seconds to delay",
            },
          },
          required: ["seconds"],
        },
      },
      {
        name: "throw_error",
        description: "Throws an error with specified message",
        inputSchema: {
          type: "object",
          properties: {
            error_message: {
              type: "string",
              description: "The error message to throw",
            },
          },
          required: ["error_message"],
        },
      },
    ],
  };
});

// Handler for tool execution
server.setRequestHandler(CallToolRequestSchema, async (request) => {
  try {
    const toolName = request.params.name;

    switch (toolName) {
      case "get_temperature": {
        const args = GetTemperatureSchema.parse(request.params.arguments);
        const locationKey = args.location.toLowerCase();

        if (!(locationKey in TEMPERATURE_DATA)) {
          const availableCities = Object.keys(TEMPERATURE_DATA)
            .map((city) => city.charAt(0).toUpperCase() + city.slice(1))
            .join(", ");

          return {
            content: [
              {
                type: "text",
                text: `Sorry, temperature data is not available for "${args.location}". Available cities: ${availableCities}`,
              },
            ],
            isError: true,
          };
        }

        const data = TEMPERATURE_DATA[locationKey];
        const locationDisplay = args.location.charAt(0).toUpperCase() + args.location.slice(1);

        return {
          content: [
            {
              type: "text",
              text: `Temperature in ${locationDisplay}: ${data.temperature}°${data.unit}\nCondition: ${data.condition}`,
            },
          ],
        };
      }

      case "echo": {
        const args = EchoSchema.parse(request.params.arguments);
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({ text: args.text }),
            },
          ],
        };
      }

      case "calculator": {
        const args = CalculatorSchema.parse(request.params.arguments);
        let result: number;

        switch (args.operation) {
          case "add":
            result = args.x + args.y;
            break;
          case "subtract":
            result = args.x - args.y;
            break;
          case "multiply":
            result = args.x * args.y;
            break;
          case "divide":
            if (args.y === 0) {
              return {
                content: [
                  {
                    type: "text",
                    text: "Error: Division by zero",
                  },
                ],
                isError: true,
              };
            }
            result = args.x / args.y;
            break;
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({ result }),
            },
          ],
        };
      }

      case "get_weather": {
        // Alias for get_temperature
        const args = GetWeatherSchema.parse(request.params.arguments);
        const locationKey = args.location.toLowerCase();

        if (!(locationKey in TEMPERATURE_DATA)) {
          return {
            content: [
              {
                type: "text",
                text: `Weather data not available for "${args.location}"`,
              },
            ],
            isError: true,
          };
        }

        const data = TEMPERATURE_DATA[locationKey];
        const locationDisplay = args.location.charAt(0).toUpperCase() + args.location.slice(1);

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                location: locationDisplay,
                temperature: data.temperature,
                unit: data.unit,
                condition: data.condition,
              }),
            },
          ],
        };
      }

      case "search": {
        const args = SearchSchema.parse(request.params.arguments);
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                query: args.query,
                results: [`Result 1 for ${args.query}`, `Result 2 for ${args.query}`],
              }),
            },
          ],
        };
      }

      case "get_time": {
        const args = GetTimeSchema.parse(request.params.arguments);
        const currentTime = new Date();
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                time: currentTime.toISOString(),
                timezone: args.timezone || "UTC",
              }),
            },
          ],
        };
      }

      case "read_file": {
        const args = ReadFileSchema.parse(request.params.arguments);
        try {
          const content = fs.readFileSync(args.path, "utf-8");
          return {
            content: [
              {
                type: "text",
                text: JSON.stringify({ path: args.path, content }),
              },
            ],
          };
        } catch (fileError) {
          return {
            content: [
              {
                type: "text",
                text: `Error reading file: ${fileError instanceof Error ? fileError.message : String(fileError)}`,
              },
            ],
            isError: true,
          };
        }
      }

      case "delay": {
        const args = DelaySchema.parse(request.params.arguments);
        await new Promise((resolve) => setTimeout(resolve, args.seconds * 1000));
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                delayed_seconds: args.seconds,
                message: `Delayed for ${args.seconds} seconds`,
              }),
            },
          ],
        };
      }

      case "throw_error": {
        const args = ThrowErrorSchema.parse(request.params.arguments);
        return {
          content: [
            {
              type: "text",
              text: args.error_message,
            },
          ],
          isError: true,
        };
      }

      default:
        throw new Error(`Unknown tool: ${toolName}`);
    }
  } catch (error) {
    console.error(`Error in tool execution:`, error);
    return {
      content: [
        {
          type: "text",
          text: `Error: ${error instanceof Error ? error.message : String(error)}`,
        },
      ],
      isError: true,
    };
  }
});

// Start the server with stdio transport
async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);

  // Keep process alive - stdin will keep the process running
  // The process will exit when stdin is closed by the parent
  process.stdin.resume();

  console.error("Temperature MCP Server running on stdio");
}

main().catch((error) => {
  console.error("Fatal error in main():", error);
  process.exit(1);
});
