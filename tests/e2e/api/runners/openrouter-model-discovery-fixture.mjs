import { createServer } from "node:http";

const allowed = "openai/fixture-allowed";
const restricted = "qwen/fixture-restricted-embedding";
const port = Number(process.argv[2] || 18955);

createServer((req, res) => {
  const url = new URL(req.url, "http://localhost");
  const authenticated = req.headers.authorization === "Bearer openrouter-discovery-fixture";
  const json = (status, value) => {
    res.writeHead(status, { "content-type": "application/json" });
    res.end(JSON.stringify(value));
  };
  const models = (...ids) => json(200, { data: ids.map((id) => ({ id })) });
  if (req.method !== "GET") return json(405, { error: { message: "GET required" } });
  switch (url.pathname) {
    case "/v1/models/user":
      if (!authenticated) return json(401, { error: { message: "fixture key required" } });
      if (url.searchParams.get("output_modalities") !== "all") {
        return json(400, { error: { message: "all modalities required" } });
      }
      return models(allowed);
    case "/v1/auth/key":
      return authenticated ? json(200, {}) : json(401, { error: { message: "fixture key required" } });
    case "/v1/models":
      return url.searchParams.get("output_modalities") === "decisions" ? models() : models(allowed);
    case "/v1/embeddings/models":
      return models(restricted);
    default:
      return json(404, { error: { message: "unknown fixture route" } });
  }
}).listen(port, "127.0.0.1", () => console.log(`OpenRouter fixture ready on 127.0.0.1:${port}`));
