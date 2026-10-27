import type { APIRequestContext } from "@playwright/test";
import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createContentHiddenKeyPayload,
  createGrantedKeyPayload,
  createRelayLogsRelayPayload,
  createRelayLogsTeamPayload,
  createUngrantedKeyPayload,
  EXTRACT_COST,
  RELAY_LOGS_API,
  RELAYS_API,
  type RelayLogRow,
  type RelayUpstream,
  relayLogsRelayName,
  SEARCH_COST,
  SEARCH_QUERY_MARKER,
  startRelayUpstream,
  TEAMS_API,
  VK_API,
} from "./relay-logs.data";

// Relay Logs end to end: a relay pointed at a local upstream is called through virtual keys so the
// page has real rows (a priced call, a route override, a free GET, a 403 refusal and a
// content-hidden call; a call to a disabled route is refused before the hooks and writes no row),
// then every control of the page is driven against them.

/** Lists our relay's rows straight from the API, newest first. */
async function fetchRows(request: APIRequestContext, relayName: string): Promise<RelayLogRow[]> {
  const res = await request.get(`${RELAY_LOGS_API}?relay_names=${encodeURIComponent(relayName)}&limit=100`);
  expect(res.status(), await res.text()).toBe(200);
  return ((await res.json()).logs ?? []) as RelayLogRow[];
}

test("shows the empty state until a relay has been called, and the table afterwards", async ({
  relayLogsPage: page,
  request,
}) => {
  // Whether any relay row exists is server state shared with other specs, so both branches assert.
  const res = await request.get(`${RELAY_LOGS_API}?limit=1`);
  expect(res.status(), await res.text()).toBe(200);
  const hasLogs = (await res.json()).has_logs === true;

  await page.goto();
  if (hasLogs) {
    await expect(page.pageRoot).toBeVisible();
    await expect(page.emptyState).toHaveCount(0);
    await expect(page.pagination).toBeVisible();
  } else {
    await expect(page.emptyState).toBeVisible();
    await expect(page.emptyState).toContainText("Get Started with Relays");
    await expect(page.pageRoot).toHaveCount(0);
  }
});

// The Cost column prints a precise dollar amount, trailing zeros dropped ($0.033, not $0.0330).
const costText = (cost: number) => `$${parseFloat(cost.toFixed(6))}`;

