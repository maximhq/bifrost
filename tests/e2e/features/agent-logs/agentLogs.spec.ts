import { expect, test } from "../../core/fixtures/base.fixture";

// History is mocked so grouping tests do not require creating gateway traffic.
const root = {
  id: "context-root",
  timestamp: "2026-10-12T00:00:00Z",
  record_kind: "request",
  operation: "SendMessage",
  status: "success",
  agent_name: "group-test-agent",
  request_id: "request-root",
  context_id: "context-group-test",
  input: "group root",
  session_child_count: 2,
};
const firstMember = {
  ...root,
  id: "context-member-1",
  request_id: "request-member-1",
  operation: "GetTask",
  input: "first member",
  session_child_count: 0,
};
const secondMember = {
  ...root,
  id: "context-member-2",
  request_id: "request-member-2",
  operation: "CancelTask",
  input: "second member",
  session_child_count: 0,
};

test.describe("Agent log context grouping", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/agents/history**", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname.endsWith("/stats")) {
        return route.fulfill({
          json: {
            total_entries: 3,
            success_count: 3,
            error_count: 0,
            success_rate: 100,
            average_latency: 0,
          },
        });
      }
      if (url.pathname.endsWith("/histogram"))
        return route.fulfill({ json: { buckets: [], bucket_size_seconds: 60 } });
      if (url.pathname.endsWith("/filterdata")) return route.fulfill({ json: {} });
      if (!url.pathname.endsWith("/history")) return route.continue();
      const params = url.searchParams;
      const offset = Number(params.get("offset") ?? 0);
      const grouped = params.get("group_sessions") === "true";
      const expanding = params.has("context_id");
      const logs = expanding
        ? offset === 0
          ? [root, firstMember]
          : [secondMember]
        : grouped
          ? [root]
          : [root, firstMember, secondMember];
      await route.fulfill({
        json: {
          logs,
          pagination: {
            limit: Number(params.get("limit")),
            offset,
            sort_by: "timestamp",
            order: params.get("order"),
            total_count: grouped ? 1 : 3,
          },
        },
      });
    });
    await page.goto("/workspace/agent-logs?polling=false&period=30d&status=success");
    await expect(page.getByTestId("agent-logs-header-group-btn")).toBeVisible();
  });

  test("toggles grouping, expands matching operations, and loads further members", async ({
    page,
  }) => {
    const group = page.getByTestId("agent-logs-header-group-btn");
    await expect(page.getByText("first member", { exact: true })).toBeVisible();
    await group.click();
    await expect(group).toHaveAttribute("aria-pressed", "true");
    await expect(page).toHaveURL(/grouped=true/);
    await expect(page.getByText("first member", { exact: true })).toBeHidden();
    await expect(page.getByText("3 ops", { exact: true })).toBeVisible();

    const expansionRequest = page.waitForRequest((request) =>
      new URL(request.url()).searchParams.has("context_id"),
    );
    await page.getByTestId("agent-logs-session-expand-btn").click();
    const params = new URL((await expansionRequest).url()).searchParams;
    expect(params.get("context_id")).toBe(root.context_id);
    expect(params.get("status")).toBe("success");
    expect(params.has("group_sessions")).toBe(false);
    await expect(page.getByText("first member", { exact: true })).toBeVisible();
    await expect(page.getByTestId("agent-log-row-kind-session")).toHaveText("operation 2");
    await expect(page.getByText("second member", { exact: true })).toBeHidden();
    await page.getByTestId("agent-logs-session-load-more-btn").click();
    await expect(page.getByText("second member", { exact: true })).toBeVisible();
    await expect(page.getByTestId("agent-logs-session-load-more-btn")).toBeHidden();

    await page.getByTestId("agent-logs-session-expand-btn").click();
    await expect(page.getByText("first member", { exact: true })).toBeHidden();
    await group.click();
    await expect(group).toHaveAttribute("aria-pressed", "false");
    await expect(page.getByText("first member", { exact: true })).toBeVisible();
  });
  test("refresh retains cached expanded members", async ({ page }) => {
    await page.getByTestId("agent-logs-header-group-btn").click();
    await page.getByTestId("agent-logs-session-expand-btn").click();
    await expect(page.getByText("first member", { exact: true })).toBeVisible();
    await page.route("**/api/agents/history?**", async route => {
      const params = new URL(route.request().url()).searchParams;
      if (!params.has("context_id")) return route.fallback();
      await route.fulfill({json: {
        logs: [root, {...secondMember, input: "fresh member"}],
        pagination: {limit: 100, offset: 0, sort_by: "timestamp", order: "asc", total_count: 2},
      }});
    });
    await page.getByTestId("agent-logs-header-refresh-btn").click();
    await expect(page.getByText("fresh member", {exact: true})).toBeHidden();
    await expect(page.getByText("first member", {exact: true})).toBeVisible();
    await page.getByTestId("agent-logs-session-expand-btn").click();
    await page.getByTestId("agent-logs-session-expand-btn").click();
    await expect(page.getByText("fresh member", {exact: true})).toBeHidden();
  });

});