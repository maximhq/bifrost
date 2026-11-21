import { expect, test } from "../../core/fixtures/base.fixture";

// The trace sheet in session scope, against mocked Agent history. Turn 1 carries an artifact; the session's
// summary pages hold turn 1, then a full page of GetTask polls on it (no new turn), then turn 2.
const CONTEXT = "ctx-trace-e2e";
const at = (seconds: number) => new Date(Date.UTC(2026, 10, 11, 10, 0, seconds)).toISOString();
const message = (text: string) => JSON.stringify({ message: { role: "ROLE_USER", parts: [{ text }] } });

const base = {
  record_kind: "request",
  status: "success",
  agent_name: "revops-copilot",
  context_id: CONTEXT,
  latency: 1000,
};
const turn1 = {
  ...base,
  id: "op-turn1",
  timestamp: at(0),
  operation: "SendMessage",
  request_id: "req-turn1",
  task_id: "task-1",
  caller_correlation_id: "cli-1",
  input: message("First question"),
};
const turn1Hydrated = {
  ...turn1,
  events: [
    {
      ...turn1,
      id: "ev-artifact",
      record_kind: "event",
      event_sequence: 1,
      event_type: "artifact_update",
      event_body: JSON.stringify({ artifactUpdate: { artifact: { artifactId: "art-report", name: "report.pdf", parts: [{ text: "Report body" }] } } }),
    },
    {
      ...turn1,
      id: "ev-done",
      record_kind: "event",
      event_sequence: 2,
      event_type: "status_update",
      event_body: JSON.stringify({ statusUpdate: { status: { state: "TASK_STATE_COMPLETED", message: { parts: [{ text: "First answer" }] } } } }),
    },
  ],
};
const poll = (n: number) => ({
  ...base,
  id: `op-poll-${n}`,
  timestamp: at(1 + n * 0.01),
  operation: "GetTask",
  request_id: `req-poll-${n}`,
  task_id: "task-1",
  caller_correlation_id: "cli-1",
});
const turn2 = {
  ...base,
  id: "op-turn2",
  timestamp: at(30),
  operation: "SendMessage",
  request_id: "req-turn2",
  task_id: "task-2",
  caller_correlation_id: "cli-2",
  input: message("Second question"),
};
const summaryPages: Record<number, unknown[]> = {
  0: [turn1, ...Array.from({ length: 49 }, (_, n) => poll(n))],
  50: Array.from({ length: 50 }, (_, n) => poll(49 + n)),
  100: [turn2],
};
const SUMMARY_TOTAL = 101;

const page200 = (logs: unknown[], params: URLSearchParams, total = logs.length) => ({
  logs,
  pagination: {
    limit: Number(params.get("limit") ?? 50),
    offset: Number(params.get("offset") ?? 0),
    sort_by: "timestamp",
    order: params.get("order") ?? "asc",
    total_count: total,
  },
});

