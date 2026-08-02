#!/usr/bin/env node
// Adds generated provider-harness coverage rows that would be too repetitive to
// maintain by hand in the checked-in Postman collection.

import { readFileSync, writeFileSync } from "node:fs";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) {
      const key = cur.slice(2);
      const next = arr[i + 1];
      acc.push([key, next && !next.startsWith("--") ? next : "true"]);
    }
    return acc;
  }, [])
);

const source = args.source;
const out = args.out;

if (!source || !out) {
  console.error("[augment-provider-harness] --source and --out are required");
  process.exit(2);
}

const collection = JSON.parse(readFileSync(source, "utf8"));

const responseNonEmptyTest = {
  listen: "test",
  script: {
    type: "text/javascript",
    exec: [
      "if (pm.response.code < 400) { pm.test('Thinking response non-empty', function () { var j = pm.response.json(); var msg = j.choices && j.choices[0] && j.choices[0].message; var c = (msg && msg.content) || ''; var r = (msg && (msg.reasoning || msg.reasoning_details)) || null; var parts = (j.candidates && j.candidates[0] && j.candidates[0].content && j.candidates[0].content.parts) || []; var gc = parts.some(function (p) { return p && (p.text || p.thought || p.thoughtSignature); }); pm.expect(c || r || gc, 'expected answer or reasoning content').to.be.ok; }); }",
    ],
  },
};

const streamingTest = {
  listen: "test",
  script: {
    type: "text/javascript",
    exec: [
      "if (pm.response.code < 400) { pm.test('Streaming: response is SSE', function () { var ct = pm.response.headers.get('content-type') || ''; pm.expect(ct, 'expected SSE, got ' + ct).to.include('event-stream'); }); }",
    ],
  },
};

const chatUrl = {
  raw: "{{baseUrl}}/v1/chat/completions",
  host: ["{{baseUrl}}"],
  path: ["v1", "chat", "completions"],
};

const item = ({ name, body, stream = false }) => ({
  name,
  event: [stream ? streamingTest : responseNonEmptyTest],
  request: {
    method: "POST",
    header: [{ key: "Content-Type", value: "application/json" }],
    body: { mode: "raw", raw: JSON.stringify(body, null, 2) },
    url: chatUrl,
  },
});

const genaiItem = ({ name, model, body }) => ({
  name,
  event: [responseNonEmptyTest],
  request: {
    method: "POST",
    header: [
      { key: "Content-Type", value: "application/json" },
      { key: "x-goog-api-key", value: "{{genaiKey}}" },
    ],
    body: { mode: "raw", raw: JSON.stringify(body, null, 2) },
    url: {
      raw: `{{baseUrl}}/genai/v1beta/models/${model}:generateContent`,
      host: ["{{baseUrl}}"],
      path: ["genai", "v1beta", "models", `${model}:generateContent`],
    },
  },
});

const effortModels = [
  { label: "openai/gpt-5", model: "openai/gpt-5" },
  { label: "openai/gpt-5-mini", model: "openai/gpt-5-mini" },
  { label: "openai/o3-mini", model: "openai/o3-mini" },
  { label: "anthropic/claude-opus-4-7", model: "anthropic/claude-opus-4-7", maxTokens: 4096 },
  { label: "anthropic/claude-sonnet-4-6", model: "anthropic/claude-sonnet-4-6", maxTokens: 4096 },
  { label: "bedrock/claude-opus-4-7", model: "bedrock/global.anthropic.claude-opus-4-7", maxTokens: 4096 },
  { label: "bedrock/claude-sonnet-4-6", model: "bedrock/global.anthropic.claude-sonnet-4-6", maxTokens: 4096 },
  { label: "gemini/gemini-2.5-flash", model: "gemini/gemini-2.5-flash" },
  { label: "gemini/gemini-2.5-pro", model: "gemini/gemini-2.5-pro" },
  { label: "vertex/gemini-2.5-flash", model: "vertex/gemini-2.5-flash" },
  { label: "vertex/gemini-2.5-pro", model: "vertex/gemini-2.5-pro" },
  { label: "vertex/claude-opus-4-7", model: "vertex/claude-opus-4-7", maxTokens: 4096 },
  { label: "vertex/claude-sonnet-4-6", model: "vertex/claude-sonnet-4-6", maxTokens: 4096 },
];

const efforts = ["low", "medium", "high"];

const bodyForEffort = (model, effort, stream = false) => {
  const body = {
    model: model.model,
    messages: [{ role: "user", content: "Solve 17 * 23 + 12. Keep the final answer concise." }],
    reasoning_effort: effort,
  };
  if (model.maxTokens) body.max_tokens = model.maxTokens;
  if (stream) body.stream = true;
  return body;
};

