import { randomUUID } from "node:crypto";
import { createServer } from "node:http";
import { isDeepStrictEqual } from "node:util";

const model = "fixture-video";
const required = {
  model,
  size: "3840x2160",
  seconds: "5",
  seed: 41,
  image_reference: [{ file_id: "ref-fixture" }],
  input_video: { job_id: "draft-fixture" },
  sampler: "fixture-three-step",
};

function json(res, code, body) {
  res.writeHead(code, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

const server = createServer(async (req, res) => {
  if (req.method === "GET" && req.url === "/v1/models") {
    json(res, 200, { object: "list", data: [{ id: model, object: "model" }] });
    return;
  }
  if (req.method !== "POST" || req.url !== "/v1/videos") {
    json(res, 404, { error: { message: "Unknown fixture route" } });
    return;
  }
  if (req.headers["content-type"] !== "application/json") {
    json(res, 415, { error: { message: "Expected application/json" } });
    return;
  }
  try {
    const chunks = [];
    let bytes = 0;
    for await (const chunk of req) {
      bytes += chunk.length;
      if (bytes > 16 * 1024) {
        json(res, 413, { error: { message: "Fixture payload is too large" } });
        return;
      }
      chunks.push(chunk);
    }
    const body = JSON.parse(Buffer.concat(chunks));
    const wrong = Object.keys(required).filter((key) => !isDeepStrictEqual(body[key], required[key]));
    if (wrong.length || "extra_params" in body) {
      json(res, 422, { error: { message: `Unexpected fixture fields: ${wrong.join(", ")}` } });
      return;
    }
    json(res, 202, {
      id: "video_fixture_" + randomUUID().replaceAll("-", ""),
      model,
      object: "video",
      status: "queued",
      size: body.size,
      seconds: body.seconds,
      created_at: Math.floor(Date.now() / 1000),
    });
  } catch {
    json(res, 400, { error: { message: "Fixture expects a JSON object" } });
  }
});

const port = Number(process.argv[2] ?? 9097);
server.listen(port, "127.0.0.1", () => {
  console.log(`OpenAI video JSON fixture on http://127.0.0.1:${server.address().port}`);
});