test.describe("Relay Logs", () => {
  test.describe.configure({ mode: "serial" });

  const relayName = relayLogsRelayName();
  const vkIds: string[] = [];
  let upstream: RelayUpstream;
  let teamId = "";
  let grantedKey = "";
  let grantedKeyId = "";
  let grantedKeyName = "";
  let ungrantedKey = "";
  let hiddenKey = "";
  let hiddenKeyName = "";
  let rows: RelayLogRow[] = [];

  const rowOf = (predicate: (r: RelayLogRow) => boolean): RelayLogRow => {
    const row = rows.find(predicate);
    if (!row) throw new Error(`no relay log row matched: ${rows.map((r) => `${r.method} ${r.path} ${r.status_code}`).join(", ")}`);
    return row;
  };
  const searchRow = () => rowOf((r) => r.path.startsWith("/v1/search") && r.status_code === 200 && !r.content_hidden);
  const extractRow = () => rowOf((r) => r.path === "/v1/extract");
  const statusRow = () => rowOf((r) => r.method === "GET");
  const deniedRow = () => rowOf((r) => r.status_code === 403);
  const hiddenRow = () => rowOf((r) => r.content_hidden);

  test.beforeAll(async ({ request }) => {
    upstream = await startRelayUpstream();

    const created = await request.post(RELAYS_API, { data: createRelayLogsRelayPayload(relayName, upstream.url) });
    expect(created.status(), await created.text()).toBe(201);

    const team = await request.post(TEAMS_API, { data: createRelayLogsTeamPayload(`${relayName}-team`) });
    expect(team.ok(), await team.text()).toBeTruthy();
    teamId = (await team.json()).team.id;

    grantedKeyName = `${relayName}-granted`;
    const granted = await request.post(VK_API, { data: createGrantedKeyPayload(grantedKeyName, relayName, teamId) });
    expect(granted.ok(), await granted.text()).toBeTruthy();
    const grantedBody = await granted.json();
    grantedKey = grantedBody.virtual_key.value;
    grantedKeyId = grantedBody.virtual_key.id;
    vkIds.push(grantedKeyId);

    const ungranted = await request.post(VK_API, { data: createUngrantedKeyPayload(`${relayName}-ungranted`) });
    expect(ungranted.ok(), await ungranted.text()).toBeTruthy();
    const ungrantedBody = await ungranted.json();
    ungrantedKey = ungrantedBody.virtual_key.value;
    vkIds.push(ungrantedBody.virtual_key.id);

    hiddenKeyName = `${relayName}-hidden`;
    const hidden = await request.post(VK_API, { data: createContentHiddenKeyPayload(hiddenKeyName, relayName) });
    expect(hidden.ok(), await hidden.text()).toBeTruthy();
    const hiddenBody = await hidden.json();
    hiddenKey = hiddenBody.virtual_key.value;
    vkIds.push(hiddenBody.virtual_key.id);

    // The calls, oldest first. Each is awaited so the rows have distinct timestamps.
    const search = await request.post(`/relay/${relayName}/v1/search?q=1`, {
      headers: { "x-bf-vk": grantedKey, "Content-Type": "application/json" },
      data: { query: SEARCH_QUERY_MARKER },
    });
    expect(search.status(), await search.text()).toBe(200);

    const extract = await request.post(`/relay/${relayName}/v1/extract`, {
      headers: { "x-bf-vk": grantedKey, "Content-Type": "application/json" },
      data: { text: "e2e" },
    });
    expect(extract.status(), await extract.text()).toBe(200);

    const status = await request.get(`/relay/${relayName}/v1/status`, { headers: { "x-bf-vk": grantedKey } });
    expect(status.status(), await status.text()).toBe(200);

    const denied = await request.post(`/relay/${relayName}/v1/search`, {
      headers: { "x-bf-vk": ungrantedKey, "Content-Type": "application/json" },
      data: { query: "denied" },
    });
    expect(denied.status(), await denied.text()).toBe(403);

    const disabled = await request.post(`/relay/${relayName}/v1/disabled`, {
      headers: { "x-bf-vk": grantedKey, "Content-Type": "application/json" },
      data: {},
    });
    expect(disabled.status(), await disabled.text()).toBe(404);

    const hiddenCall = await request.post(`/relay/${relayName}/v1/search`, {
      headers: { "x-bf-vk": hiddenKey, "Content-Type": "application/json" },
      data: { query: SEARCH_QUERY_MARKER },
    });
    expect(hiddenCall.status(), await hiddenCall.text()).toBe(200);

    // The refused and the disabled call never reach the upstream.
    expect(upstream.seen).toHaveLength(4);

    // Every call that reaches the hooks gets a row, the refused one included; the disabled route
    // is refused by the relay before the hooks run, so it writes none (see the troubleshooting guide).
    await expect
      .poll(
        async () => {
          rows = await fetchRows(request, relayName);
          return rows.length;
        },
        { timeout: 30_000, message: "every call that reached the hooks gets a Relay Logs row" },
      )
      .toBe(5);
    expect(searchRow().cost).toBeCloseTo(SEARCH_COST, 9);
    expect(extractRow().cost).toBeCloseTo(EXTRACT_COST, 9);
    expect(statusRow().billable).toBe(false);
    expect(hiddenRow().content_hidden).toBe(true);
  });

  test.afterAll(async ({ request }) => {
    const remaining = await fetchRows(request, relayName).catch(() => [] as RelayLogRow[]);
    if (remaining.length > 0) {
      await request.delete(RELAY_LOGS_API, { data: { ids: remaining.map((r) => r.id) } }).catch(() => undefined);
    }
    for (const id of vkIds) await request.delete(`${VK_API}/${id}`).catch(() => undefined);
    if (teamId) await request.delete(`${TEAMS_API}/${teamId}`).catch(() => undefined);
    await request.delete(`${RELAYS_API}/${relayName}`).catch(() => undefined);
    await upstream.close();
  });

  test.afterEach(async ({ relayLogsPage: page }) => {
    await page.closeSheet().catch(() => undefined);
  });

  // ---------------------------------------------------------------------------------------------
  // Table
  // ---------------------------------------------------------------------------------------------

  test("lists one row per relay call with its route, status, cost and billing", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await expect(page.pageRoot).toBeVisible();
    await expect(page.emptyState).toHaveCount(0);

    const rowsOnPage = page.rowsFor(relayName);
    await expect(rowsOnPage).toHaveCount(5);
    await expect(page.pagination).toContainText("1-5 of 5");

    const search = rowsOnPage.filter({ hasText: "/v1/search?q=1" });
    await expect(search).toContainText("POST /v1/search?q=1");
    await expect(search).toContainText("Route /v1/search");
    await expect(search).toContainText(costText(SEARCH_COST));
    // The table shows the outcome as a status bar, as the LLM logs table does; the code is in the sheet.
    await expect(search.locator('[title="success"]')).toHaveCount(1);
    // The virtual key column is hidden by default, as on the LLM logs table: shown here to check
    // which key each call was served under.
    await expect(search).not.toContainText(grantedKeyName);
    await page.toggleColumn("virtual_key");
    await expect(search).toContainText(grantedKeyName);

    const extract = rowsOnPage.filter({ hasText: "/v1/extract" });
    await expect(extract).toContainText(costText(EXTRACT_COST));

    const status = rowsOnPage.filter({ hasText: "/v1/status" });
    await expect(status).toContainText("GET /v1/status");
    await expect(status).toContainText("not billable");

    await expect(rowsOnPage.filter({ hasText: hiddenKeyName })).toContainText("POST /v1/search");
    await expect(rowsOnPage.filter({ hasText: "-ungranted" }).locator('[title="error"]')).toHaveCount(1);

    // The stat cards aggregate the same filtered rows.
    await expect(page.statRequests).toContainText("5");
    await expect(page.statCost).toBeVisible();
  });

  test("searches by path and keeps the term in the URL", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await page.search("/v1/extract");

    await expect(page.rowsFor(relayName)).toHaveCount(1);
    await expect(page.rowsFor(relayName)).toContainText("/v1/extract");
    await expect(page.searchInput).toHaveValue("/v1/extract");

    await page.searchInput.fill("");
    await page.expectUrlParam("search", null);
    await expect(page.rowsFor(relayName)).toHaveCount(5);
  });

  test("switches the time period and keeps it in the URL", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await expect(page.pageRoot.locator("button#date")).toContainText("Last hour");

    await page.selectPeriod("Last 24 hours");
    await page.expectUrlParam("period", "24h");
    await expect(page.pageRoot.locator("button#date")).toContainText("Last 24 hours");
    // The rows were just created, so a wider window still lists all of them.
    await expect(page.rowsFor(relayName)).toHaveCount(5);
  });

  test("sorts by timestamp, latency and cost through the column headers", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    // Newest first by default: the content-hidden call was made last (the key column is shown to tell it apart).
    await page.toggleColumn("virtual_key");
    await expect(page.rowsFor(relayName).first()).toContainText(hiddenKeyName);

    // Time is the default (desc) sort, so one click flips it to ascending.
    await page.sortButton("Time").click();
    await page.expectUrlParam("order", "asc");
    await expect(page.rowsFor(relayName).first()).toContainText("/v1/search?q=1");

    await page.sortButton("Latency").click();
    await page.expectUrlParam("sort_by", "latency");
    await page.expectUrlParam("order", "asc");
    await page.sortButton("Latency").click();
    await page.expectUrlParam("order", "desc");

    await page.sortButton("Cost").click();
    await page.expectUrlParam("sort_by", "cost");
    await page.expectUrlParam("order", "asc");
    await page.sortButton("Cost").click();
    await page.expectUrlParam("order", "desc");
    // Descending cost lists the formula price (0.033) before the route override (0.032). Rows
    // without a cost sort differently per database, so only the relative order is checked.
    await expect
      .poll(async () => {
        const texts = await page.rowsFor(relayName).allTextContents();
        const search = texts.findIndex((t) => t.includes(costText(SEARCH_COST)));
        const extract = texts.findIndex((t) => t.includes(costText(EXTRACT_COST)));
        return search >= 0 && extract >= 0 && search < extract;
      })
      .toBe(true);
  });

  test("paginates with the prev and next buttons", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName, limit: "2", offset: "0" });
    await expect(page.rowsFor(relayName)).toHaveCount(2);
    await expect(page.pagination).toContainText("1-2 of 5");
    await expect(page.pagination).toContainText("of 3");
    await expect(page.prevPageBtn).toBeDisabled();
    await expect(page.nextPageBtn).toBeEnabled();

    await page.nextPageBtn.click();
    await page.expectUrlParam("offset", "2");
    await expect(page.pagination).toContainText("3-4 of 5");
    await expect(page.prevPageBtn).toBeEnabled();

    await page.nextPageBtn.click();
    await page.expectUrlParam("offset", "4");
    await expect(page.pagination).toContainText("5-5 of 5");
    await expect(page.nextPageBtn).toBeDisabled();

    await page.prevPageBtn.click();
    await page.expectUrlParam("offset", "2");
    await page.prevPageBtn.click();
    // Back on page 1 the default offset is dropped from the URL.
    await expect.poll(() => page.urlParam("offset") ?? "0").toBe("0");
    await expect(page.pagination).toContainText("1-2 of 5");
  });

  test("toggles live polling and offers a manual refresh while paused", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await expect(page.pageRoot).toContainText("Waiting for new relay calls");
    await expect(page.tableRefreshBtn).toHaveCount(0);

    await page.liveBtn.click();
    await page.expectUrlParam("polling", "false");
    await expect(page.tableRefreshBtn).toBeVisible();
    await expect(page.pageRoot).not.toContainText("Waiting for new relay calls");

    await page.tableRefreshBtn.click();
    await expect(page.rowsFor(relayName)).toHaveCount(5);

    await page.liveBtn.click();
    await page.expectUrlParam("polling", "true");
    await expect(page.pageRoot).toContainText("Waiting for new relay calls");
    await expect(page.tableRefreshBtn).toHaveCount(0);
  });

  test("hides, shows, pins and resets columns", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await expect(page.sortButton("Cost")).toBeVisible();

    await page.toggleColumn("cost");
    await expect.poll(() => page.urlParam("relay_cols") ?? "").toContain("cost:h");
    await expect(page.sortButton("Cost")).toHaveCount(0);

    await page.toggleColumn("cost");
    await expect.poll(() => page.urlParam("relay_cols") ?? "").not.toContain("cost:h");
    await expect(page.sortButton("Cost")).toBeVisible();

    // A hidden-by-default column can be shown the same way.
    await expect(page.headerCell("Team")).toHaveCount(0);
    await page.toggleColumn("team");
    await expect(page.headerCell("Team")).toBeVisible();
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/search?q=1" })).toContainText(`${relayName}-team`);

    await page.pickColumnAction("Relay", "Pin to left");
    await expect.poll(() => page.urlParam("relay_cols") ?? "").toContain("relay_name:l");

    await page.resetColumns();
    await expect.poll(() => page.urlParam("relay_cols") ?? "").not.toContain("relay_name:l");
    await expect(page.headerCell("Team")).toHaveCount(0);
    await expect(page.sortButton("Cost")).toBeVisible();
  });

  // ---------------------------------------------------------------------------------------------
  // Filter sidebar
  // ---------------------------------------------------------------------------------------------

  test("filters by status and resets every filter", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await expect(page.filterSidebar).toBeVisible();

    await page.page.getByTestId("relay-status-filter-checkbox-error").click();
    await expect.poll(() => page.urlParams("statuses")).toEqual(["error"]);
    await expect(page.rowsFor(relayName)).toHaveCount(1);
    await expect(page.rowsFor(relayName).filter({ hasText: "-ungranted" })).toHaveCount(1);

    await page.page.getByTestId("relay-status-filter-checkbox-success").click();
    await expect.poll(() => page.urlParams("statuses").slice().sort()).toEqual(["error", "success"]);
    await expect(page.rowsFor(relayName)).toHaveCount(5);

    await page.page.getByTestId("relay-status-filter-checkbox-cancelled").click();
    await expect.poll(() => page.urlParams("statuses")).toHaveLength(3);

    // Reset clears the statuses and the relay filter, but not the time window.
    await expect(page.filterResetBtn).toBeVisible();
    await page.filterResetBtn.click();
    await page.expectUrlParam("statuses", null);
    await page.expectUrlParam("relay_names", null);
    await expect(page.filterResetBtn).toHaveCount(0);
  });

  test("filters by relay name and clears the section", async ({ relayLogsPage: page }) => {
    await page.goto();
    // The Relay section is open by default; narrow its list to our relay before picking it.
    await page.filterSearch("relay-filter").fill(relayName);
    const checkbox = page.filterCheckbox("relay-filter", relayName);
    await expect(checkbox).toBeVisible();
    await checkbox.click();
    await expect.poll(() => page.urlParams("relay_names")).toEqual([relayName]);
    await expect(page.rowsFor(relayName)).toHaveCount(5);

    await expect(page.clearSection("relay_names")).toContainText("1 selected");
    await page.clearSection("relay_names").click();
    await page.expectUrlParam("relay_names", null);
    await expect(page.clearSection("relay_names")).toHaveCount(0);
  });

  test("filters by method, billing and kind", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });

    await page.openSection("relay-method-filter-toggle");
    await page.filterCheckbox("relay-method-filter", "GET").click();
    await expect.poll(() => page.urlParams("methods")).toEqual(["GET"]);
    await expect(page.rowsFor(relayName)).toHaveCount(1);
    await expect(page.rowsFor(relayName)).toContainText("GET /v1/status");
    await page.clearSection("methods").click();
    await page.expectUrlParam("methods", null);

    await page.openSection("relay-billing-filter-toggle");
    await page.page.getByTestId("relay-billing-filter-checkbox-not-billable").click();
    await page.expectUrlParam("billable", "false");
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/status" })).toHaveCount(1);
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/search?q=1" })).toHaveCount(0);
    await page.page.getByTestId("relay-billing-filter-checkbox-billable").click();
    await page.expectUrlParam("billable", "true");
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/search?q=1" })).toHaveCount(1);
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/status" })).toHaveCount(0);
    await page.page.getByTestId("relay-billing-filter-checkbox-billable").click();
    await page.expectUrlParam("billable", null);

    await page.openSection("relay-kind-filter-toggle");
    await page.page.getByTestId("relay-kind-filter-checkbox-call").click();
    await expect.poll(() => page.urlParams("kinds")).toEqual(["call"]);
    await expect(page.rowsFor(relayName)).toHaveCount(5);
    await page.page.getByTestId("relay-kind-filter-checkbox-job").click();
    await expect.poll(() => page.urlParams("kinds").slice().sort()).toEqual(["call", "job"]);
    await page.page.getByTestId("relay-kind-filter-checkbox-call").click();
    await expect.poll(() => page.urlParams("kinds")).toEqual(["job"]);
    // No job settlements were made, so the page is empty.
    await expect(page.noResultsRow).toBeVisible();
    await page.page.getByTestId("relay-kind-filter-checkbox-job").click();
    await page.expectUrlParam("kinds", null);
  });

  test("filters by a cost range committed on Enter or blur", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await page.openSection("relay-cost-filter-toggle");

    const min = page.page.getByTestId("relay-cost-filter-min");
    const max = page.page.getByTestId("relay-cost-filter-max");

    // Between the override (0.032) and the formula price (0.033): only the two search calls.
    await min.fill("0.0325");
    await min.press("Enter");
    await page.expectUrlParam("min_cost", "0.0325");
    await expect(page.rowsFor(relayName)).toHaveCount(2);
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/extract" })).toHaveCount(0);

    await max.fill("0.0325");
    await max.blur();
    await page.expectUrlParam("max_cost", "0.0325");
    await expect(page.noResultsRow).toBeVisible();

    await expect(page.clearSection("cost")).toBeVisible();
    await page.clearSection("cost").click();
    await page.expectUrlParam("min_cost", null);
    await page.expectUrlParam("max_cost", null);
    await expect(min).toHaveValue("");
    await expect(max).toHaveValue("");
    await expect(page.rowsFor(relayName)).toHaveCount(5);
  });

  test("filters by virtual key and by team", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });

    await page.openSection("relay-vk-filter-toggle");
    await expect(page.filterSearch("relay-vk-filter")).toBeVisible();
    await page.filterSearch("relay-vk-filter").fill(grantedKeyName);
    await page.filterCheckbox("relay-vk-filter", grantedKeyId).click();
    await expect.poll(() => page.urlParams("virtual_key_ids")).toEqual([grantedKeyId]);
    await expect(page.rowsFor(relayName).filter({ hasText: hiddenKeyName })).toHaveCount(0);
    await expect(page.rowsFor(relayName).filter({ hasText: "-ungranted" })).toHaveCount(0);
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/search?q=1" })).toHaveCount(1);
    await page.clearSection("virtual_key_ids").click();
    await page.expectUrlParam("virtual_key_ids", null);

    await page.openSection("relay-teams-filter-toggle");
    await page.filterCheckbox("relay-teams-filter", teamId).click();
    await expect.poll(() => page.urlParams("team_ids")).toEqual([teamId]);
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/search?q=1" })).toHaveCount(1);
    await expect(page.rowsFor(relayName).filter({ hasText: hiddenKeyName })).toHaveCount(0);
    await page.clearSection("team_ids").click();
    await page.expectUrlParam("team_ids", null);
    await expect(page.rowsFor(relayName)).toHaveCount(5);
  });

  test("filters by a custom user id and opens the app, customer, business unit and project sections", async ({
    relayLogsPage: page,
  }) => {
    await page.goto({ relay_names: relayName });

    await page.openSection("relay-user-filter-toggle");
    await page.filterSearch("relay-user-filter").fill("e2e-missing-user");
    await expect(page.filterAddCustom("relay-user-filter")).toBeVisible();
    await page.filterAddCustom("relay-user-filter").click();
    await expect.poll(() => page.urlParams("user_ids")).toEqual(["e2e-missing-user"]);
    await expect(page.noResultsRow).toBeVisible();
    await expect(page.filterCheckbox("relay-user-filter", "e2e-missing-user")).toBeVisible();
    await page.clearSection("user_ids").click();
    await page.expectUrlParam("user_ids", null);
    await expect(page.rowsFor(relayName)).toHaveCount(5);

    await page.openSection("relay-app-filter-toggle");
    await expect(page.page.getByTestId("relay-app-filter-toggle")).toHaveAttribute("data-state", "open");

    await page.openSection("relay-customers-filter-toggle");
    await expect(page.filterSearch("relay-customers-filter")).toBeVisible();
    await page.filterSearch("relay-customers-filter").fill("e2e-missing-customer");
    await page.filterAddCustom("relay-customers-filter").click();
    await expect.poll(() => page.urlParams("customer_ids")).toEqual(["e2e-missing-customer"]);
    await page.clearSection("customer_ids").click();
    await page.expectUrlParam("customer_ids", null);

    await page.openSection("relay-business-units-filter-toggle");
    await expect(page.filterSearch("relay-business-units-filter")).toBeVisible();
    await page.filterSearch("relay-business-units-filter").fill("e2e-missing-bu");
    await page.filterAddCustom("relay-business-units-filter").click();
    await expect.poll(() => page.urlParams("business_unit_ids")).toEqual(["e2e-missing-bu"]);
    await page.clearSection("business_unit_ids").click();
    await page.expectUrlParam("business_unit_ids", null);

    await page.openSection("relay-projects-filter-toggle");
    await expect(page.filterSearch("relay-projects-filter")).toBeVisible();
    await page.filterSearch("relay-projects-filter").fill("e2e-missing-project");
    await page.filterAddCustom("relay-projects-filter").click();
    await expect.poll(() => page.urlParams("project_ids")).toEqual(["e2e-missing-project"]);
    await page.clearSection("project_ids").click();
    await page.expectUrlParam("project_ids", null);
    await expect(page.rowsFor(relayName)).toHaveCount(5);
  });

  test("restores filters from the URL into the sidebar", async ({ relayLogsPage: page }) => {
    await page.goto({
      relay_names: relayName,
      statuses: ["success"],
      methods: ["POST"],
      billable: "true",
      kinds: ["call"],
      min_cost: "0.01",
      virtual_key_ids: [grantedKeyId],
    });

    await expect(page.page.getByTestId("relay-status-filter-checkbox-success").getByRole("checkbox")).toBeChecked();
    await expect(page.filterCheckbox("relay-filter", relayName).getByRole("checkbox")).toBeChecked();
    await expect(page.filterCheckbox("relay-method-filter", "POST").getByRole("checkbox")).toBeChecked();
    await expect(page.page.getByTestId("relay-billing-filter-checkbox-billable").getByRole("checkbox")).toBeChecked();
    await expect(page.page.getByTestId("relay-kind-filter-checkbox-call").getByRole("checkbox")).toBeChecked();
    await expect(page.page.getByTestId("relay-cost-filter-min")).toHaveValue("0.01");
    await expect(page.filterCheckbox("relay-vk-filter", grantedKeyId).getByRole("checkbox")).toBeChecked();
    await expect(page.filterResetBtn).toBeVisible();

    // Only the granted key's priced POSTs: search and extract.
    await expect(page.rowsFor(relayName)).toHaveCount(2);
  });

  // ---------------------------------------------------------------------------------------------
  // Detail sheet
  // ---------------------------------------------------------------------------------------------

  test("opens the detail sheet from a row with the hero card, timings, cost and bodies", async ({
    relayLogsPage: page,
  }) => {
    await page.goto({ relay_names: relayName });
    await page.openRow(relayName, "/v1/search?q=1");
    const row = searchRow();
    await page.expectUrlParam("selected_log", row.id);

    const sheet = page.sheet;
    await expect(sheet).toContainText("Request details");
    await expect(sheet).toContainText(relayName);
    await expect(sheet).toContainText("Call");
    await expect(sheet).toContainText("success");
    await expect(sheet).toContainText("200");
    await expect(sheet).toContainText("POST /v1/search");
    await expect(sheet).toContainText(row.request_id);
    await expect(sheet).toContainText(grantedKeyName);
    await expect(page.sheetCopyRequestIdBtn).toBeVisible();
    await expect(page.sheetCopyRouteBtn).toBeVisible();

    // Hero stats.
    for (const label of ["Latency", "Units", "Cost", "Bytes out"]) {
      await expect(sheet.getByText(label, { exact: true }).first()).toBeVisible();
    }
    await expect(sheet).toContainText("$0.033");
    await expect(sheet).toContainText("per unit");

    // Collapsed secondary detail.
    await expect(page.sheetMoreDetails).toBeVisible();
    await expect(page.sheetMoreDetails).not.toHaveAttribute("open", "");
    await expect(page.sheetMoreDetails).not.toContainText("Upstream Latency");
    await page.openMoreDetails();
    await expect(page.sheetMoreDetails).toContainText("Timings");
    await expect(page.sheetMoreDetails).toContainText("Start Timestamp");
    await expect(page.sheetMoreDetails).toContainText("Upstream Latency");
    await expect(page.sheetMoreDetails).toContainText("Bifrost Overhead");
    await expect(page.sheetCostBlock).toContainText("Charged");
    await expect(page.sheetCostBlock).toContainText("$0.033");
    await expect(page.sheetCostBlock).toContainText("Fee per call");
    await expect(page.sheetCostBlock).toContainText("$0.001");
    await expect(page.sheetCostBlock).toContainText("Price per unit");
    await expect(page.sheetCostBlock).toContainText("$0.008");
    await expect(page.sheetCostBlock).toContainText("Units billed");
    await expect(page.sheetCostBlock).toContainText("4");
    await expect(page.sheetMoreDetails).toContainText("Request Details");
    await expect(page.sheetMoreDetails).toContainText("Method");
    await expect(page.sheetMoreDetails).toContainText("Path");
    await expect(page.sheetMoreDetails).toContainText("q=1");
    await expect(page.sheetMoreDetails).toContainText("Matched route");
    await expect(page.sheetMoreDetails).toContainText("Billable");
    await expect(page.sheetMoreDetails).toContainText("Request Headers");
    await expect(page.sheetMoreDetails).toContainText("Response Headers");

    // Bodies as they crossed the wire.
    await expect(page.sheetRequestBody).toBeVisible();
    await expect(page.sheetRequestBody).toContainText(SEARCH_QUERY_MARKER);
    await expect(page.sheetResponseBody).toBeVisible();
    await expect(page.sheetResponseBody).toContainText("credits");
    await expect(page.sheetError).toHaveCount(0);
    await expect(page.sheetContentHidden).toHaveCount(0);

    await page.sheetCloseBtn.click();
    await expect(sheet).toBeHidden();
    await page.expectUrlParam("selected_log", null);
  });

  test("shows the governance refusal on the 403 row", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await page.openRow(relayName, "-ungranted");

    await expect(page.sheet).toContainText("error");
    await expect(page.sheet).toContainText("403");
    await expect(page.sheetError).toBeVisible();
    await expect(page.sheetError).toContainText("Error");
    await expect(page.sheetError).toContainText(`Relay '${relayName}' is not allowed for virtual key`);
    await expect(page.sheetError).toContainText(`${relayName}-ungranted`);
    // A refused call never reached the upstream, so there is nothing to charge.
    await expect(page.sheet).toContainText("not charged");
    await expect(page.sheetResponseBody).toHaveCount(0);
  });

  test("opens a row from the selected_log URL parameter", async ({ relayLogsPage: page }) => {
    const row = extractRow();
    await page.goto({ relay_names: relayName, selected_log: row.id });
    await expect(page.sheet).toBeVisible();
    await expect(page.sheet).toContainText("POST /v1/extract");
    await expect(page.sheet).toContainText(row.request_id);
    await expect(page.sheet).toContainText("$0.032");
  });

  test("steps through rows with prev and next, turning the page at the edge", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName, limit: "2", offset: "0" });
    // Page 1 holds the two newest rows: the content-hidden call, then the 403 refusal.
    await page.openRowAt(relayName, 0);
    await page.expectUrlParam("selected_log", hiddenRow().id);
    await expect(page.sheetPrevBtn).toBeDisabled();
    await expect(page.sheetNextBtn).toBeEnabled();

    await page.sheetNextBtn.click();
    await page.expectUrlParam("selected_log", deniedRow().id);
    await expect(page.sheet).toContainText("403");
    await expect(page.sheetPrevBtn).toBeEnabled();

    // Past the last row of the page: the next page is fetched and its first row selected.
    await page.sheetNextBtn.click();
    await page.expectUrlParam("offset", "2");
    await page.expectUrlParam("selected_log", statusRow().id);
    await expect(page.sheet).toBeVisible();
    await expect(page.sheet).toContainText("/v1/status");
    await expect(page.pagination).toContainText("3-4 of 5");

    // And back: the previous page's last row.
    await page.sheetPrevBtn.click();
    await expect.poll(() => page.urlParam("offset") ?? "0").toBe("0");
    await page.expectUrlParam("selected_log", deniedRow().id);
    await expect(page.sheet).toContainText("403");
    await expect(page.pagination).toContainText("1-2 of 5");

    // Keyboard navigation does the same (k / ArrowUp for prev, j / ArrowDown for next).
    await page.page.keyboard.press("k");
    await page.expectUrlParam("selected_log", hiddenRow().id);
    await page.page.keyboard.press("ArrowDown");
    await page.expectUrlParam("selected_log", deniedRow().id);
  });

  test("links to the virtual key's details and filters the list by team attribution", async ({
    relayLogsPage: page,
  }) => {
    await page.goto({ relay_names: relayName });
    await page.openRow(relayName, "/v1/search?q=1");
    await page.openMoreDetails();

    const teamLink = page.page.getByTestId("relay-log-team_ids-link");
    await expect(teamLink).toContainText(`${relayName}-team`);
    await teamLink.click();
    await expect.poll(() => page.urlParams("team_ids")).toEqual([teamId]);
    await expect(page.sheet).toBeHidden();
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/search?q=1" })).toHaveCount(1);
    await expect(page.rowsFor(relayName).filter({ hasText: hiddenKeyName })).toHaveCount(0);

    await page.openRow(relayName, "/v1/search?q=1");
    await page.openMoreDetails();
    await expect(page.sheetVirtualKeyLink).toContainText(grantedKeyName);
    await page.sheetVirtualKeyLink.click();
    await expect(page.page).toHaveURL(/\/workspace\/governance\/virtual-keys\?.*selected_vk=/);
    expect(new URL(page.page.url()).searchParams.get("selected_vk")).toBe(grantedKeyId);
    await expect(page.page.getByTestId("vk-details-relay-access")).toBeVisible({ timeout: 15000 });
    await expect(page.page.getByTestId(`vk-details-relay-grant-${relayName}`)).toBeVisible();
  });

  test("exports a row as JSON from the actions menu", async ({ relayLogsPage: page }) => {
    await page.goto({ relay_names: relayName });
    await page.openRow(relayName, "/v1/extract");
    const row = extractRow();

    await page.sheetActionsBtn.click();
    await expect(page.sheetExportBtn).toBeVisible();
    const download = page.page.waitForEvent("download");
    await page.sheetExportBtn.click();
    expect((await download).suggestedFilename()).toBe(`relay-log-${row.id}.json`);
    await expect(page.sheet).toBeVisible();
  });

  test("shows hidden content for a key with content logging off and bodies otherwise", async ({
    relayLogsPage: page,
  }) => {
    await page.goto({ relay_names: relayName });

    await page.openRow(relayName, hiddenKeyName);
    await expect(page.sheet).toContainText(hiddenKeyName);
    await expect(page.sheetContentHidden).toBeVisible();
    await expect(page.sheetContentHidden).toContainText("Content logging has been disabled");
    await expect(page.sheetRequestBody).toHaveCount(0);
    await expect(page.sheetResponseBody).toHaveCount(0);
    // The cost is still charged and shown; only the content is withheld.
    await expect(page.sheet).toContainText("$0.033");
    await page.openMoreDetails();
    await expect(page.sheetMoreDetails).not.toContainText("Request Headers");
    await page.closeSheet();

    await page.openRow(relayName, "/v1/search?q=1");
    await expect(page.sheetContentHidden).toHaveCount(0);
    await expect(page.sheetRequestBody).toContainText(SEARCH_QUERY_MARKER);
    await expect(page.sheetResponseBody).toContainText("credits");
  });

  // ---------------------------------------------------------------------------------------------
  // Deletion (last, since it removes rows)
  // ---------------------------------------------------------------------------------------------

  test("deletes a row from the sheet after confirming", async ({ relayLogsPage: page, request }) => {
    await page.goto({ relay_names: relayName });
    const row = statusRow();
    await page.openRow(relayName, "/v1/status");

    await page.sheetActionsBtn.click();
    await page.sheetDeleteBtn.click();
    await expect(page.sheetDeleteConfirmBtn).toBeVisible();
    await page.sheetDeleteCancelBtn.click();
    await expect(page.sheetDeleteConfirmBtn).toBeHidden();
    await expect(page.sheet).toBeVisible();
    expect((await request.get(`${RELAY_LOGS_API}/${row.id}`)).status()).toBe(200);

    await page.sheetActionsBtn.click();
    await page.sheetDeleteBtn.click();
    await page.sheetDeleteConfirmBtn.click();
    await expect(page.sheet).toBeHidden({ timeout: 10000 });
    await page.expectUrlParam("selected_log", null);
    await expect(page.rowsFor(relayName).filter({ hasText: "/v1/status" })).toHaveCount(0);
    await expect(page.rowsFor(relayName)).toHaveCount(4);
    await expect.poll(async () => (await request.get(`${RELAY_LOGS_API}/${row.id}`)).status()).toBe(404);
  });

  test("deletes a row from the table's row actions", async ({ relayLogsPage: page, request }) => {
    await page.goto({ relay_names: relayName });
    const row = deniedRow();
    await expect(page.rowsFor(relayName)).toHaveCount(4);

    await page.deleteRowViaActions(relayName, "-ungranted");
    await expect(page.rowsFor(relayName).filter({ hasText: "-ungranted" })).toHaveCount(0);
    await expect(page.rowsFor(relayName)).toHaveCount(3);
    await expect.poll(async () => (await request.get(`${RELAY_LOGS_API}/${row.id}`)).status()).toBe(404);
    await expect(page.sheet).toBeHidden();
  });
});