const effortItems = effortModels.flatMap((model) =>
  efforts.map((effort) =>
    item({
      name: `Cross-cut: ${model.label} thinking effort ${effort}`,
      body: bodyForEffort(model, effort, false),
    })
  )
);

const streamingEffortItems = effortModels.flatMap((model) =>
  efforts.map((effort) =>
    item({
      name: `Cross-cut: ${model.label} streaming + thinking effort ${effort}`,
      body: bodyForEffort(model, effort, true),
      stream: true,
    })
  )
);

const nativeThinkingItems = [
  item({
    name: "Cross-cut: anthropic/claude-sonnet-4-6 thinking budget lowest",
    body: {
      model: "anthropic/claude-sonnet-4-6",
      max_tokens: 4096,
      thinking: { type: "enabled", budget_tokens: 1024 },
      messages: [{ role: "user", content: "Solve 31 * 27." }],
    },
  }),
  item({
    name: "Cross-cut: anthropic/claude-sonnet-4-6 thinking budget highest",
    body: {
      model: "anthropic/claude-sonnet-4-6",
      max_tokens: 8192,
      thinking: { type: "enabled", budget_tokens: 4096 },
      messages: [{ role: "user", content: "Solve 31 * 27." }],
    },
  }),
  item({
    name: "Cross-cut: anthropic/claude-opus-4-7 adaptive thinking",
    body: {
      model: "anthropic/claude-opus-4-7",
      max_tokens: 4096,
      thinking: { type: "adaptive" },
      messages: [{ role: "user", content: "Solve 31 * 27." }],
    },
  }),
  genaiItem({
    name: "Cross-cut: gemini/gemini-2.5-flash thinking budget lowest",
    model: "gemini-2.5-flash",
    body: { contents: [{ parts: [{ text: "Solve 31 * 27." }] }], generationConfig: { thinkingConfig: { thinkingBudget: 1024 } } },
  }),
  genaiItem({
    name: "Cross-cut: gemini/gemini-2.5-pro thinking budget highest",
    model: "gemini-2.5-pro",
    body: { contents: [{ parts: [{ text: "Solve 31 * 27." }] }], generationConfig: { thinkingConfig: { thinkingBudget: 8192 } } },
  }),
];

