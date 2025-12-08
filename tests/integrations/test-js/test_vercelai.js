import { describe, it, before } from "node:test";
import assert from "node:assert";
import { generateText, streamText, tool } from "ai";
import { createOpenAI } from "@ai-sdk/openai";
import { z } from "zod";

// Configuration
const BIFROST_BASE_URL =
process.env.BIFROST_BASE_URL || "http://localhost:8080";
const VERCELAI_ENDPOINT = `${BIFROST_BASE_URL}/vercelai/v1`;

// Create OpenAI provider configured to use Bifrost's vercelai endpoint
const openai = createOpenAI({
baseURL: VERCELAI_ENDPOINT,
apiKey: "dummy-key-bifrost-handles-auth",
});

// Test messages
const SIMPLE_CHAT_MESSAGE = "Say 'Hello from Bifrost' and nothing else.";
const MULTI_TURN_MESSAGES = [
{ role: "user", content: "My name is Alice." },
{ role: "assistant", content: "Hello Alice! Nice to meet you." },
{ role: "user", content: "What is my name?" },
];

// Weather tool using Vercel AI SDK's tool() with zod schema
const weatherTool = tool({
description: "Get the current weather for a location",
parameters: z.object({
location: z.string().describe("The city and state, e.g. San Francisco, CA"),
}),
execute: async ({ location }) => ({
location,
temperature: 72,
unit: "fahrenheit",
condition: "sunny",
}),
});

async function checkBifrostAvailable() {
try {
const response = await fetch(`${BIFROST_BASE_URL}/health`);
return response.ok;
} catch {
return false;
}
}

describe("Vercel AI SDK Integration Tests", { timeout: 60000 }, () => {
before(async () => {
const available = await checkBifrostAvailable();
if (!available) {
console.log(`\n⚠️ Bifrost not available at ${BIFROST_BASE_URL}`);
process.exit(1);
}
});

it("test_01_simple_chat", async () => {
const { text } = await generateText({
model: openai("openai/gpt-4o-mini"),
prompt: SIMPLE_CHAT_MESSAGE,
maxTokens: 50,
});

assert.ok(text, "Response should not be empty");
assert.ok(text.length > 0, "Response should have content");
});

it("test_02_multi_turn_conversation", async () => {
const { text } = await generateText({
model: openai("openai/gpt-4o-mini"),
messages: MULTI_TURN_MESSAGES,
maxTokens: 50,
});

assert.ok(text, "Response should not be empty");
assert.ok(
text.toLowerCase().includes("alice"),
`Response should mention 'Alice'. Got: "${text}"`
);
});

it("test_03_single_tool_call", async () => {
const { toolCalls } = await generateText({
model: openai("openai/gpt-4o-mini"),
prompt: "What's the weather in San Francisco?",
tools: { getWeather: weatherTool },
maxTokens: 100,
});

assert.ok(toolCalls && toolCalls.length > 0, "Should have tool calls");
assert.strictEqual(toolCalls[0].toolName, "getWeather");
assert.ok(toolCalls[0].args.location, "Should have location argument");
});

it("test_04_end2end_tool_calling", async () => {
const { text, steps } = await generateText({
model: openai("openai/gpt-4o-mini"),
prompt: "What's the weather in Boston? Tell me the temperature.",
tools: { getWeather: weatherTool },
maxSteps: 3,
maxTokens: 200,
});

assert.ok(text || steps.length > 0, "Should have response or steps");
});

it("test_05_streaming", async () => {
const result = streamText({
model: openai("openai/gpt-4o-mini"),
prompt: "Count from 1 to 5.",
maxTokens: 100,
});

let fullText = "";
let chunkCount = 0;

for await (const chunk of result.textStream) {
fullText += chunk;
chunkCount++;
}

assert.ok(chunkCount > 0, "Should receive chunks");
assert.ok(fullText.length > 0, "Should receive content");
});

it("test_06_error_handling", async () => {
try {
await generateText({
model: openai("invalid-model"),
prompt: "Hello",
maxTokens: 10,
});
assert.fail("Should have thrown an error");
} catch (error) {
assert.ok(error, "Should throw error for invalid model");
}
});
});