test.describe("Agent trace session view", () => {
  let summaryOffsets: number[];

  test.beforeEach(async ({ page }) => {
    summaryOffsets = [];
    await page.route("**/api/agents/history**", async (route) => {
      const url = new URL(route.request().url());
      const params = url.searchParams;
      const path = url.pathname;
      if (path.endsWith("/stats"))
        return route.fulfill({ json: { total_entries: 1, success_count: 1, error_count: 0, success_rate: 100, average_latency: 0 } });
      if (path.endsWith("/histogram")) return route.fulfill({ json: { buckets: [], bucket_size_seconds: 60 } });
      if (path.endsWith("/filterdata") || path.endsWith("/top-agents")) return route.fulfill({ json: {} });
      if (path.endsWith("/history/op-turn1")) return route.fulfill({ json: params.get("hydrate") ? turn1Hydrated : turn1 });
      if (!path.endsWith("/history")) return route.continue();
      if (params.get("hydrate") === "true") {
        // A turn's detail: its own operations (hydrated) and the agents it delegated (none here).
        const taskId = params.get("task_id");
        const logs = taskId === "task-1" ? [turn1Hydrated] : taskId === "task-2" ? [{ ...turn2, events: [] }] : [];
        return route.fulfill({ json: page200(logs, params) });
      }
      if (params.get("context_id") === CONTEXT) {
        const offset = Number(params.get("offset") ?? 0);
        summaryOffsets.push(offset);
        return route.fulfill({ json: page200(summaryPages[offset] ?? [], params, SUMMARY_TOTAL) });
      }
      return route.fulfill({ json: page200([turn1], params) });
    });
    // The turns made no LLM or MCP calls.
    await page.route("**/api/logs?**", (route) => route.fulfill({ json: page200([], new URL(route.request().url()).searchParams) }));
    await page.route("**/api/mcp-logs?**", (route) => route.fulfill({ json: page200([], new URL(route.request().url()).searchParams) }));
    await page.goto(`/workspace/agent-logs?polling=false&period=30d&selected_log=${turn1.id}&agent_trace_scope=session`);
    await expect(page.getByTestId("agent-trace-view")).toBeVisible();
  });

  test("keeps paging the session after a page that adds no new turn", async () => {
    // Page 50 holds only polls on turn 1; the loader must still go on to page 100 without a click.
    await expect.poll(() => summaryOffsets, { timeout: 10_000 }).toContain(100);
  });

  test("opens an artifact's detail from Thread mode", async ({ page }) => {
    await page.getByRole("tab", { name: "Thread" }).click();
    await expect(page.getByTestId("agent-trace-conversation")).toBeVisible();
    await page.getByTestId("agent-trace-conversation").getByRole("button", { name: "report.pdf" }).click();
    await expect(page.getByTestId("agent-trace-artifact-detail")).toBeVisible();
    await expect(page.getByTestId("agent-trace-conversation")).toBeHidden();
  });

  test("shows the session's turns as a conversation with the agent's answer", async ({ page }) => {
    await page.getByRole("tab", { name: "Thread" }).click();
    const conversation = page.getByTestId("agent-trace-conversation");
    await expect(conversation.getByText("First question")).toBeVisible();
    await expect(conversation.getByText("First answer")).toBeVisible();
  });
});

// Trace scope: one root task, with each test shaping what the task's queries return.
const root = {
  ...base,
  id: "op-root",
  timestamp: at(0),
  operation: "SendMessage",
  request_id: "req-root",
  task_id: "task-root",
  caller_correlation_id: "cli-root",
  input: message("Root question"),
};
const rootHydrated = { ...root, events: [] };
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

type HydrateReply = { status?: number; logs?: unknown[]; total?: number };

async function openTrace(
  page: import("@playwright/test").Page,
  row: typeof root,
  hydrate: (params: URLSearchParams) => HydrateReply | Promise<HydrateReply>,
  onCall: (params: URLSearchParams) => void | Promise<void> = () => {},
) {
  await page.route("**/api/agents/history**", async (route) => {
    const url = new URL(route.request().url());
    const params = url.searchParams;
    const path = url.pathname;
    if (path.endsWith("/stats"))
      return route.fulfill({ json: { total_entries: 1, success_count: 1, error_count: 0, success_rate: 100, average_latency: 0 } });
    if (path.endsWith("/histogram")) return route.fulfill({ json: { buckets: [], bucket_size_seconds: 60 } });
    if (path.endsWith("/filterdata") || path.endsWith("/top-agents")) return route.fulfill({ json: {} });
    if (path.endsWith(`/history/${row.id}`)) return route.fulfill({ json: { ...row, events: [] } });
    if (!path.endsWith("/history")) return route.continue();
    if (params.get("hydrate") === "true") {
      const reply = await hydrate(params);
      if (reply.status && reply.status >= 400) return route.fulfill({ status: reply.status, json: { error: { message: "boom" } } });
      return route.fulfill({ json: page200(reply.logs ?? [], params, reply.total) });
    }
    return route.fulfill({ json: page200([row], params) });
  });
  for (const pattern of ["**/api/logs?**", "**/api/mcp-logs?**"]) {
    await page.route(pattern, async (route) => {
      const params = new URL(route.request().url()).searchParams;
      await onCall(params);
      return route.fulfill({ json: page200([], params) });
    });
  }
  await page.goto(`/workspace/agent-logs?polling=false&period=30d&selected_log=${row.id}`);
  await expect(page.getByTestId("agent-trace-view")).toBeVisible();
}

