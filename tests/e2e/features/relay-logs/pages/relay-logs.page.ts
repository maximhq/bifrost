import { Locator, Page, expect } from "@playwright/test";
import { BasePage } from "../../../core/pages/base.page";
import { waitForNetworkIdle } from "../../../core/utils/test-helpers";

/** The URL state keys the Relay Logs page keeps in the query string (nuqs). */
export type RelayLogsUrlKey =
  | "relay_names"
  | "methods"
  | "statuses"
  | "kinds"
  | "virtual_key_ids"
  | "user_ids"
  | "team_ids"
  | "customer_ids"
  | "business_unit_ids"
  | "project_ids"
  | "apps"
  | "min_cost"
  | "max_cost"
  | "billable"
  | "search"
  | "start_time"
  | "end_time"
  | "limit"
  | "offset"
  | "sort_by"
  | "order"
  | "polling"
  | "period"
  | "selected_log"
  | "relay_cols";

/** Prefixes of the searchable checkbox lists in the filter sidebar. */
export type RelayFilterListPrefix =
  | "relay-filter"
  | "relay-method-filter"
  | "relay-app-filter"
  | "relay-vk-filter"
  | "relay-user-filter"
  | "relay-teams-filter"
  | "relay-customers-filter"
  | "relay-business-units-filter"
  | "relay-projects-filter";

/** Section keys of the per-section "N selected" clear buttons (`relay-clear-<key>`). */
export type RelayFilterClearKey =
  | "relay_names"
  | "methods"
  | "apps"
  | "virtual_key_ids"
  | "user_ids"
  | "team_ids"
  | "customer_ids"
  | "business_unit_ids"
  | "project_ids"
  | "cost";

