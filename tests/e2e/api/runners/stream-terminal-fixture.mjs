import http from "node:http";

const port = Number(process.env.STREAM_TERMINAL_FIXTURE_PORT || "8797");
const plain = '{"candidates":[{"content":{"parts":[{"text":"hello"}],"role":"model"},"index":0}]}';
const benign = '{"text":"finishReason message_stop error blockReason\\n","metadata":{"finishReason":"STOP"}}';
const malformed = '{"finishReason":"STOP",}';
// Force another 4KB stream read after the benign/malformed marker events. An
// incorrect early terminal match must truncate bytes, not pass a coalesced read.
const padding = '{"text":"' + "x".repeat(8192) + '"}';
const usage = '{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1,"totalTokenCount":6}}';
const terminal = '{"candidates":[{"finish\\u0052eason":"STOP"}]}';
const sse = (events, delimiter) => events.map((event) => "data: " + event + delimiter).join("");
const fixtures = {
  "sse-lf": { type: "text/event-stream", body: sse([plain, benign, malformed, padding, usage, terminal], "\n\n") },
  "sse-crlf": { type: "text/event-stream", body: sse([plain, usage, terminal], "\r\n\r\n") },
  "json-array": { type: "application/json", body: "[" + [plain, usage, terminal].join(",") + "]" },
  "eof-control": { type: "text/event-stream", body: sse([plain, usage], "\n\n"), eof: true },
  "error-array": { type: "application/json", status: 429, body: '[{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"fixture rate limit"}}]' },
};

// All inference traffic stays on loopback with a dummy Gemini key. Except for
// the EOF control, never send the terminating HTTP chunk: the gateway must end
// its downstream stream on semantic completion instead of draining this body.
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, "http://localhost");
  if (req.method === "GET" && (url.pathname === "/pricing.json" || url.pathname === "/model-parameters.json")) {
    res.writeHead(200, { "Content-Type": "application/json" }).end("{}");
    return;
  }
  if (req.method !== "POST" || url.pathname !== "/models/gemini-2.5-flash:streamGenerateContent") {
    res.writeHead(404).end("unknown fixture route");
    return;
  }
  if (req.headers["x-goog-api-key"] !== "test-key") {
    res.writeHead(401).end("fixture requires test-key");
    return;
  }
  try {
    let raw = "";
    for await (const chunk of req) raw += chunk.toString("utf8");
    const request = JSON.parse(raw);
    const mode = request.contents?.[0]?.parts?.[0]?.text;
    const fixture = fixtures[mode];
    const alt = fixture?.type === "application/json" ? "json" : "sse";
    if (!fixture || url.searchParams.get("alt") !== alt) {
      res.writeHead(400).end("unknown fixture mode or alt");
      return;
    }
    res.writeHead(fixture.status || 200, { "Content-Type": fixture.type });
    res.write(fixture.body);
    if (fixture.eof) res.end();
  } catch {
    res.writeHead(400).end("invalid fixture request");
  }
});
server.listen(port, "127.0.0.1", () => {
  console.log(`Stream terminal fixture: http://127.0.0.1:${port}`);
});
for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    server.closeAllConnections();
    server.close(() => process.exit(0));
  });
}
