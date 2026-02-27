#!/usr/bin/env node

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

// Schemas for parallel test tools
const FastToolSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
});

const SlowToolSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  delay_ms: z.number().optional().describe("Delay in milliseconds (default 100)"),
});

const server = new Server(
  { name: "parallel-test-server", version: "1.0.0" },
  { capabilities: { tools: {} } }
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: "fast_tool_1",
      description: "Fast tool (10ms delay)",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
    },
    {
      name: "fast_tool_2",
      description: "Fast tool (20ms delay)",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
    },
    {
      name: "medium_tool_1",
      description: "Medium tool (50ms delay)",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
    },
    {
      name: "medium_tool_2",
      description: "Medium tool (75ms delay)",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
    },
    {
      name: "slow_tool_1",
      description: "Slow tool (100ms delay)",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
    },
    {
      name: "slow_tool_2",
      description: "Slow tool (150ms delay)",
      inputSchema: {
        type: "object",
        properties: { id: { type: "string" } },
        required: ["id"],
      },
    },
    {
      name: "variable_delay",
      description: "Tool with configurable delay",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          delay_ms: { type: "number", description: "Delay in milliseconds" },
        },
        required: ["id"],
      },
    },
  ],
}));

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const toolName = request.params.name;
  const startTime = Date.now();

  try {
    let delay = 0;
    let args: any;

    switch (toolName) {
      case "fast_tool_1":
        args = FastToolSchema.parse(request.params.arguments);
        delay = 10;
        break;
      case "fast_tool_2":
        args = FastToolSchema.parse(request.params.arguments);
        delay = 20;
        break;
      case "medium_tool_1":
        args = FastToolSchema.parse(request.params.arguments);
        delay = 50;
        break;
      case "medium_tool_2":
        args = FastToolSchema.parse(request.params.arguments);
        delay = 75;
        break;
      case "slow_tool_1":
        args = FastToolSchema.parse(request.params.arguments);
        delay = 100;
        break;
      case "slow_tool_2":
        args = FastToolSchema.parse(request.params.arguments);
        delay = 150;
        break;
      case "variable_delay":
        args = SlowToolSchema.parse(request.params.arguments);
        delay = args.delay_ms || 100;
        break;
      default:
        throw new Error(`Unknown tool: ${toolName}`);
    }

    await new Promise((resolve) => setTimeout(resolve, delay));
    const elapsed = Date.now() - startTime;

    return {
      content: [
        {
          type: "text",
          text: JSON.stringify({
            tool: toolName,
            id: args.id,
            delay_ms: delay,
            actual_elapsed_ms: elapsed,
            completed_at: new Date().toISOString(),
          }),
        },
      ],
    };
  } catch (error) {
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

async function main() {
  const transport = new StdioServerTransport();
  await server.connect(transport);
  console.error("Parallel Test MCP Server running on stdio");
}

main().catch((error) => {
  console.error("Fatal error:", error);
  process.exit(1);
});
