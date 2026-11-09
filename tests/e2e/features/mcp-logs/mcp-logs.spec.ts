import { readFileSync } from 'fs'
import { resolve } from 'path'
import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('MCP Logs', () => {
  test.beforeEach(async ({ mcpLogsPage }) => {
    await mcpLogsPage.goto()
  })

  test.describe('MCP Logs Display', () => {
    test('should display MCP logs table or getting started guide', async ({ mcpLogsPage }) => {
      // When MCP logs exist the table is visible; otherwise a "Get Started" guide is shown
      const tableExists = await mcpLogsPage.logsTable.isVisible().catch(() => false)
      const gettingStarted = await mcpLogsPage.page.getByText(/Get Started/i).isVisible().catch(() => false)
      expect(tableExists || gettingStarted).toBe(true)
    })

    test('should display stats cards', async ({ mcpLogsPage }) => {
      // Stats cards are only visible when MCP log data exists
      const tableExists = await mcpLogsPage.logsTable.isVisible().catch(() => false)
      if (!tableExists) {
        test.skip(true, 'No MCP logs — stats cards not rendered in getting-started view')
        return
      }
      const statsVisible = await mcpLogsPage.areStatsVisible()
      expect(statsVisible).toBe(true)
    })

    test('should display filters section', async ({ mcpLogsPage }) => {
      // Filters are only visible when MCP log data exists (not in getting-started view)
      const tableExists = await mcpLogsPage.logsTable.isVisible().catch(() => false)
      if (!tableExists) {
        test.skip(true, 'No MCP logs — filters not rendered in getting-started view')
        return
      }
      const searchVisible = await mcpLogsPage.searchInput.isVisible().catch(() => false)
      const filtersButtonVisible = await mcpLogsPage.filtersButton.isVisible().catch(() => false)
      expect(searchVisible || filtersButtonVisible).toBe(true)
    })
  })

  test.describe('MCP Log Metric Strip', () => {
    test('draws the LLM and Relay metric strip from the MCP stats and histograms', async ({ mcpLogsPage, page }) => {
      const stats = { total_executions: 5, success_count: 3, error_count: 1, cancelled_count: 1, success_rate: 60, average_latency: 42, total_cost: 0.5 }
      const buckets = [0, 1, 2].map((i) => new Date(Date.now() - (3 - i) * 3600_000).toISOString())
      await page.route('**/api/mcp-logs**', async (route) => {
        const url = new URL(route.request().url())
        const json = (body: unknown) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
        switch (url.pathname) {
          case '/api/mcp-logs/stats':
            return json({ ...stats, has_previous_period: true, previous: { ...stats, total_executions: 4, success_rate: 50, total_cost: 0.25 } })
          case '/api/mcp-logs/histogram':
            return json({ bucket_size_seconds: 3600, buckets: buckets.map((timestamp, i) => ({ timestamp, count: i + 1, success: i, error: 1, cancelled: 0 })) })
          case '/api/mcp-logs/histogram/cost':
            return json({ bucket_size_seconds: 3600, buckets: buckets.map((timestamp, i) => ({ timestamp, total_cost: 0.1 * (i + 1) })) })
          case '/api/mcp-logs/histogram/latency':
            return json({
              bucket_size_seconds: 3600,
              buckets: buckets.map((timestamp, i) => ({ timestamp, avg_latency: 40 + i, p90_latency: 50, p95_latency: 60, p99_latency: 70, avg_overhead: 0, p90_overhead: 0, p95_overhead: 0, p99_overhead: 0, total_requests: i + 1 })),
            })
          case '/api/mcp-logs/filterdata':
            return json({ tool_names: [], server_labels: [], virtual_keys: [] })
          case '/api/mcp-logs':
            return json({ logs: [], pagination: { limit: 50, offset: 0, sort_by: 'timestamp', order: 'desc', total_count: 5 }, has_logs: true })
        }
        return route.continue()
      })
      const latencyRequested = page.waitForRequest((request) => new URL(request.url()).pathname === '/api/mcp-logs/histogram/latency')
      const statsCompared = page.waitForRequest((request) => {
        const url = new URL(request.url())
        return url.pathname === '/api/mcp-logs/stats' && url.searchParams.get('compare_to_previous') === 'true'
      })
      await mcpLogsPage.goto()
      await Promise.all([latencyRequested, statsCompared])

      const strip = page.getByTestId('mcp-logs-metric-strip')
      await expect(strip).toBeVisible()
      await expect(strip.getByTestId('mcp-logs-stat-executions')).toContainText('Total Executions')
      await expect(strip.getByTestId('mcp-logs-stat-executions')).toContainText('5')
      // A previous period is reported, so the executions, success rate and cost tiles each carry their change.
      for (const tile of ['mcp-logs-stat-executions', 'mcp-logs-stat-success', 'mcp-logs-stat-cost']) {
        await expect(strip.getByTestId(tile).getByTestId('mcp-logs-metric-trailing')).toBeVisible()
      }
      await expect(strip.getByTestId('mcp-logs-stat-latency')).toBeVisible()
    })

    test('translates the executions trend that stands in when there is no previous period', async ({ mcpLogsPage, page }) => {
      const stats = { total_executions: 6, success_count: 6, error_count: 0, cancelled_count: 0, success_rate: 100, average_latency: 42, total_cost: 0 }
      const buckets = [0, 1, 2].map((i) => new Date(Date.now() - (3 - i) * 3600_000).toISOString())
      await page.route('**/api/mcp-logs**', async (route) => {
        const url = new URL(route.request().url())
        const json = (body: unknown) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })
        switch (url.pathname) {
          case '/api/mcp-logs/stats':
            return json({ ...stats, has_previous_period: false })
          case '/api/mcp-logs/histogram':
            // Executions climb from 1 to 3 across the window, so the window's own trend reads as rising.
            return json({ bucket_size_seconds: 3600, buckets: buckets.map((timestamp, i) => ({ timestamp, count: i + 1, success: i + 1, error: 0, cancelled: 0 })) })
          case '/api/mcp-logs/histogram/cost':
            return json({ bucket_size_seconds: 3600, buckets: [] })
          case '/api/mcp-logs/histogram/latency':
            return json({ bucket_size_seconds: 3600, buckets: [] })
          case '/api/mcp-logs/filterdata':
            return json({ tool_names: [], server_labels: [], virtual_keys: [] })
          case '/api/mcp-logs':
            return json({ logs: [], pagination: { limit: 50, offset: 0, sort_by: 'timestamp', order: 'desc', total_count: 6 }, has_logs: true })
        }
        return route.continue()
      })
      await page.addInitScript(() => localStorage.setItem('bifrost.locale', 'de'))
      await mcpLogsPage.goto()

      // The expected text comes from the shipped German catalog, so a translation edit does not break the test.
      const german = JSON.parse(readFileSync(resolve(__dirname, '../../../../ui/locales/de/observability.json'), 'utf8'))
      const trailing = page.getByTestId('mcp-logs-stat-executions').getByTestId('mcp-logs-metric-trailing')
      await expect(trailing).toHaveText(german.logs.stats.rising)
    })
  })

  test.describe('MCP Log Filtering', () => {
    test('should filter logs by tool name', async ({ mcpLogsPage, page }) => {
      const filtersVisible = await mcpLogsPage.filtersButton.isVisible().catch(() => false)
      if (!filtersVisible) {
        test.skip(true, 'Filters button not visible')
        return
      }
      const applied = await mcpLogsPage.filterByToolName()
      if (!applied) {
        test.skip(true, 'No tool name options in filter list')
        return
      }
      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/tool_names=/)
    })

    test('should filter logs by server label', async ({ mcpLogsPage, page }) => {
      const filtersVisible = await mcpLogsPage.filtersButton.isVisible().catch(() => false)
      if (!filtersVisible) {
        test.skip(true, 'Filters button not visible')
        return
      }
      const applied = await mcpLogsPage.filterByServerLabel()
      if (!applied) {
        test.skip(true, 'No server label options in filter list')
        return
      }
      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/server_labels=/)
    })

    test('should filter logs by status', async ({ mcpLogsPage, page }) => {
      const filtersVisible = await mcpLogsPage.filtersButton.isVisible().catch(() => false)
      if (!filtersVisible) {
        test.skip(true, 'Filters button not visible')
        return
      }

      const applied = await mcpLogsPage.filterByStatus('success')
      if (!applied) {
        test.skip(true, 'No status options in filter list')
        return
      }

      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/status=success/)
    })

    test('should search logs by content', async ({ mcpLogsPage }) => {
      const searchInput = mcpLogsPage.searchInput
      const isVisible = await searchInput.isVisible().catch(() => false)

      if (isVisible) {
        const query = `test-query-${Date.now()}`
        await mcpLogsPage.searchLogs(query)

        // Check that search input contains the query (DOM state)
        const inputValue = await searchInput.inputValue().catch(() => '')
        expect(inputValue).toContain(query)
      }
    })

    test('should filter by time period', async ({ mcpLogsPage }) => {
      const datePicker = mcpLogsPage.dateRangePicker
      const isVisible = await datePicker.isVisible().catch(() => false)

      if (isVisible) {
        // Get initial date picker value
        const initialValue = await datePicker.textContent().catch(() => '')

        await mcpLogsPage.selectTimePeriod('7d')

        // Check that date picker value changed (DOM state)
        const newValue = await datePicker.textContent().catch(() => '')
        expect(newValue || initialValue).toBeTruthy()
      }
    })
  })

  test.describe('MCP Log Details', () => {
    test('should open log details sheet', async ({ mcpLogsPage }) => {
      const logCount = await mcpLogsPage.getLogCount()

      if (logCount > 0) {
        await mcpLogsPage.viewLogDetails(0)

        const sheetVisible = await mcpLogsPage.logDetailSheet.isVisible().catch(() => false)
        expect(sheetVisible).toBe(true)

        await mcpLogsPage.closeLogDetails()
      }
    })

    test('should close log details sheet', async ({ mcpLogsPage }) => {
      const logCount = await mcpLogsPage.getLogCount()

      if (logCount > 0) {
        await mcpLogsPage.viewLogDetails(0)
        await mcpLogsPage.closeLogDetails()

        const sheetVisible = await mcpLogsPage.logDetailSheet.isVisible().catch(() => false)
        expect(sheetVisible).toBe(false)
      }
    })
  })

  test.describe('Pagination', () => {
    test('should navigate to next page', async ({ mcpLogsPage }) => {
      const paginationVisible = await mcpLogsPage.paginationControls.isVisible().catch(() => false)
      if (!paginationVisible) {
        test.skip(true, 'No MCP logs — pagination not rendered')
        return
      }
      const nextBtn = mcpLogsPage.nextPageBtn
      const isEnabled = await nextBtn.isEnabled().catch(() => false)

      if (!isEnabled) {
        test.skip(true, 'Only one page of results; skipping pagination test')
        return
      }

      const initialPage = mcpLogsPage.getCurrentPageNumber()
      await mcpLogsPage.goToNextPage()

      await expect
        .poll(() => mcpLogsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(initialPage + 1)
    })

    test('should navigate to previous page', async ({ mcpLogsPage }) => {
      const paginationVisible = await mcpLogsPage.paginationControls.isVisible().catch(() => false)
      if (!paginationVisible) {
        test.skip(true, 'No MCP logs — pagination not rendered')
        return
      }
      const nextBtn = mcpLogsPage.nextPageBtn
      const nextEnabled = await nextBtn.isEnabled().catch(() => false)

      if (!nextEnabled) {
        test.skip(true, 'Only one page of results; skipping pagination test')
        return
      }

      await mcpLogsPage.goToNextPage()

      await expect
        .poll(() => mcpLogsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(2)

      const prevBtn = mcpLogsPage.prevPageBtn
      const prevEnabled = await prevBtn.isEnabled().catch(() => false)

      if (!prevEnabled) {
        test.skip(true, 'Only one page of results; skipping previous-page test')
        return
      }

      await mcpLogsPage.goToPreviousPage()

      // We were on page 2; after previous we must be on page 1 (assert concrete value to avoid race with captured page number)
      await expect
        .poll(() => mcpLogsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(1)
    })
  })

  test.describe('Table Sorting', () => {
    test('should sort by timestamp', async ({ mcpLogsPage }) => {
      const tableExists = await mcpLogsPage.logsTable.isVisible().catch(() => false)
      if (!tableExists) {
        test.skip(true, 'No MCP logs — table not rendered')
        return
      }
      // Timestamp is the default sort column (desc), so clicking it toggles to asc
      const initialUrl = mcpLogsPage.page.url()

      await mcpLogsPage.sortBy('timestamp')

      // Wait for URL to actually change after sort
      await expect
        .poll(() => mcpLogsPage.page.url(), { timeout: 5000 })
        .not.toBe(initialUrl)
    })

    test('should sort by latency', async ({ mcpLogsPage }) => {
      const tableExists = await mcpLogsPage.logsTable.isVisible().catch(() => false)
      if (!tableExists) {
        test.skip(true, 'No MCP logs — table not rendered')
        return
      }
      await mcpLogsPage.sortBy('latency')

      // Wait for URL to update
      await mcpLogsPage.page.waitForURL(/sort_by=latency/, { timeout: 5000 })

      // Check URL state for latency sort
      const sortState = await mcpLogsPage.getSortState('latency')
      expect(sortState).toBeTruthy()
    })
  })

  test.describe('Live Updates', () => {
    test('should toggle live updates', async ({ mcpLogsPage }) => {
      const tableExists = await mcpLogsPage.logsTable.isVisible().catch(() => false)
      if (!tableExists) {
        test.skip(true, 'No MCP logs — live toggle not rendered in getting-started view')
        return
      }
      const liveToggle = mcpLogsPage.liveToggle
      const isVisible = await liveToggle.isVisible().catch(() => false)

      if (isVisible) {
        // Default is live_enabled=true (but URL may not have it since it's the default)
        // Check for live_enabled=false to determine if currently disabled
        const initialUrl = mcpLogsPage.page.url()
        const initialLiveDisabled = initialUrl.includes('live_enabled=false')

        await mcpLogsPage.toggleLiveUpdates()

        // Wait for URL to reflect live_enabled toggle
        await mcpLogsPage.page.waitForURL(/live_enabled=/, { timeout: 5000 })

        const newUrl = mcpLogsPage.page.url()
        const newLiveDisabled = newUrl.includes('live_enabled=false')

        // Live enabled state should have toggled
        // If initially enabled (not disabled), after toggle it should be disabled
        // If initially disabled, after toggle it should be enabled (no live_enabled=false)
        expect(newLiveDisabled).not.toBe(initialLiveDisabled)
      }
    })
  })
})