/** Normalizes a list key the way SearchableCheckboxList does when `normalizeTestIdKey` is on. */
export function normalizeFilterKey(key: string): string {
  return key
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/** Page object for /workspace/relay-logs. */
export class RelayLogsPage extends BasePage {
  readonly pageRoot: Locator;
  readonly emptyState: Locator;
  readonly filterSidebar: Locator;
  readonly filterResetBtn: Locator;
  readonly searchInput: Locator;
  readonly liveBtn: Locator;
  readonly headerRefreshBtn: Locator;
  readonly tableRefreshBtn: Locator;
  readonly columnConfigTrigger: Locator;
  readonly columnResetBtn: Locator;
  readonly pagination: Locator;
  readonly nextPageBtn: Locator;
  readonly prevPageBtn: Locator;
  readonly statRequests: Locator;
  readonly statCost: Locator;

  // Detail sheet
  readonly sheet: Locator;
  readonly sheetCloseBtn: Locator;
  readonly sheetActionsBtn: Locator;
  readonly sheetExportBtn: Locator;
  readonly sheetDeleteBtn: Locator;
  readonly sheetDeleteConfirmBtn: Locator;
  readonly sheetDeleteCancelBtn: Locator;
  readonly sheetMoreDetails: Locator;
  readonly sheetCostBlock: Locator;
  readonly sheetRequestBody: Locator;
  readonly sheetResponseBody: Locator;
  readonly sheetContentHidden: Locator;
  readonly sheetError: Locator;
  readonly sheetCopyRequestIdBtn: Locator;
  readonly sheetCopyRouteBtn: Locator;
  readonly sheetVirtualKeyLink: Locator;
  readonly sheetPrevBtn: Locator;
  readonly sheetNextBtn: Locator;

  constructor(page: Page) {
    super(page);
    this.pageRoot = page.getByTestId("relay-logs-page");
    this.emptyState = page.getByTestId("relay-logs-empty");
    this.filterSidebar = page.getByTestId("relay-filter-sidebar");
    this.filterResetBtn = page.getByTestId("relay-filter-reset-btn");
    this.searchInput = page.getByPlaceholder("Search path, route, model or request ID");
    this.liveBtn = page.getByTestId("relay-logs-header-live-btn");
    this.headerRefreshBtn = page.getByTestId("relay-logs-header-refresh-btn");
    this.tableRefreshBtn = page.getByTestId("relay-logs-table-refresh-btn");
    this.columnConfigTrigger = page.getByTestId("column-config-trigger");
    this.columnResetBtn = page.getByTestId("column-reset-default");
    this.pagination = page.getByTestId("pagination");
    this.nextPageBtn = page.getByTestId("next-page");
    this.prevPageBtn = page.getByTestId("prev-page");
    this.statRequests = page.getByTestId("relay-logs-stat-requests");
    this.statCost = page.getByTestId("relay-logs-stat-cost");

    this.sheet = page.getByTestId("relay-log-details");
    this.sheetCloseBtn = page.getByTestId("relay-log-close-button");
    this.sheetActionsBtn = page.getByTestId("relay-log-actions");
    this.sheetExportBtn = page.getByTestId("export-log-json");
    this.sheetDeleteBtn = page.getByTestId("relay-log-delete");
    this.sheetDeleteConfirmBtn = page.getByTestId("relay-log-delete-confirm");
    this.sheetDeleteCancelBtn = page.getByTestId("relay-log-delete-cancel");
    this.sheetMoreDetails = page.getByTestId("relay-log-more-details");
    this.sheetCostBlock = page.getByTestId("relay-log-cost");
    this.sheetRequestBody = page.getByTestId("relay-log-request-body");
    this.sheetResponseBody = page.getByTestId("relay-log-response-body");
    this.sheetContentHidden = page.getByTestId("relay-log-content-hidden");
    this.sheetError = page.getByTestId("relay-log-error");
    this.sheetCopyRequestIdBtn = page.getByTestId("relay-log-copy-request-id-button");
    this.sheetCopyRouteBtn = page.getByTestId("relay-log-copy-route-button");
    this.sheetVirtualKeyLink = page.getByTestId("relay-log-virtual-key-link");
    this.sheetPrevBtn = this.sheet.getByRole("button", { name: "Previous log" });
    this.sheetNextBtn = this.sheet.getByRole("button", { name: "Next log" });
  }

  /** Opens the page with the given query string state and waits for the table or the empty state. */
  async goto(params: Partial<Record<RelayLogsUrlKey, string | string[]>> = {}): Promise<void> {
    const search = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value === undefined) continue;
      for (const v of Array.isArray(value) ? value : [value]) search.append(key, v);
    }
    const qs = search.toString();
    await this.page.goto(`/workspace/relay-logs${qs ? `?${qs}` : ""}`);
    await this.pageRoot.or(this.emptyState).first().waitFor({ state: "visible", timeout: 15000 });
    await waitForNetworkIdle(this.page);
  }

  /** The data rows of one relay (the status row carries no relay name, so it is excluded). */
  rowsFor(relayName: string): Locator {
    return this.pageRoot.locator("tbody tr").filter({ hasText: relayName });
  }

  /** The "No results found" row the table shows for an empty page. */
  get noResultsRow(): Locator {
    return this.pageRoot.getByText("No results found", { exact: false });
  }

  /** A column header's sort button (Time, Latency and Cost are sortable). */
  sortButton(label: "Time" | "Latency" | "Cost"): Locator {
    return this.pageRoot.locator("thead").getByRole("button", { name: label, exact: true });
  }

  /** The header cell of a column, by its visible label. */
  headerCell(label: string): Locator {
    return this.pageRoot.locator("thead th").filter({ hasText: new RegExp(`^\\s*${label}\\s*$`) });
  }

  /** Opens the per-column menu of a header and picks an entry (Hide column, Pin to left, Pin to right, Unpin). */
  async pickColumnAction(label: string, action: "Hide column" | "Pin to left" | "Pin to right" | "Unpin"): Promise<void> {
    const th = this.headerCell(label);
    await th.hover();
    await th.getByRole("button", { name: "Column actions" }).click();
    await this.page.getByRole("menuitem", { name: action }).click();
  }

  /** Toggles a column in the header column-config popover. */
  async toggleColumn(columnId: string): Promise<void> {
    await this.columnConfigTrigger.click();
    const checkbox = this.page.getByTestId(`column-visibility-${columnId}`);
    await expect(checkbox).toBeVisible();
    await checkbox.click();
    await this.page.keyboard.press("Escape");
  }

  /** Resets the column config from the header popover. */
  async resetColumns(): Promise<void> {
    await this.columnConfigTrigger.click();
    await this.columnResetBtn.click();
    await this.page.keyboard.press("Escape");
  }

  /** Reads one query-string value from the current URL. */
  urlParam(key: RelayLogsUrlKey): string | null {
    return new URL(this.page.url()).searchParams.get(key);
  }

  /** Reads every value of a repeated query-string key. */
  urlParams(key: RelayLogsUrlKey): string[] {
    return new URL(this.page.url()).searchParams.getAll(key);
  }

  /** Waits until the URL carries `key=value` (or has no `key` when value is null). */
  async expectUrlParam(key: RelayLogsUrlKey, value: string | null, timeout = 10000): Promise<void> {
    await expect.poll(() => this.urlParam(key), { timeout }).toBe(value);
  }

  /** Types a search and waits for the debounced URL update. */
  async search(text: string): Promise<void> {
    await this.searchInput.fill(text);
    await this.expectUrlParam("search", text);
    await waitForNetworkIdle(this.page);
  }

  /** Picks a predefined period in the date range popover. */
  async selectPeriod(label: "Last hour" | "Last 6 hours" | "Last 24 hours" | "Last 7 days" | "Last 30 days"): Promise<void> {
    // DateTimePickerWithRange renders its trigger as the button with id "date" and the current period's label.
    const trigger = this.pageRoot.locator("button#date");
    await expect(trigger).toBeVisible();
    await trigger.click();
    const option = this.page.getByRole("button", { name: label, exact: true });
    await option.waitFor({ state: "visible", timeout: 5000 });
    await option.click();
    await this.page.keyboard.press("Escape");
    await waitForNetworkIdle(this.page);
  }

  /** Expands or collapses a filter sidebar section by its toggle testid. */
  async toggleSection(toggleTestId: string): Promise<void> {
    await this.page.getByTestId(toggleTestId).click();
  }

  /** Opens a section that is collapsed, leaving an open one alone. */
  async openSection(toggleTestId: string): Promise<void> {
    const toggle = this.page.getByTestId(toggleTestId);
    await expect(toggle).toBeVisible();
    if ((await toggle.getAttribute("data-state")) !== "open") {
      await toggle.click();
    }
    await expect(toggle).toHaveAttribute("data-state", "open");
  }

  /** The checkbox row of one key in a searchable filter list. */
  filterCheckbox(prefix: RelayFilterListPrefix, key: string): Locator {
    return this.page.getByTestId(`${prefix}-checkbox-${normalizeFilterKey(key)}`);
  }

  /** The search input of a searchable filter list. */
  filterSearch(prefix: RelayFilterListPrefix): Locator {
    return this.page.getByTestId(`${prefix}-search`);
  }

  /** The "Use ..." custom-value button of a filter list that allows custom ids. */
  filterAddCustom(prefix: RelayFilterListPrefix): Locator {
    return this.page.getByTestId(`${prefix}-add-custom`);
  }

  /** The per-section clear button. */
  clearSection(key: RelayFilterClearKey): Locator {
    return this.page.getByTestId(`relay-clear-${key}`);
  }

  /** Clicks a row that mentions the given text and waits for the sheet. */
  async openRow(relayName: string, rowText: string): Promise<void> {
    const row = this.rowsFor(relayName).filter({ hasText: rowText }).first();
    await expect(row).toBeVisible();
    // The relay badge sits in a plain cell whatever the column order; the actions cell swallows clicks.
    await row.getByText(relayName, { exact: true }).click();
    await expect(this.sheet).toBeVisible({ timeout: 10000 });
    await this.waitForSheetAnimation();
  }

  /** Opens the detail sheet of the row at `index` (0 = first data row). */
  async openRowAt(relayName: string, index: number): Promise<void> {
    const row = this.rowsFor(relayName).nth(index);
    await expect(row).toBeVisible();
    await row.getByText(relayName, { exact: true }).click();
    await expect(this.sheet).toBeVisible({ timeout: 10000 });
    await this.waitForSheetAnimation();
  }

  /** Closes the detail sheet through its close button. */
  async closeSheet(): Promise<void> {
    if (await this.sheet.isVisible().catch(() => false)) {
      await this.sheetCloseBtn.click();
      await expect(this.sheet).toBeHidden({ timeout: 5000 });
    }
  }

  /** Expands the "More details" disclosure in the open sheet. */
  async openMoreDetails(): Promise<void> {
    if ((await this.sheetMoreDetails.getAttribute("open")) === null) {
      await this.sheetMoreDetails.locator("summary").click();
    }
    await expect(this.sheetMoreDetails).toHaveAttribute("open", "");
  }

  /** Deletes a row through its row-level actions menu. */
  async deleteRowViaActions(relayName: string, rowText: string): Promise<void> {
    const row = this.rowsFor(relayName).filter({ hasText: rowText }).first();
    await expect(row).toBeVisible();
    await row.getByTestId("relay-log-actions-btn").click();
    await this.page.getByTestId("relay-log-delete-btn").click();
  }
}
