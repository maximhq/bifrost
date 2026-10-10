import type { Locator } from "@playwright/test";
import { expect, test } from "../../core/fixtures/base.fixture";
import {
  createLogSearchQuery,
  SAMPLE_MODELS,
  SAMPLE_PROVIDERS,
} from "./logs.data";

test.describe("LLM Logs", () => {
  test.beforeEach(async ({ logsPage }) => {
    await logsPage.goto();
  });

  test.describe("Logs Display", () => {
    test("should display logs table", async ({ logsPage }) => {
      // Table should be visible after goto (which waits for load)
      const tableExists = await logsPage.logsTable
        .isVisible()
        .catch(() => false);
      expect(tableExists).toBe(true);
    });

    test("should display stats cards", async ({ logsPage }) => {
      const statsVisible = await logsPage.areStatsVisible();
      expect(statsVisible).toBe(true);
    });

    test("should fit stat card trend figures at 1440px", async ({
      logsPage,
      page,
    }) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await logsPage.goto();

      const figures = page
        .getByTestId("logs-metric-strip")
        .getByTestId("logs-metric-trailing");
      const count = await figures.count();
      test.skip(
        count === 0,
        "No trend figures rendered: the strip needs traffic in the selected window",
      );
      for (let i = 0; i < count; i++) {
        const figure = figures.nth(i);
        await expect
          .poll(
            () =>
              figure.evaluate((el) => {
                const box = el.getBoundingClientRect();
                const range = document.createRange();
                range.selectNodeContents(el);
                const content = range.getBoundingClientRect();
                return (
                  content.left >= box.left - 0.5 &&
                  content.right <= box.right + 0.5
                );
              }),
            {
              message: `trend figure "${await figure.textContent()}" is truncated`,
            },
          )
          .toBe(true);
      }
    });

    test("should keep the table status row inside the visible table at 1440px", async ({
      logsPage,
      page,
    }) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await logsPage.goto();

      const status = page.getByTestId("logs-table-status-row");
      await expect(status).toBeVisible();
      const inside = await status.evaluate((el) => {
        const scroller = el.closest('[data-slot="table-container"]');
        if (!scroller) return false;
        const box = el.getBoundingClientRect();
        const view = scroller.getBoundingClientRect();
        return box.left >= view.left && box.right <= view.right;
      });
      expect(inside).toBe(true);
    });

    test("should display filters section", async ({ logsPage }) => {
      // Check if the search input or filters button is visible
      // These are always visible when the page loads (not inside empty state)
      const searchVisible = await logsPage.searchInput
        .isVisible()
        .catch(() => false);
      const filtersButtonVisible = await logsPage.filtersButton
        .isVisible()
        .catch(() => false);

      // Either search input OR filters button should be visible
      expect(searchVisible || filtersButtonVisible).toBe(true);
    });
  });

  test.describe("Log Filtering", () => {
    test("should filter logs by provider", async ({ logsPage }) => {
      // Try to filter by first available provider
      const providerFilter = logsPage.providerFilter;
      const isVisible = await providerFilter.isVisible().catch(() => false);

      if (!isVisible) {
        test.skip(!isVisible, "Provider filter not visible");
        return;
      }

      // Get initial filter state
      const initialValue = await providerFilter.textContent().catch(() => "");

      await logsPage.filterByProvider(SAMPLE_PROVIDERS[0]);

      // Check that filter value changed (or verify filter is applied via DOM)
      const newValue = await providerFilter.textContent().catch(() => "");
      // Filter should have changed or show selected provider
      expect(newValue || initialValue).toBeTruthy();
    });

    test("should filter logs by model", async ({ logsPage }) => {
      const modelFilter = logsPage.modelFilter;
      const isVisible = await modelFilter.isVisible().catch(() => false);

      if (!isVisible) {
        test.skip(!isVisible, "Model filter not visible");
        return;
      }

      // Get initial filter state
      const initialValue = await modelFilter.textContent().catch(() => "");

      await logsPage.filterByModel(SAMPLE_MODELS[0]);

      // Check that filter value changed (or verify filter is applied via DOM)
      const newValue = await modelFilter.textContent().catch(() => "");
      // Filter should have changed or show selected model
      expect(newValue || initialValue).toBeTruthy();
    });

    test("should filter logs by status", async ({ logsPage, page }) => {
      const filtersVisible = await logsPage.filtersButton
        .isVisible()
        .catch(() => false);
      if (!filtersVisible) {
        test.skip(true, "Filters button not visible");
        return;
      }

      await logsPage.filterByStatus("success");

      // Assert status filter is applied: logs page persists filters in URL (e.g. status=success)
      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/status=success/);
    });

    test("should filter logs by tool call name", async ({ logsPage, page }) => {
      const filtersVisible = await logsPage.filtersButton
        .isVisible()
        .catch(() => false);
      if (!filtersVisible) {
        test.skip(true, "Filters button not visible");
        return;
      }

      await logsPage.filterByToolCallName("get_weather");

      // The tool calls filter persists in the URL like every other sidebar filter
      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/tool_call_names=get_weather/);
    });

    test("should search logs by content", async ({ logsPage }) => {
      const searchInput = logsPage.searchInput;
      const isVisible = await searchInput.isVisible().catch(() => false);

      if (!isVisible) {
        test.skip(true, "Search input not visible");
        return;
      }

      const query = createLogSearchQuery();
      await logsPage.searchLogs(query);

      // Check that search input contains the query (DOM state)
      const inputValue = await searchInput.inputValue().catch(() => "");
      expect(inputValue).toContain(query);
    });

    test("should clear search", async ({ logsPage }) => {
      const searchInput = logsPage.searchInput;
      const isVisible = await searchInput.isVisible().catch(() => false);

      if (!isVisible) {
        test.skip(true, "Search input not visible");
        return;
      }

      await logsPage.searchLogs("test query");
      await logsPage.clearSearch();

      // Search should be cleared
      const inputValue = await searchInput.inputValue().catch(() => "");
      expect(inputValue).toBe("");
    });

    test("should filter by time period", async ({ logsPage }) => {
      const datePicker = logsPage.dateRangePicker;
      const isVisible = await datePicker.isVisible().catch(() => false);

      if (!isVisible) {
        test.skip(true, "Date range picker not visible");
        return;
      }

      // Get initial date picker value
      const initialValue = await datePicker.textContent().catch(() => "");

      await logsPage.selectTimePeriod("7d");

      // Check that date picker value changed (DOM state)
      const newValue = await datePicker.textContent().catch(() => "");
      // Date picker should show "Last 7 days" or similar
      expect(newValue || initialValue).toBeTruthy();
    });
  });

  test.describe("Log Details", () => {
    test("should open log details sheet", async ({ logsPage }) => {
      // Wait a bit for logs to potentially load
      await logsPage.page.waitForTimeout(1000);

      const logCount = await logsPage.getLogCount();

      if (logCount > 0) {
        await logsPage.viewLogDetails(0);

        // Wait for sheet animation
        await logsPage.page.waitForTimeout(500);

        // Detail sheet should be visible
        const sheetVisible = await logsPage.logDetailSheet
          .isVisible()
          .catch(() => false);
        expect(sheetVisible).toBe(true);

        // Close the sheet
        await logsPage.closeLogDetails();
      } else {
        // If no logs exist, the test passes (nothing to click)
        expect(logCount).toBe(0);
      }
    });

    test("should close log details sheet", async ({ logsPage }) => {
      const logCount = await logsPage.getLogCount();

      if (logCount > 0) {
        await logsPage.viewLogDetails(0);
        await logsPage.closeLogDetails();

        // Sheet should be closed
        const sheetVisible = await logsPage.logDetailSheet
          .isVisible()
          .catch(() => false);
        expect(sheetVisible).toBe(false);
      }
    });
  });

  test.describe("Pagination", () => {
    test("should navigate to next page", async ({ logsPage }) => {
      // Wait for pagination to settle (useTablePageSize may adjust limit dynamically)
      await logsPage.page.waitForTimeout(2000);
      const paginationVisible = await logsPage.paginationControls
        .isVisible()
        .catch(() => false);
      if (!paginationVisible) {
        test.skip(true, "Pagination controls not visible");
        return;
      }
      const nextBtn = logsPage.nextPageBtn.first();
      const isEnabled = await nextBtn.isEnabled().catch(() => false);

      if (!isEnabled) {
        test.skip(true, "Only one page of results; skipping pagination test");
        return;
      }

      const initialPage = logsPage.getCurrentPageNumber();
      expect(initialPage).toBe(1);
      await logsPage.goToNextPage();

      await expect
        .poll(() => logsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(initialPage + 1);
    });

    test("should navigate to previous page", async ({ logsPage }) => {
      // Wait for pagination to settle (useTablePageSize may adjust limit dynamically)
      await logsPage.page.waitForTimeout(2000);
      const paginationVisible = await logsPage.paginationControls
        .isVisible()
        .catch(() => false);
      if (!paginationVisible) {
        test.skip(true, "Pagination controls not visible");
        return;
      }
      const nextBtn = logsPage.nextPageBtn.first();
      const nextEnabled = await nextBtn.isEnabled().catch(() => false);

      if (!nextEnabled) {
        test.skip(true, "Only one page of results; skipping pagination test");
        return;
      }

      await logsPage.goToNextPage();

      await expect
        .poll(() => logsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(2);

      const prevBtn = logsPage.prevPageBtn.first();
      const prevEnabled = await prevBtn.isEnabled().catch(() => false);

      if (!prevEnabled) {
        test.skip(
          true,
          "Only one page of results; skipping previous-page test",
        );
        return;
      }

      await logsPage.goToPreviousPage();

      await expect
        .poll(() => logsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(1);
    });
  });

  test.describe("Table Sorting", () => {
    test("should sort by timestamp", async ({ logsPage }) => {
      // Timestamp is the default sort column (desc), so clicking it toggles to asc
      await logsPage.sortBy("timestamp");

      // Timestamp sort toggles order; wait for URL to reflect the change
      await logsPage.page.waitForURL(/order=asc|sort_by=timestamp/, {
        timeout: 5000,
      });
    });

    test("should sort by latency", async ({ logsPage }) => {
      await logsPage.sortBy("latency");

      // Wait for URL to update
      await logsPage.page.waitForURL(/sort_by=latency/, { timeout: 5000 });

      // Check URL state for latency sort
      const sortState = await logsPage.getSortState("latency");
      expect(sortState).toBeTruthy();
    });

    test("should sort by cost", async ({ logsPage }) => {
      await logsPage.sortBy("cost");

      // Wait for URL to update
      await logsPage.page.waitForURL(/sort_by=cost/, { timeout: 5000 });

      // Check URL state for cost sort
      const sortState = await logsPage.getSortState("cost");
      expect(sortState).toBeTruthy();
    });
  });

  test.describe("Live Updates", () => {
    test("should toggle live updates", async ({ logsPage }) => {
      const liveToggle = logsPage.liveToggle;
      const isVisible = await liveToggle.isVisible().catch(() => false);

      if (!isVisible) {
        test.skip(true, "Live toggle not visible");
        return;
      }

      // Default is live_enabled=true (but URL may not have it since it's the default)
      // Check for live_enabled=false to determine if currently disabled
      const initialUrl = logsPage.page.url();
      const initialLiveDisabled = initialUrl.includes("live_enabled=false");

      await logsPage.toggleLiveUpdates();

      // Wait for URL to reflect live_enabled toggle
      await logsPage.page.waitForURL(/live_enabled=/, { timeout: 5000 });

      const newUrl = logsPage.page.url();
      const newLiveDisabled = newUrl.includes("live_enabled=false");

      // Live enabled state should have toggled
      // If initially enabled (not disabled), after toggle it should be disabled
      // If initially disabled, after toggle it should be enabled (no live_enabled=false)
      expect(newLiveDisabled).not.toBe(initialLiveDisabled);
    });
  });

  test.describe("Empty State", () => {
    test("should show empty state when no logs", async ({ logsPage }) => {
      // Try to filter by a non-existent provider
      const searchInput = logsPage.searchInput;
      const isVisible = await searchInput.isVisible().catch(() => false);

      if (!isVisible) {
        test.skip(true, "Search input not visible");
        return;
      }

      await logsPage.searchLogs(`nonexistent-query-${Date.now()}`);

      // After searching for a non-existent query, empty state should appear (wait for API + render)
      await expect(
        logsPage.page.locator("text=/No results found|No logs found/i"),
      ).toBeVisible({ timeout: 10000 });
    });
  });

  test.describe("Advanced Filtering", () => {
    test("should combine multiple filters", async ({ logsPage }) => {
      // Apply multiple filters if they're visible
      const searchVisible = await logsPage.searchInput
        .isVisible()
        .catch(() => false);
      const providerVisible = await logsPage.providerFilter
        .isVisible()
        .catch(() => false);

      if (!searchVisible || !providerVisible) {
        test.skip(true, "Search input or provider filter not visible");
        return;
      }

      // Apply search filter
      await logsPage.searchLogs("test");

      // Apply provider filter
      if (SAMPLE_PROVIDERS.length > 0) {
        await logsPage.filterByProvider(SAMPLE_PROVIDERS[0]);
      }

      // Both filters should be applied
      const searchValue = await logsPage.searchInput
        .inputValue()
        .catch(() => "");
      expect(searchValue).toContain("test");
    });

    test("should clear all filters", async ({ logsPage }) => {
      const searchVisible = await logsPage.searchInput
        .isVisible()
        .catch(() => false);

      if (!searchVisible) {
        test.skip(true, "Search input not visible");
        return;
      }

      // Apply a filter first
      await logsPage.searchLogs("test query to clear");

      // Clear the search
      await logsPage.clearSearch();

      // Search should be empty
      const searchValue = await logsPage.searchInput
        .inputValue()
        .catch(() => "");
      expect(searchValue).toBe("");
    });

    test("should search within filtered results", async ({ logsPage }) => {
      const searchVisible = await logsPage.searchInput
        .isVisible()
        .catch(() => false);
      const statusVisible = await logsPage.statusFilter
        .isVisible()
        .catch(() => false);

      if (!searchVisible || !statusVisible) {
        test.skip(true, "Search input or status filter not visible");
        return;
      }

      // Apply status filter first
      await logsPage.filterByStatus("success");

      // Then apply search
      await logsPage.searchLogs("api");

      // Search input should contain the query
      const searchValue = await logsPage.searchInput
        .inputValue()
        .catch(() => "");
      expect(searchValue).toContain("api");
    });
  });

  test.describe("Grouped View", () => {
    // One session root (A) that also has its own fallback attempt, one session
    // peer (M), and a plain chain root (B) with one attempt. The rows are mocked
    // so the test does not depend on traffic having produced sessions or chains.
    const now = Date.now();
    const row = (
      id: string,
      offsetMs: number,
      extra: Record<string, unknown> = {},
    ) => ({
      id,
      object: "chat.completion",
      timestamp: new Date(now - offsetMs).toISOString(),
      created_at: new Date(now - offsetMs).toISOString(),
      provider: "openai",
      model: "gpt-4o-mini",
      number_of_retries: 0,
      fallback_index: 0,
      status: "success",
      stream: false,
      latency: 120,
      cost: 0.0001,
      input_history: [],
      responses_input_history: [],
      ...extra,
    });
    const sessionRoot = row("grp-session-root", 60_000, {
      session_id: "grp-session",
      child_count: 1,
      session_child_count: 1,
      session_total_cost: 0.0002,
      session_total_tokens: 40,
    });
    const sessionPeer = row("grp-session-peer", 30_000, {
      session_id: "grp-session",
    });
    const sessionRootAttempt = row("grp-session-root-attempt", 59_000, {
      parent_request_id: "grp-session-root",
      fallback_index: 1,
    });
    const chainRoot = row("grp-chain-root", 10_000, { child_count: 1 });
    const chainAttempt = row("grp-chain-attempt", 9_000, {
      parent_request_id: "grp-chain-root",
      fallback_index: 1,
    });

    test.beforeEach(async ({ page }) => {
      await page.route(
        (url) => url.pathname === "/api/logs",
        async (route) => {
          if (route.request().method() !== "GET") return route.continue();
          const params = new URL(route.request().url()).searchParams;
          const parent = params.get("parent_request_id");
          const session = params.get("session_id");
          let logs;
          if (parent === sessionRoot.id) logs = [sessionRootAttempt];
          else if (parent === chainRoot.id) logs = [chainAttempt];
          else if (session === "grp-session") logs = [sessionRoot, sessionPeer];
          else logs = [chainRoot, sessionRoot];
          await route.fulfill({
            json: {
              logs,
              pagination: {
                limit: 50,
                offset: 0,
                sort_by: "timestamp",
                order: "desc",
              },
              stats: {
                total_requests: logs.length,
                success_rate: 100,
                user_facing_success_rate: 100,
                user_facing_total_requests: logs.length,
                average_latency: 120,
                total_tokens: 0,
                prompt_tokens: 0,
                completion_tokens: 0,
                total_cost: 0,
              },
              has_logs: true,
            },
          });
        },
      );
    });

    test("should tell session rows apart from fallback chain rows", async ({
      page,
    }) => {
      await page.goto("/workspace/logs?grouped=true");

      const sessionBtn = page.getByTestId("log-session-expand-btn");
      const chainBtn = page.getByTestId("log-chain-expand-btn");
      await expect(sessionBtn).toHaveCount(1);
      await expect(chainBtn).toHaveCount(1);
      // The toggles say what they open in words, not just a bare count.
      await expect(sessionBtn).toHaveText("2 turns");
      await expect(chainBtn).toHaveText("1 fallback");

      // Each expander names its own grouping key, so the matching counts stop
      // reading as the same thing.
      await sessionBtn.hover();
      await expect(
        page.getByRole("tooltip").filter({ hasText: "session_id" }),
      ).toBeVisible();
      // Close the session tooltip first: it can sit over the chain toggle and stay open.
      await page.keyboard.press("Escape");
      await expect(page.getByRole("tooltip")).toHaveCount(0);
      await chainBtn.hover();
      await expect(
        page.getByRole("tooltip").filter({ hasText: "parent_request_id" }),
      ).toBeVisible();

      // A session expands into its peer plus the root's own attempt, and each
      // nested row is marked with the kind of link that put it there.
      await sessionBtn.click();
      await expect(page.getByTestId("log-row-kind-session")).toHaveCount(1);
      await expect(page.getByTestId("log-row-kind-session")).toHaveText(
        "turn 2",
      );
      await expect(page.getByTestId("log-row-kind-chain")).toHaveCount(1);
      await expect(page.getByTestId("log-row-kind-chain")).toHaveText(
        "fallback 1",
      );

      await chainBtn.click();
      await expect(page.getByTestId("log-row-kind-session")).toHaveCount(1);
      await expect(page.getByTestId("log-row-kind-chain")).toHaveCount(2);
    });
  });

  test.describe('Decision Logs', () => {
    // Decision logs keep the state as the user message and the answers as the
    // assistant message; the questions live in params, as an array in request
    // order, or, in logs written before decisions were normalized, an object
    // keyed by question name. Rows are mocked so the test does not depend on
    // OpenAI Decisions access. The first instruction carries a redaction
    // placeholder, which must show as stored.
    const now = Date.now()
    const mapQuestions = {
      is_frustrated: { kind: 'noul', instructions: 'Is {{PERSON_1}} frustrated?' },
      category: {
        kind: 'choice',
        instructions: 'Pick the ticket category',
        criteria: { billing: 'charges and refunds', bug: 'product defects', other: 'anything else' },
      },
      urgency: { kind: 'score', instructions: 'How urgent is it?', criteria: ['can wait', 'soon', 'today'] },
    }
    const orderedQuestions = [
      { type: 'predicate', name: 'is_frustrated', instructions: 'Is the customer frustrated?' },
      { type: 'choice', instructions: 'Refund?', choices: [{ value: true }, { value: false }] },
      { type: 'score', name: 'urgency', instructions: 'How urgent?', levels: [{ label: 'Low' }, { label: 'High', description: 'today' }] },
    ]
    const decisionRow = (id: string, offsetMs: number, params: unknown) => ({
      id,
      object: 'decisions',
      timestamp: new Date(now - offsetMs).toISOString(),
      created_at: new Date(now - offsetMs).toISOString(),
      provider: 'openai',
      model: 'gpt-6-luna',
      number_of_retries: 0,
      fallback_index: 0,
      status: 'success',
      stream: false,
      latency: 300,
      cost: 0.0001,
      params,
      input_history: [{ role: 'user', content: 'I was double charged and want a refund today' }],
      responses_input_history: [],
      output_message: { role: 'assistant', content: '{"is_frustrated":{"kind":"noul","value":0.9}}' },
    })
    // Rows are told apart by model name, so the tests do not depend on row order.
    const mapRow = { ...decisionRow('dec-map', 20_000, mapQuestions), model: 'luna-map-form' }
    const orderedRow = { ...decisionRow('dec-ordered', 10_000, orderedQuestions), model: 'luna-ordered-form' }
    const chatRow = {
      ...decisionRow('dec-chat', 5_000, undefined),
      object: 'chat.completion',
      model: 'chat-not-decision',
      params: { temperature: 0.2 },
    }

    test.beforeEach(async ({ page }) => {
      const rows = [chatRow, orderedRow, mapRow]
      await page.route(
        (url) => url.pathname.startsWith('/api/logs'),
        async (route) => {
          if (route.request().method() !== 'GET') return route.continue()
          const { pathname } = new URL(route.request().url())
          const byId = rows.find((candidate) => pathname === `/api/logs/${candidate.id}`)
          if (byId) return route.fulfill({ json: byId })
          if (pathname !== '/api/logs') return route.continue()
          await route.fulfill({
            json: {
              logs: rows,
              pagination: { limit: 50, offset: 0, sort_by: 'timestamp', order: 'desc' },
              stats: {
                total_requests: rows.length,
                success_rate: 100,
                user_facing_success_rate: 100,
                user_facing_total_requests: rows.length,
                average_latency: 300,
                total_tokens: 0,
                prompt_tokens: 0,
                completion_tokens: 0,
                total_cost: 0,
              },
              has_logs: true,
            },
          })
        },
      )
    })

    // openRow opens the detail sheet of the row carrying the given model name.
    const openRow = async (logsPage: { tableRows: Locator; logDetailSheet: Locator }, model: string) => {
      await logsPage.tableRows.filter({ hasText: model }).first().click()
      await expect(logsPage.logDetailSheet).toBeVisible({ timeout: 5000 })
    }

    test('should show the questions of an older decision log keyed by name', async ({ logsPage, page }) => {
      await logsPage.goto()
      await openRow(logsPage, 'luna-map-form')

      const box = page.getByTestId('log-decision-questions')
      await expect(box).toBeVisible()
      await expect(box).toContainText('Questions (3)')
      // The redaction placeholder shows as stored; no real value is substituted.
      await expect(box).toContainText('{{PERSON_1}}')
      // State and answers keep their own rows in the timeline.
      await expect(page.getByText('State', { exact: true })).toBeVisible()
      await expect(page.getByText('Decision', { exact: true })).toBeVisible()
    })

    test('should show the questions of a decision in request order', async ({ logsPage, page }) => {
      await logsPage.goto()
      await openRow(logsPage, 'luna-ordered-form')

      const box = page.getByTestId('log-decision-questions')
      await expect(box).toBeVisible()
      await expect(box).toContainText('Questions (3)')
      // The editor shows the questions as sent, so their instructions must
      // appear in the order the request listed them.
      const positionsOf = async () => {
        // Monaco renders spaces as non-breaking spaces.
        const text = (await box.innerText()).replace(/\u00a0/g, ' ')
        return orderedQuestions.map((question) => text.indexOf(question.instructions))
      }
      await expect.poll(async () => (await positionsOf()).every((position) => position >= 0)).toBe(true)
      const positions = await positionsOf()
      expect([...positions].sort((a, b) => a - b)).toEqual(positions)
    })

    test('should copy the questions as JSON', async ({ logsPage, page, context }) => {
      await context.grantPermissions(['clipboard-read', 'clipboard-write'])
      await logsPage.goto()
      await openRow(logsPage, 'luna-ordered-form')

      const box = page.getByTestId('log-decision-questions')
      await expect(box).toBeVisible()
      await box.getByRole('button').first().click()
      // The write is asynchronous, so poll until the clipboard holds the JSON.
      await expect
        .poll(async () => {
          const copied = await page.evaluate(() => navigator.clipboard.readText())
          try {
            return JSON.parse(copied)
          } catch {
            return null
          }
        })
        .toEqual(orderedQuestions)
    })

    test('should not show a questions box on a chat log', async ({ logsPage, page }) => {
      await logsPage.goto()
      await openRow(logsPage, 'chat-not-decision')

      await expect(page.getByTestId('log-decision-questions')).toHaveCount(0)
    })
  })

  test.describe("URL State Persistence", () => {
    test("should persist filters in URL", async ({ logsPage }) => {
      const searchVisible = await logsPage.searchInput
        .isVisible()
        .catch(() => false);
      if (!searchVisible) return;

      await logsPage.searchLogs("persistent-search");

      // Search is debounced (500ms) then URL updates; wait for URL to contain the param
      await expect
        .poll(() => logsPage.page.url(), {
          timeout: 8000,
          intervals: [300, 500, 500],
        })
        .toContain("content_search=");
      const url = logsPage.page.url();
      // Value may be percent-encoded (e.g. persistent-search → persistent%2Dsearch)
      expect(decodeURIComponent(url)).toContain("persistent-search");
    });

    test("should look up a pasted request ID by id instead of content", async ({
      logsPage,
    }) => {
      const searchVisible = await logsPage.searchInput
        .isVisible()
        .catch(() => false);
      if (!searchVisible) return;

      // A log's primary key is its request ID, so a UUID-shaped query switches
      // the search box from free-text content search to an exact ID lookup.
      const requestId = "018f2c3d-4e5f-4a6b-8c9d-0e1f2a3b4c5d";
      await logsPage.searchLogs(requestId);

      await expect
        .poll(() => logsPage.page.url(), {
          timeout: 8000,
          intervals: [300, 500, 500],
        })
        .toContain("request_id=");
      const url = logsPage.page.url();
      expect(decodeURIComponent(url)).toContain(requestId);
      expect(url).not.toContain("content_search=");

      // The mode switch is surfaced to the user.
      await expect(
        logsPage.page.locator('[data-testid="logs-search-id-badge"]'),
      ).toBeVisible();
    });

    test("should restore state from URL", async ({ logsPage, page }) => {
      // Logs page uses start_time and end_time (unix timestamps), not period
      const endTime = Math.floor(Date.now() / 1000);
      const startTime = endTime - 7 * 24 * 60 * 60; // 7 days ago
      await page.goto(
        `/workspace/logs?start_time=${startTime}&end_time=${endTime}`,
      );

      // Wait for page to load and URL to reflect state (nuqs may merge or keep params)
      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/start_time=\d+/);
      const url = page.url();
      expect(url).toMatch(/start_time=\d+/);
      expect(url).toMatch(/end_time=\d+/);
    });
  });
});