test.describe("Agent trace loading", () => {
  test("pages a task's operations past the first page", async ({ page }) => {
    const operations = [rootHydrated, ...Array.from({ length: 200 }, (_, n) => ({ ...poll(n), task_id: "task-root", events: [] }))];
    const offsets: number[] = [];
    await openTrace(page, root, (params) => {
      if (params.get("task_id") !== "task-root") return { logs: [] };
      const offset = Number(params.get("offset") ?? 0);
      offsets.push(offset);
      return { logs: operations.slice(offset, offset + Number(params.get("limit"))), total: operations.length };
    });
    // 201 operations do not fit one page of 200: the second page must be requested too.
    await expect.poll(() => offsets, { timeout: 10_000 }).toContain(200);
  });

  test("stops loading a trace once the sheet closes", async ({ page }) => {
    const children = ["c1", "c2", "c3"].map((c) => ({ ...root, id: `op-${c}`, task_id: `task-${c}`, caller_correlation_id: "task-root", events: [] }));
    let childStarted = false;
    let closed = false;
    let afterClose = 0;
    const childRequest = async (owner: string | null) => {
      if (!owner?.startsWith("task-c")) return;
      childStarted = true;
      if (closed) afterClose++;
      await sleep(1500); // slow enough that the children's requests queue behind the in-flight limit
    };
    await openTrace(
      page,
      root,
      async (params) => {
        const taskId = params.get("task_id");
        const caller = params.get("caller_correlation_id");
        if (caller === "task-root") return { logs: children };
        await childRequest(taskId ?? caller);
        if (taskId === "task-root") return { logs: [rootHydrated] };
        return { logs: children.filter((child) => child.task_id === taskId) };
      },
      (params) => childRequest(params.get("agent_correlation_id")),
    );
    await expect.poll(() => childStarted, { timeout: 10_000 }).toBe(true);
    await page.getByTestId("agent-trace-close").click();
    closed = true;
    await sleep(5000);
    // Requests still queued for the closed trace must not start.
    expect(afterClose).toBe(0);
  });

  test("refreshes a task that was still running when it loaded, then stops", async ({ page }) => {
    let loads = 0;
    await openTrace(page, { ...root, status: "processing" }, (params) => {
      if (params.get("task_id") !== "task-root") return { logs: [] };
      loads++;
      return { logs: [loads === 1 ? { ...rootHydrated, status: "processing" } : rootHydrated] };
    });
    await expect.poll(() => loads, { timeout: 15_000 }).toBeGreaterThanOrEqual(2);
    // Finished now: no further reloads.
    const settled = loads;
    await sleep(7000);
    expect(loads).toBe(settled);
  });

  test("retries a task whose load failed when it is selected again", async ({ page }) => {
    let loads = 0;
    await openTrace(page, root, (params) => {
      if (params.get("task_id") !== "task-root") return { logs: [] };
      loads++;
      return loads === 1 ? { status: 500 } : { logs: [rootHydrated] };
    });
    await expect.poll(() => loads, { timeout: 10_000 }).toBe(1);
    await page.getByTestId("agent-trace-node-agent").first().click();
    await expect.poll(() => loads, { timeout: 10_000 }).toBe(2);
  });
});
