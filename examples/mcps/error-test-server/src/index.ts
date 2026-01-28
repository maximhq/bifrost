#!/usr/bin/env node

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

// Schemas for error test tools
const MalformedJsonSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  json_type: z.enum(["truncated", "invalid_escape", "unclosed_bracket", "mixed_types"]).optional(),
});

const TimeoutToolSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  timeout_ms: z.number().optional().describe("Timeout duration in milliseconds (default 5000)"),
});

const IntermittentFailSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  fail_rate: z.number().min(0).max(1).optional().describe("Probability of failure (0-1, default 0.5)"),
});

const NetworkErrorSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  error_type: z.enum(["connection_refused", "timeout", "dns_failure", "ssl_error"]).optional(),
});

const LargePayloadSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  size_kb: z.number().optional().describe("Payload size in KB (default 100)"),
});

const PartialResponseSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  break_at: z.enum(["start", "middle", "end"]).optional().describe("Where to break the response"),
});

const server = new Server(
  { name: "error-test-server", version: "1.0.0" },
  { capabilities: { tools: {} } }
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: "malformed_json",
      description: "Returns malformed JSON to test error handling",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          json_type: {
            type: "string",
            enum: ["truncated", "invalid_escape", "unclosed_bracket", "mixed_types"],
            description: "Type of JSON malformation",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "timeout_tool",
      description: "Hangs for a specified duration to test timeouts",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          timeout_ms: {
            type: "number",
            description: "Timeout duration in milliseconds (default 5000)",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "intermittent_fail",
      description: "Randomly fails to test retry logic",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          fail_rate: {
            type: "number",
            minimum: 0,
            maximum: 1,
            description: "Probability of failure (0-1, default 0.5)",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "network_error",
      description: "Simulates various network errors",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          error_type: {
            type: "string",
            enum: ["connection_refused", "timeout", "dns_failure", "ssl_error"],
            description: "Type of network error to simulate",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "large_payload",
      description: "Returns a very large payload to test size limits",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          size_kb: {
            type: "number",
            description: "Payload size in KB (default 100)",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "partial_response",
      description: "Returns incomplete response to test handling",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          break_at: {
            type: "string",
            enum: ["start", "middle", "end"],
            description: "Where to break the response",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "invalid_content_type",
      description: "Returns content with mismatched type declaration",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
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
    switch (toolName) {
      case "malformed_json": {
        const args = MalformedJsonSchema.parse(request.params.arguments);
        const jsonType = args.json_type || "truncated";

        let malformedText: string;
        switch (jsonType) {
          case "truncated":
            malformedText = '{"status": "success", "data": {"items": [1, 2, 3';
            break;
          case "invalid_escape":
            malformedText = '{"status": "success", "message": "Invalid \\x escape"}';
            break;
          case "unclosed_bracket":
            malformedText = '{"status": "success", "data": [1, 2, 3]';
            break;
          case "mixed_types":
            malformedText = '{"status": "success", "value": NaN, "other": undefined}';
            break;
          default:
            malformedText = '{"incomplete": true';
        }

        return {
          content: [
            {
              type: "text",
              text: malformedText,
            },
          ],
        };
      }

      case "timeout_tool": {
        const args = TimeoutToolSchema.parse(request.params.arguments);
        const timeoutMs = args.timeout_ms || 5000;

        // Hang for the specified duration
        await new Promise((resolve) => setTimeout(resolve, timeoutMs));

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "timeout_tool",
                id: args.id,
                timeout_ms: timeoutMs,
                message: "This should have timed out",
              }),
            },
          ],
        };
      }

      case "intermittent_fail": {
        const args = IntermittentFailSchema.parse(request.params.arguments);
        const failRate = args.fail_rate ?? 0.5;

        // Randomly fail based on fail_rate
        if (Math.random() < failRate) {
          return {
            content: [
              {
                type: "text",
                text: JSON.stringify({
                  error: "Intermittent failure occurred",
                  id: args.id,
                  fail_rate: failRate,
                }),
              },
            ],
            isError: true,
          };
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "intermittent_fail",
                id: args.id,
                success: true,
                fail_rate: failRate,
              }),
            },
          ],
        };
      }

      case "network_error": {
        const args = NetworkErrorSchema.parse(request.params.arguments);
        const errorType = args.error_type || "connection_refused";

        const errorMessages = {
          connection_refused: "Connection refused: Unable to connect to remote server",
          timeout: "Request timeout: Server did not respond within timeout period",
          dns_failure: "DNS resolution failed: Unable to resolve hostname",
          ssl_error: "SSL handshake failed: Certificate verification error",
        };

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                error: errorMessages[errorType],
                error_type: errorType,
                id: args.id,
              }),
            },
          ],
          isError: true,
        };
      }

      case "large_payload": {
        const args = LargePayloadSchema.parse(request.params.arguments);
        const sizeKb = args.size_kb || 100;

        // Generate a large string (approximately sizeKb KB)
        const chunkSize = 1024; // 1 KB chunks
        const chunks: string[] = [];
        for (let i = 0; i < sizeKb; i++) {
          chunks.push("x".repeat(chunkSize));
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "large_payload",
                id: args.id,
                size_kb: sizeKb,
                payload: chunks.join(""),
                message: `Generated ${sizeKb}KB payload`,
              }),
            },
          ],
        };
      }

      case "partial_response": {
        const args = PartialResponseSchema.parse(request.params.arguments);
        const breakAt = args.break_at || "middle";

        let response: string;
        switch (breakAt) {
          case "start":
            response = '{"sta';
            break;
          case "middle":
            response = '{"status": "success", "data": {"incomplete';
            break;
          case "end":
            response = '{"status": "success", "data": {"complete": true}, "message": "Almost done"';
            break;
        }

        return {
          content: [
            {
              type: "text",
              text: response,
            },
          ],
        };
      }

      case "invalid_content_type": {
        const args = z.object({ id: z.string() }).parse(request.params.arguments);

        // Return a response that claims to be JSON but isn't properly formatted
        return {
          content: [
            {
              type: "text",
              text: "This is not valid JSON content but the server says it is",
            },
          ],
        };
      }

      default:
        throw new Error(`Unknown tool: ${toolName}`);
    }
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
  console.error("Error Test MCP Server running on stdio");
}

main().catch((error) => {
  console.error("Fatal error:", error);
  process.exit(1);
});
