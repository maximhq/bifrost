#!/usr/bin/env node

import { Server } from "@modelcontextprotocol/sdk/server/index.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from "@modelcontextprotocol/sdk/types.js";
import { z } from "zod";

// Schemas for edge case test tools
const UnicodeToolSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  include_emojis: z.boolean().optional().describe("Include emoji characters"),
  include_rtl: z.boolean().optional().describe("Include right-to-left text"),
});

const BinaryDataSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  encoding: z.enum(["base64", "hex", "raw"]).optional(),
});

const EmptyResponseSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  type: z.enum(["empty_string", "empty_object", "empty_array", "null"]).optional(),
});

const NullFieldsSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  null_count: z.number().optional().describe("Number of null fields to include"),
});

const DeeplyNestedSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  depth: z.number().optional().describe("Nesting depth (default 10)"),
});

const SpecialCharsSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  char_type: z.enum(["quotes", "backslashes", "newlines", "control_chars", "all"]).optional(),
});

const ZeroLengthSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
});

const ExtremeSizesSchema = z.object({
  id: z.string().describe("Tool invocation ID"),
  size_type: z.enum(["tiny", "normal", "huge"]).optional(),
});

const server = new Server(
  { name: "edge-case-server", version: "1.0.0" },
  { capabilities: { tools: {} } }
);

server.setRequestHandler(ListToolsRequestSchema, async () => ({
  tools: [
    {
      name: "unicode_tool",
      description: "Returns Unicode text including emojis and RTL characters",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          include_emojis: {
            type: "boolean",
            description: "Include emoji characters",
          },
          include_rtl: {
            type: "boolean",
            description: "Include right-to-left text",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "binary_data",
      description: "Returns binary-like data in various encodings",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          encoding: {
            type: "string",
            enum: ["base64", "hex", "raw"],
            description: "Data encoding format",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "empty_response",
      description: "Returns various types of empty responses",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          type: {
            type: "string",
            enum: ["empty_string", "empty_object", "empty_array", "null"],
            description: "Type of empty response",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "null_fields",
      description: "Returns responses with null fields",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          null_count: {
            type: "number",
            description: "Number of null fields to include",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "deeply_nested",
      description: "Returns deeply nested data structures",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          depth: {
            type: "number",
            description: "Nesting depth (default 10)",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "special_chars",
      description: "Returns text with special characters that need escaping",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          char_type: {
            type: "string",
            enum: ["quotes", "backslashes", "newlines", "control_chars", "all"],
            description: "Type of special characters to include",
          },
        },
        required: ["id"],
      },
    },
    {
      name: "zero_length",
      description: "Returns zero-length content",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
        },
        required: ["id"],
      },
    },
    {
      name: "extreme_sizes",
      description: "Returns data of various extreme sizes",
      inputSchema: {
        type: "object",
        properties: {
          id: { type: "string" },
          size_type: {
            type: "string",
            enum: ["tiny", "normal", "huge"],
            description: "Size category",
          },
        },
        required: ["id"],
      },
    },
  ],
}));

server.setRequestHandler(CallToolRequestSchema, async (request) => {
  const toolName = request.params.name;

  try {
    switch (toolName) {
      case "unicode_tool": {
        const args = UnicodeToolSchema.parse(request.params.arguments);
        let text = "Unicode test: ";

        // Basic Unicode characters
        text += "Ω α β γ δ ε ζ η θ ";

        if (args.include_emojis) {
          text += "😀 😎 🔧 🚀 🎉 🌟 💻 🐍 ";
        }

        if (args.include_rtl) {
          text += "مرحبا 你好 שלום ";
        }

        // Additional Unicode ranges
        text += "© ® ™ € £ ¥ ";

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "unicode_tool",
                id: args.id,
                unicode_text: text,
                include_emojis: args.include_emojis ?? false,
                include_rtl: args.include_rtl ?? false,
              }),
            },
          ],
        };
      }

      case "binary_data": {
        const args = BinaryDataSchema.parse(request.params.arguments);
        const encoding = args.encoding || "base64";

        const binaryData = Buffer.from("This is binary data \x00\x01\x02\x03\xff\xfe");
        let encodedData: string;

        switch (encoding) {
          case "base64":
            encodedData = binaryData.toString("base64");
            break;
          case "hex":
            encodedData = binaryData.toString("hex");
            break;
          case "raw":
            encodedData = binaryData.toString("binary");
            break;
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "binary_data",
                id: args.id,
                encoding,
                data: encodedData,
              }),
            },
          ],
        };
      }

      case "empty_response": {
        const args = EmptyResponseSchema.parse(request.params.arguments);
        const type = args.type || "empty_string";

        let responseData: any;
        switch (type) {
          case "empty_string":
            responseData = "";
            break;
          case "empty_object":
            responseData = {};
            break;
          case "empty_array":
            responseData = [];
            break;
          case "null":
            responseData = null;
            break;
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "empty_response",
                id: args.id,
                type,
                data: responseData,
              }),
            },
          ],
        };
      }

      case "null_fields": {
        const args = NullFieldsSchema.parse(request.params.arguments);
        const nullCount = args.null_count || 3;

        const response: any = {
          tool: "null_fields",
          id: args.id,
        };

        // Add null fields
        for (let i = 0; i < nullCount; i++) {
          response[`null_field_${i + 1}`] = null;
        }

        response.non_null_field = "This is not null";

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify(response),
            },
          ],
        };
      }

      case "deeply_nested": {
        const args = DeeplyNestedSchema.parse(request.params.arguments);
        const depth = args.depth || 10;

        // Create deeply nested structure
        let nested: any = { value: "leaf" };
        for (let i = 0; i < depth; i++) {
          nested = {
            level: depth - i,
            child: nested,
          };
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "deeply_nested",
                id: args.id,
                depth,
                data: nested,
              }),
            },
          ],
        };
      }

      case "special_chars": {
        const args = SpecialCharsSchema.parse(request.params.arguments);
        const charType = args.char_type || "all";

        let text = "";

        if (charType === "quotes" || charType === "all") {
          text += 'Text with "double quotes" and \'single quotes\' ';
        }

        if (charType === "backslashes" || charType === "all") {
          text += "Path: C:\\Users\\Test\\file.txt ";
        }

        if (charType === "newlines" || charType === "all") {
          text += "Line 1\nLine 2\r\nLine 3\tTabbed ";
        }

        if (charType === "control_chars" || charType === "all") {
          text += "Control: \x00 \x01 \x1F ";
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "special_chars",
                id: args.id,
                char_type: charType,
                text,
              }),
            },
          ],
        };
      }

      case "zero_length": {
        const args = ZeroLengthSchema.parse(request.params.arguments);

        return {
          content: [
            {
              type: "text",
              text: "",
            },
          ],
        };
      }

      case "extreme_sizes": {
        const args = ExtremeSizesSchema.parse(request.params.arguments);
        const sizeType = args.size_type || "normal";

        let data: string;
        switch (sizeType) {
          case "tiny":
            data = "x";
            break;
          case "normal":
            data = "x".repeat(1000);
            break;
          case "huge":
            data = "x".repeat(1000000); // 1MB
            break;
        }

        return {
          content: [
            {
              type: "text",
              text: JSON.stringify({
                tool: "extreme_sizes",
                id: args.id,
                size_type: sizeType,
                data_length: data.length,
                data,
              }),
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
  console.error("Edge Case MCP Server running on stdio");
}

main().catch((error) => {
  console.error("Fatal error:", error);
  process.exit(1);
});
