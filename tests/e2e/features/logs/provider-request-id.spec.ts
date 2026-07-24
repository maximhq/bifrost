import type { Page, Route } from "@playwright/test";
import { expect, test } from "../../core/fixtures/base.fixture";

const providerRequestID = "req-provider-e2e-final";
const providerRequestIDTrail = [
  {
    attempt: 0,
    provider: "openai",
    request_id: "req-provider-e2e-rate-limited",
    header_name: "x-request-id",
    status_code: 429,
  },
  {
    attempt: 1,
    provider: "openai",
    request_id: providerRequestID,
    header_name: "x-request-id",
    status_code: 200,
  },
];

const baseLog = {
  id: "bf-provider-request-id-e2e",
  object: "chat.completion",
  timestamp: "2026-07-24T10:00:00Z",
  provider: "openai",
  model: "provider-id-target-model",
  number_of_retries: 1,
  fallback_index: 0,
  input_history: [],
  responses_input_history: [],
  status: "success",
  stream: false,
  created_at: "2026-07-24T10:00:00Z",
  provider_request_id: providerRequestID,
  provider_request_id_header: "x-request-id",
  provider_request_id_trail: providerRequestIDTrail,
};

const otherLog = {
  ...baseLog,
  id: "bf-provider-request-id-other",
  model: "unfiltered-model",
  provider_request_id: "req-provider-e2e-other",
  provider_request_id_trail: [],
};

const stats = {
  total_requests: 1,
  success_rate: 100,
  user_facing_success_rate: 100,
  user_facing_total_requests: 1,
  average_latency: 0,
  total_tokens: 0,
  prompt_tokens: 0,
  completion_tokens: 0,
  total_cost: 0,
};

async function installLogsRoutes(
  page: Page,
  filtered: boolean,
  observedListQueries: string[] = [],
) {
  await page.route("**/api/config**", async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ is_db_connected: true, is_logs_connected: true }),
    });
  });

  await page.route("**/api/logs**", async (route: Route) => {
    const url = new URL(route.request().url());

    if (url.pathname === "/api/logs/stats") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(stats),
      });
      return;
    }
    if (url.pathname === "/api/logs/histogram") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ buckets: [], bucket_size_seconds: 60 }),
      });
      return;
    }
    if (url.pathname === "/api/logs/filterdata") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ providers: ["openai"], models: [baseLog.model, otherLog.model] }),
      });
      return;
    }
    if (url.pathname === `/api/logs/${baseLog.id}`) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(baseLog),
      });
      return;
    }
    if (url.pathname === "/api/logs") {
      observedListQueries.push(url.search);
      const exactMatch = url.searchParams.get("provider_request_id") === providerRequestID;
      const logs = filtered ? (exactMatch ? [baseLog] : [otherLog]) : [baseLog];
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          logs,
          pagination: { limit: 25, offset: 0, sort_by: "timestamp", order: "desc", total_count: 1 },
          stats,
          has_logs: true,
        }),
      });
      return;
    }

    await route.continue();
  });
}

async function ensureFiltersVisible(page: Page) {
  const toggle = page.getByTestId("provider-request-id-filter-toggle");
  if (!(await toggle.isVisible().catch(() => false))) {
    await page.getByRole("button", { name: "Show filters" }).click();
  }
  await expect(toggle).toBeVisible();
}

test.describe("Provider Request ID logs UI", () => {
  test("updates the URL and exact logs API query, returns the matching log, and clears cleanly", async ({
    logsPage,
    page,
  }) => {
    const observedListQueries: string[] = [];
    await installLogsRoutes(page, true, observedListQueries);
    await logsPage.goto();
    await ensureFiltersVisible(page);

    await page.getByTestId("provider-request-id-filter-toggle").click();
    const input = page.getByTestId("provider-request-id-filter-input");
    await input.fill(providerRequestID);

    await expect
      .poll(() => new URL(page.url()).searchParams.get("provider_request_id"))
      .toBe(providerRequestID);
    await expect
      .poll(() =>
        observedListQueries.some(
          (query) => new URLSearchParams(query).get("provider_request_id") === providerRequestID,
        ),
      )
      .toBe(true);
    await expect(logsPage.logsTable.getByText(baseLog.model)).toBeVisible();
    await expect(logsPage.logsTable.getByText(otherLog.model)).not.toBeVisible();

    await input.fill("");
    await expect
      .poll(() => new URL(page.url()).searchParams.has("provider_request_id"))
      .toBe(false);
    await expect(logsPage.logsTable.getByText(otherLog.model)).toBeVisible();
    await expect(logsPage.logsTable.getByText(baseLog.model)).not.toBeVisible();
  });

  test("distinguishes Bifrost and provider IDs and renders a copyable retry trail", async ({
    logsPage,
    page,
  }) => {
    await installLogsRoutes(page, false);
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await logsPage.goto();
    await logsPage.tableRows.filter({ hasText: baseLog.model }).click();
    await expect(logsPage.logDetailSheet).toBeVisible();

    const sheet = logsPage.logDetailSheet;
    await expect(sheet.getByText("Bifrost Request ID")).toBeVisible();
    await expect(sheet.getByText(baseLog.id)).toBeVisible();
    await expect(sheet.getByText("Provider Request ID", { exact: true })).toBeVisible();
    await expect(sheet.getByText(providerRequestID)).toBeVisible();
    await expect(sheet.getByText("Provider Request ID Header")).toBeVisible();
    await expect(sheet.getByText("x-request-id")).toBeVisible();

    await sheet.getByRole("tab", { name: /Routing/ }).click();
    const trailTitle = sheet.getByText("Provider Request ID Trail (2 captured attempts)");
    await expect(trailTitle).toBeVisible();
    await expect(sheet.getByText("req-provider-e2e-rate-limited")).toBeVisible();
    await expect(sheet.getByRole("cell", { name: providerRequestID, exact: true })).toBeVisible();
    await expect(sheet.getByRole("cell", { name: "429", exact: true })).toBeVisible();

    await trailTitle.locator("..").getByRole("button").click();
    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toContain(providerRequestID);
  });
});