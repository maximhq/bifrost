import { createServer, type IncomingHttpHeaders, type Server } from "http";
import type { AddressInfo } from "net";
import { expect, test } from "../../core/fixtures/base.fixture";

// End-to-end relay and pricing: a local upstream stands in for a paid API, a relay
// points at it, and calls made with virtual keys are checked for access, credential handling, the
// formula cost on each Relay Logs row, and the charge on the key's budget.

const ENDPOINTS_API = "/api/relays";
const LOGS_API = "/api/relay-logs";
const VK_API = "/api/governance/virtual-keys";
const UPSTREAM_KEY = "sk-upstream-e2e";

// The endpoint pricing: $0.001 per call plus $0.008 per credit the upstream reports.
const FEE = 0.001;
const RATE = 0.008;

interface SeenRequest {
  method: string;
  path: string;
  headers: IncomingHttpHeaders;
}

let upstream: Server;
let upstreamURL: string;
const seen: SeenRequest[] = [];

function startUpstream(): Promise<void> {
  upstream = createServer((req, res) => {
    seen.push({ method: req.method ?? "", path: req.url ?? "", headers: req.headers });
    req.resume();
    req.on("end", () => {
      const path = (req.url ?? "").split("?")[0];
      if (path === "/v1/search") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ results: ["a", "b"], usage: { credits: 4 } }));
        return;
      }
      if (path === "/v1/extract") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ text: "x", usage: { credits: 2 } }));
        return;
      }
      if (path === "/v1/status") {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ ok: true }));
        return;
      }
      if (path === "/v1/stream") {
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        res.write(`data: ${JSON.stringify({ delta: "a" })}\n\n`);
        res.write(`data: ${JSON.stringify({ delta: "b", usage: { credits: 3 } })}\n\n`);
        res.end();
        return;
      }
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end(JSON.stringify({ error: "not found" }));
    });
  });
  return new Promise((resolve) => {
    upstream.listen(0, "127.0.0.1", () => {
      upstreamURL = `http://127.0.0.1:${(upstream.address() as AddressInfo).port}`;
      resolve();
    });
  });
}

