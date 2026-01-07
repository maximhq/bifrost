#!/usr/bin/env node
import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { CallToolRequestSchema, ListToolsRequestSchema, } from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";
// Tool input schemas
const EchoSchema = z.object({
    message: z.string().describe("The message to echo back"),
});
const CalculatorSchema = z.object({
    operation: z.enum(["add", "subtract", "multiply", "divide"]).describe("The operation to perform"),
    x: z.number().describe("First number"),
    y: z.number().describe("Second number"),
});
const WeatherSchema = z.object({
    location: z.string().describe("The location to get weather for"),
    units: z.string().optional().describe("Temperature units (celsius or fahrenheit)"),
});
const DelaySchema = z.object({
    seconds: z.number().describe("Number of seconds to delay"),
});
const ThrowErrorSchema = z.object({
    error_message: z.string().describe("The error message to throw"),
});
// Create the MCP server
const server = new Server({
    name: "test-tools-server",
    version: "1.0.0",
}, {
    capabilities: {
        tools: {},
    },
});
// Handler for listing available tools
server.setRequestHandler(ListToolsRequestSchema, async () => {
    return {
        tools: [
            {
                name: "echo",
                description: "Echoes back the provided message",
                inputSchema: {
                    type: "object",
                    properties: {
                        message: {
                            type: "string",
                            description: "The message to echo back",
                        },
                    },
                    required: ["message"],
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
                description: "Gets weather information for a location",
                inputSchema: {
                    type: "object",
                    properties: {
                        location: {
                            type: "string",
                            description: "The location to get weather for",
                        },
                        units: {
                            type: "string",
                            description: "Temperature units (celsius or fahrenheit)",
                        },
                    },
                    required: ["location"],
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
            case "echo": {
                const args = EchoSchema.parse(request.params.arguments);
                return {
                    content: [
                        {
                            type: "text",
                            text: JSON.stringify({ message: args.message }),
                        },
                    ],
                };
            }
            case "calculator": {
                const args = CalculatorSchema.parse(request.params.arguments);
                let result;
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
                const args = WeatherSchema.parse(request.params.arguments);
                // Mock weather data
                return {
                    content: [
                        {
                            type: "text",
                            text: JSON.stringify({
                                location: args.location,
                                temperature: 72,
                                units: args.units || "fahrenheit",
                                condition: "Partly Cloudy",
                            }),
                        },
                    ],
                };
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
    }
    catch (error) {
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
    console.error("Test Tools MCP Server running on stdio");
}
main().catch((error) => {
    console.error("Fatal error in main():", error);
    process.exit(1);
});