const streamingFeatureItems = [
  item({
    name: "Cross-cut: openai/gpt-4o-mini streaming + function calling tools",
    stream: true,
    body: {
      model: "openai/gpt-4o-mini",
      messages: [{ role: "user", content: "Weather in Lagos?" }],
      stream: true,
      tools: [{ type: "function", function: { name: "get_weather", parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] } } }],
    },
  }),
  item({
    name: "Cross-cut: anthropic/claude-haiku-4-5 streaming + function calling tools",
    stream: true,
    body: {
      model: "anthropic/claude-haiku-4-5",
      max_tokens: 512,
      messages: [{ role: "user", content: "Weather in Lagos?" }],
      stream: true,
      tools: [{ type: "function", function: { name: "get_weather", parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] } } }],
    },
  }),
  item({
    name: "Cross-cut: gemini/gemini-2.5-flash streaming + function calling tools",
    stream: true,
    body: {
      model: "gemini/gemini-2.5-flash",
      messages: [{ role: "user", content: "Weather in Lagos?" }],
      stream: true,
      tools: [{ type: "function", function: { name: "get_weather", parameters: { type: "object", properties: { city: { type: "string" } }, required: ["city"] } } }],
    },
  }),
  item({
    name: "Cross-cut: openai/gpt-4o-mini streaming + vision image input",
    stream: true,
    body: {
      model: "openai/gpt-4o-mini",
      messages: [{ role: "user", content: [{ type: "text", text: "Describe this image briefly." }, { type: "image_url", image_url: { url: "https://storage.googleapis.com/generativeai-downloads/images/scones.jpg" } }] }],
      stream: true,
    },
  }),
  item({
    name: "Cross-cut: anthropic/claude-haiku-4-5 streaming + vision image input",
    stream: true,
    body: {
      model: "anthropic/claude-haiku-4-5",
      max_tokens: 512,
      messages: [{ role: "user", content: [{ type: "image", source: { type: "url", url: "https://storage.googleapis.com/generativeai-downloads/images/scones.jpg" } }, { type: "text", text: "Describe this image briefly." }] }],
      stream: true,
    },
  }),
  item({
    name: "Cross-cut: gemini/gemini-2.5-flash streaming + vision image input",
    stream: true,
    body: {
      model: "gemini/gemini-2.5-flash",
      messages: [{ role: "user", content: [{ type: "text", text: "Describe this image briefly." }, { type: "image_url", image_url: { url: "https://storage.googleapis.com/generativeai-downloads/images/scones.jpg" } }] }],
      stream: true,
    },
  }),
  item({
    name: "Cross-cut: openai/gpt-4o-mini streaming + structured output json_schema",
    stream: true,
    body: {
      model: "openai/gpt-4o-mini",
      messages: [{ role: "user", content: "Extract city/country/population for Paris." }],
      stream: true,
      response_format: { type: "json_schema", json_schema: { name: "city", strict: true, schema: { type: "object", properties: { city: { type: "string" }, country: { type: "string" }, population: { type: "number" } }, required: ["city", "country", "population"], additionalProperties: false } } },
    },
  }),
  item({
    name: "Cross-cut: gemini/gemini-2.5-flash streaming + structured output json_schema",
    stream: true,
    body: {
      model: "gemini/gemini-2.5-flash",
      messages: [{ role: "user", content: "Extract city/country/population for Paris." }],
      stream: true,
      response_format: { type: "json_schema", json_schema: { name: "city", strict: true, schema: { type: "object", properties: { city: { type: "string" }, country: { type: "string" }, population: { type: "number" } }, required: ["city", "country", "population"], additionalProperties: false } } },
    },
  }),
  item({
    name: "Cross-cut: anthropic/claude-opus-4-7 streaming + web search",
    stream: true,
    body: {
      model: "anthropic/claude-opus-4-7",
      max_tokens: 1024,
      messages: [{ role: "user", content: "Find one current headline and summarize it in one sentence." }],
      stream: true,
      tools: [{ type: "web_search_20250305", name: "web_search", max_uses: 1 }],
    },
  }),
  item({
    name: "Cross-cut: gemini/gemini-2.5-flash streaming + google search",
    stream: true,
    body: {
      model: "gemini/gemini-2.5-flash",
      messages: [{ role: "user", content: "Find one current headline and summarize it in one sentence." }],
      stream: true,
      tools: [{ type: "google_search" }],
    },
  }),
  item({
    name: "Cross-cut: anthropic/claude-haiku-4-5 streaming + prompt caching",
    stream: true,
    body: {
      model: "anthropic/claude-haiku-4-5",
      max_tokens: 512,
      system: [{ type: "text", text: "Reusable cached context for streaming prompt caching coverage.", cache_control: { type: "ephemeral" } }],
      messages: [{ role: "user", content: "Reply with a short acknowledgement." }],
      stream: true,
    },
  }),
  item({
    name: "Cross-cut: openai/gpt-4o-mini streaming + stop sequences",
    stream: true,
    body: {
      model: "openai/gpt-4o-mini",
      messages: [{ role: "user", content: "Count one, two, three, four." }],
      stop: ["three"],
      stream: true,
    },
  }),
  item({
    name: "Cross-cut: openai/gpt-4o-mini streaming + sampling params",
    stream: true,
    body: {
      model: "openai/gpt-4o-mini",
      messages: [{ role: "user", content: "Pick a color and explain why." }],
      temperature: 0.7,
      top_p: 0.9,
      stream: true,
    },
  }),
];

const generatedFolders = [
  {
    name: "Cross-Cut Round 29: Thinking Effort Ladder (generated)",
    description: "Generated at harness runtime. Covers low, medium, and high reasoning_effort across reasoning-capable OpenAI, Anthropic, Bedrock, Gemini, and Vertex model families.",
    item: effortItems,
  },
  {
    name: "Cross-Cut Round 30: Streaming + Thinking Matrix (generated)",
    description: "Generated at harness runtime. Combines stream:true with low, medium, and high reasoning_effort across reasoning-capable model families.",
    item: streamingEffortItems,
  },
  {
    name: "Cross-Cut Round 31: Native Thinking Modes (generated)",
    description: "Generated at harness runtime. Covers native thinking controls where providers expose explicit budgets or adaptive modes.",
    item: nativeThinkingItems,
  },
  {
    name: "Cross-Cut Round 32: Streaming Feature Matrix (generated)",
    description: "Generated at harness runtime. Ensures each interactive feature bucket has streaming coverage where the request shape supports stream:true.",
    item: streamingFeatureItems,
  },
];

const findFolder = (items, name) => {
  for (const entry of items || []) {
    if (entry.name === name) return entry;
    const nested = findFolder(entry.item, name);
    if (nested) return nested;
  }
  return null;
};

const backlog = findFolder(collection.item, "12. Backlog Coverage (auto-added missing cases)");
if (!backlog) {
  console.error("[augment-provider-harness] backlog folder not found");
  process.exit(1);
}

const generatedNames = new Set(generatedFolders.map((f) => f.name));
backlog.item = (backlog.item || []).filter((entry) => !generatedNames.has(entry.name));
backlog.item.push(...generatedFolders);

writeFileSync(out, `${JSON.stringify(collection, null, 2)}\n`);
const generatedCount = generatedFolders.reduce((sum, folder) => sum + folder.item.length, 0);
console.error(`[augment-provider-harness] wrote ${out} with ${generatedCount} generated requests`);