test.describe("Relays relay and pricing", () => {
  test.describe.configure({ mode: "serial" });

  const name = `e2e-pte-relay-${Date.now()}`;
  const vkIds: string[] = [];
  let grantedKey = "";
  let grantedKeyId = "";
  let ungrantedKey = "";

  test.beforeAll(async ({ request }) => {
    await startUpstream();

    const created = await request.post(ENDPOINTS_API, {
      data: {
        name,
        description: "e2e relay and pricing",
        base_url: upstreamURL,
        auth: { type: "bearer" },
        credential: { value: { value: UPSTREAM_KEY } },
        usage: { cost_per_request: FEE, units_path: "usage.credits", cost_per_unit: RATE },
        routes: [
          { method: "POST", path: "/v1/search" },
          {
            method: "POST",
            path: "/v1/extract",
            usage: { cost_per_request: 0, cost_per_unit: 0.016 },
          },
          { method: "GET", path: "/v1/status" },
          { method: "POST", path: "/v1/stream", stream: true },
          { method: "POST", path: "/v1/disabled", enabled: false },
        ],
      },
    });
    expect(created.status(), await created.text()).toBe(201);

    const granted = await request.post(VK_API, {
      data: {
        name: `${name}-granted`,
        is_active: true,
        budgets: [{ max_limit: 100, reset_duration: "1M" }],
        relay_grants: [{ relay_name: name }],
      },
    });
    expect(granted.ok(), await granted.text()).toBeTruthy();
    const grantedBody = await granted.json();
    grantedKey = grantedBody.virtual_key.value;
    grantedKeyId = grantedBody.virtual_key.id;
    vkIds.push(grantedKeyId);

    const ungranted = await request.post(VK_API, {
      data: { name: `${name}-ungranted`, is_active: true },
    });
    expect(ungranted.ok(), await ungranted.text()).toBeTruthy();
    const ungrantedBody = await ungranted.json();
    ungrantedKey = ungrantedBody.virtual_key.value;
    vkIds.push(ungrantedBody.virtual_key.id);
  });

  test.afterAll(async ({ request }) => {
    for (const id of vkIds) await request.delete(`${VK_API}/${id}`).catch(() => undefined);
    await request.delete(`${ENDPOINTS_API}/${name}`).catch(() => undefined);
    await new Promise<void>((resolve) => upstream.close(() => resolve()));
  });

  test("relays a granted call with the endpoint credential, never the caller's", async ({
    request,
  }) => {
    seen.length = 0;
    const res = await request.post(`/relay/${name}/v1/search?q=1`, {
      headers: {
        "x-bf-vk": grantedKey,
        Authorization: "Bearer caller-token",
        "Content-Type": "application/json",
      },
      data: { query: "bifrost" },
    });
    expect(res.status(), await res.text()).toBe(200);
    expect(await res.json()).toEqual({ results: ["a", "b"], usage: { credits: 4 } });

    expect(seen).toHaveLength(1);
    expect(seen[0].method).toBe("POST");
    expect(seen[0].path).toBe("/v1/search?q=1");
    expect(seen[0].headers.authorization).toBe(`Bearer ${UPSTREAM_KEY}`);
    expect(seen[0].headers["x-bf-vk"]).toBeUndefined();
  });

  test("refuses keys without a grant, disabled routes and unlisted paths", async ({ request }) => {
    seen.length = 0;
    const denied = await request.post(`/relay/${name}/v1/search`, {
      headers: { "x-bf-vk": ungrantedKey },
      data: {},
    });
    expect(denied.status(), await denied.text()).toBe(403);

    const disabled = await request.post(`/relay/${name}/v1/disabled`, {
      headers: { "x-bf-vk": grantedKey },
      data: {},
    });
    expect(disabled.status(), await disabled.text()).toBe(404);

    const unlisted = await request.post(`/relay/${name}/v1/crawl`, {
      headers: { "x-bf-vk": grantedKey },
      data: {},
    });
    expect(unlisted.status(), await unlisted.text()).toBe(404);

    expect(seen, "a refused call never reaches the upstream").toHaveLength(0);
  });

  test("prices each call with the endpoint formula and route overrides", async ({ request }) => {
    const extract = await request.post(`/relay/${name}/v1/extract`, {
      headers: { "x-bf-vk": grantedKey },
      data: {},
    });
    expect(extract.status(), await extract.text()).toBe(200);

    const status = await request.get(`/relay/${name}/v1/status`, {
      headers: { "x-bf-vk": grantedKey },
    });
    expect(status.status(), await status.text()).toBe(200);

    const stream = await request.post(`/relay/${name}/v1/stream`, {
      headers: { "x-bf-vk": grantedKey, Accept: "text/event-stream" },
      data: {},
    });
    expect(stream.status(), await stream.text()).toBe(200);
    expect(await stream.text()).toContain('"credits":3');

    type Row = {
      route_path?: string;
      cost?: number;
      billable: boolean;
      streamed: boolean;
      status_code: number;
      virtual_key_id?: string;
    };
    let rows: Row[] = [];
    await expect
      .poll(
        async () => {
          const res = await request.get(
            `${LOGS_API}?relay_names=${encodeURIComponent(name)}&limit=50`,
          );
          expect(res.status(), await res.text()).toBe(200);
          // The refused call is logged too; pricing is read from the relayed ones.
          rows = ((await res.json()).logs ?? []).filter((r: Row) => r.status_code === 200);
          return rows.length;
        },
        { timeout: 30_000, message: "every relayed call gets a Relay Logs row" },
      )
      .toBeGreaterThanOrEqual(4);

    const byRoute = (route: string) => rows.find((r) => r.route_path === route);
    // /v1/search: fee + 4 credits x rate.
    expect(byRoute("/v1/search")?.cost).toBeCloseTo(FEE + 4 * RATE, 9);
    // /v1/extract: the route waives the fee and doubles the rate, inheriting the units path.
    expect(byRoute("/v1/extract")?.cost).toBeCloseTo(2 * 0.016, 9);
    // /v1/status: a GET is not billable by default, so it is relayed but not charged.
    expect(byRoute("/v1/status")?.billable).toBe(false);
    expect(byRoute("/v1/status")?.cost ?? 0).toBe(0);
    // /v1/stream: usage read from the last SSE event that carries it.
    expect(byRoute("/v1/stream")?.streamed).toBe(true);
    expect(byRoute("/v1/stream")?.cost).toBeCloseTo(FEE + 3 * RATE, 9);
    for (const r of rows) expect(r.virtual_key_id).toBe(grantedKeyId);
  });

  test("charges the formula cost to the virtual key budget", async ({ request }) => {
    const expected = FEE + 4 * RATE + 2 * 0.016 + FEE + 3 * RATE;
    await expect
      .poll(
        async () => {
          const res = await request.get(`${VK_API}/${grantedKeyId}`);
          expect(res.status(), await res.text()).toBe(200);
          const vk = (await res.json()).virtual_key;
          const budgets: { current_usage?: number }[] =
            vk?.budgets ?? (vk?.budget ? [vk.budget] : []);
          return budgets.reduce((sum, b) => sum + (b.current_usage ?? 0), 0);
        },
        { timeout: 30_000, message: "the budget usage reaches the summed formula cost" },
      )
      .toBeCloseTo(expected, 6);
  });

  test("shows the priced calls on the Relay Logs page", async ({ page }) => {
    await page.goto(`/workspace/relay-logs?relay_names=${encodeURIComponent(name)}`);
    await expect(page.getByTestId("relay-logs-page")).toBeVisible();
    await expect(page.getByText(name).first()).toBeVisible();
    // The cost column renders the shared cost cell: a dollar sign and the value without trailing zeros.
    await expect(page.getByText(`$${parseFloat((FEE + 4 * RATE).toFixed(6))}`).first()).toBeVisible();
  });
});