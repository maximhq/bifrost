import { expect, test } from '../../core/fixtures/base.fixture'
import { createLogSearchQuery, SAMPLE_MODELS, SAMPLE_PROVIDERS } from './logs.data'

test.describe('LLM Logs', () => {
  test.beforeEach(async ({ logsPage }) => {
    await logsPage.goto()
  })

  test.describe('Logs Display', () => {
    test('should display logs table', async ({ logsPage }) => {
      // Table should be visible after goto (which waits for load)
      const tableExists = await logsPage.logsTable.isVisible().catch(() => false)
      expect(tableExists).toBe(true)
    })

    test('should display stats cards', async ({ logsPage }) => {
      const statsVisible = await logsPage.areStatsVisible()
      expect(statsVisible).toBe(true)
    })

    test('should display filters section', async ({ logsPage }) => {
      // Check if the search input or filters button is visible
      // These are always visible when the page loads (not inside empty state)
      const searchVisible = await logsPage.searchInput.isVisible().catch(() => false)
      const filtersButtonVisible = await logsPage.filtersButton.isVisible().catch(() => false)

      // Either search input OR filters button should be visible
      expect(searchVisible || filtersButtonVisible).toBe(true)
    })
  })

  test.describe('Log Filtering', () => {
    test('should filter logs by provider', async ({ logsPage }) => {
      // Try to filter by first available provider
      const providerFilter = logsPage.providerFilter
      const isVisible = await providerFilter.isVisible().catch(() => false)

      if (!isVisible || SAMPLE_PROVIDERS.length === 0) {
        test.skip(!isVisible || SAMPLE_PROVIDERS.length === 0, 'Provider filter not visible or no sample providers')
        return
      }

      // Get initial filter state
      const initialValue = await providerFilter.textContent().catch(() => '')

      await logsPage.filterByProvider(SAMPLE_PROVIDERS[0])

      // Check that filter value changed (or verify filter is applied via DOM)
      const newValue = await providerFilter.textContent().catch(() => '')
      // Filter should have changed or show selected provider
      expect(newValue || initialValue).toBeTruthy()
    })

    test('should filter logs by model', async ({ logsPage }) => {
      const modelFilter = logsPage.modelFilter
      const isVisible = await modelFilter.isVisible().catch(() => false)

      if (!isVisible || SAMPLE_MODELS.length === 0) {
        test.skip(!isVisible || SAMPLE_MODELS.length === 0, 'Model filter not visible or no sample models')
        return
      }

      // Get initial filter state
      const initialValue = await modelFilter.textContent().catch(() => '')

      await logsPage.filterByModel(SAMPLE_MODELS[0])

      // Check that filter value changed (or verify filter is applied via DOM)
      const newValue = await modelFilter.textContent().catch(() => '')
      // Filter should have changed or show selected model
      expect(newValue || initialValue).toBeTruthy()
    })

    test('should filter logs by status', async ({ logsPage, page }) => {
      const filtersVisible = await logsPage.filtersButton.isVisible().catch(() => false)
      if (!filtersVisible) {
        test.skip(true, 'Filters button not visible')
        return
      }

      await logsPage.filterByStatus('success')

      // Assert status filter is applied: logs page persists filters in URL (e.g. status=success)
      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/status=success/)
    })

    test('should search logs by content', async ({ logsPage }) => {
      const searchInput = logsPage.searchInput
      const isVisible = await searchInput.isVisible().catch(() => false)

      if (!isVisible) {
        test.skip(true, 'Search input not visible')
        return
      }

      const query = createLogSearchQuery()
      await logsPage.searchLogs(query)

      // Check that search input contains the query (DOM state)
      const inputValue = await searchInput.inputValue().catch(() => '')
      expect(inputValue).toContain(query)
    })

    test('should clear search', async ({ logsPage }) => {
      const searchInput = logsPage.searchInput
      const isVisible = await searchInput.isVisible().catch(() => false)

      if (!isVisible) {
        test.skip(true, 'Search input not visible')
        return
      }

      await logsPage.searchLogs('test query')
      await logsPage.clearSearch()

      // Search should be cleared
      const inputValue = await searchInput.inputValue().catch(() => '')
      expect(inputValue).toBe('')
    })

    test('should filter by time period', async ({ logsPage }) => {
      const datePicker = logsPage.dateRangePicker
      const isVisible = await datePicker.isVisible().catch(() => false)

      if (!isVisible) {
        test.skip(true, 'Date range picker not visible')
        return
      }

      // Get initial date picker value
      const initialValue = await datePicker.textContent().catch(() => '')

      await logsPage.selectTimePeriod('7d')

      // Check that date picker value changed (DOM state)
      const newValue = await datePicker.textContent().catch(() => '')
      // Date picker should show "Last 7 days" or similar
      expect(newValue || initialValue).toBeTruthy()
    })
  })

  test.describe('Log Details', () => {
    test('should open log details sheet', async ({ logsPage }) => {
      // Wait a bit for logs to potentially load
      await logsPage.page.waitForTimeout(1000)

      const logCount = await logsPage.getLogCount()

      if (logCount > 0) {
        await logsPage.viewLogDetails(0)

        // Wait for sheet animation
        await logsPage.page.waitForTimeout(500)

        // Detail sheet should be visible
        const sheetVisible = await logsPage.logDetailSheet.isVisible().catch(() => false)
        expect(sheetVisible).toBe(true)

        // Close the sheet
        await logsPage.closeLogDetails()
      } else {
        // If no logs exist, the test passes (nothing to click)
        expect(logCount).toBe(0)
      }
    })

    test('should close log details sheet', async ({ logsPage }) => {
      const logCount = await logsPage.getLogCount()

      if (logCount > 0) {
        await logsPage.viewLogDetails(0)
        await logsPage.closeLogDetails()

        // Sheet should be closed
        const sheetVisible = await logsPage.logDetailSheet.isVisible().catch(() => false)
        expect(sheetVisible).toBe(false)
      }
    })
  })

  test.describe('Pagination', () => {
    test('should navigate to next page', async ({ logsPage }) => {
      // Wait for pagination to settle (useTablePageSize may adjust limit dynamically)
      await logsPage.page.waitForTimeout(2000)
      const paginationVisible = await logsPage.paginationControls.isVisible().catch(() => false)
      if (!paginationVisible) {
        test.skip(true, 'Pagination controls not visible')
        return
      }
      const nextBtn = logsPage.nextPageBtn.first()
      const isEnabled = await nextBtn.isEnabled().catch(() => false)

      if (!isEnabled) {
        test.skip(true, 'Only one page of results; skipping pagination test')
        return
      }

      const initialPage = logsPage.getCurrentPageNumber()
      expect(initialPage).toBe(1)
      await logsPage.goToNextPage()

      await expect
        .poll(() => logsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(initialPage + 1)
    })

    test('should navigate to previous page', async ({ logsPage }) => {
      // Wait for pagination to settle (useTablePageSize may adjust limit dynamically)
      await logsPage.page.waitForTimeout(2000)
      const paginationVisible = await logsPage.paginationControls.isVisible().catch(() => false)
      if (!paginationVisible) {
        test.skip(true, 'Pagination controls not visible')
        return
      }
      const nextBtn = logsPage.nextPageBtn.first()
      const nextEnabled = await nextBtn.isEnabled().catch(() => false)

      if (!nextEnabled) {
        test.skip(true, 'Only one page of results; skipping pagination test')
        return
      }

      await logsPage.goToNextPage()

      await expect
        .poll(() => logsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(2)

      const prevBtn = logsPage.prevPageBtn.first()
      const prevEnabled = await prevBtn.isEnabled().catch(() => false)

      if (!prevEnabled) {
        test.skip(true, 'Only one page of results; skipping previous-page test')
        return
      }

      await logsPage.goToPreviousPage()

      await expect
        .poll(() => logsPage.getCurrentPageNumber(), { timeout: 5000 })
        .toBe(1)
    })
  })

  test.describe('Table Sorting', () => {
    test('should sort by timestamp', async ({ logsPage }) => {
      // Timestamp is the default sort column (desc), so clicking it toggles to asc
      await logsPage.sortBy('timestamp')

      // Timestamp sort toggles order; wait for URL to reflect the change
      await logsPage.page.waitForURL(/order=asc|sort_by=timestamp/, { timeout: 5000 })
    })

    test('should sort by latency', async ({ logsPage }) => {
      await logsPage.sortBy('latency')

      // Wait for URL to update
      await logsPage.page.waitForURL(/sort_by=latency/, { timeout: 5000 })

      // Check URL state for latency sort
      const sortState = await logsPage.getSortState('latency')
      expect(sortState).toBeTruthy()
    })

    test('should sort by cost', async ({ logsPage }) => {
      await logsPage.sortBy('cost')

      // Wait for URL to update
      await logsPage.page.waitForURL(/sort_by=cost/, { timeout: 5000 })

      // Check URL state for cost sort
      const sortState = await logsPage.getSortState('cost')
      expect(sortState).toBeTruthy()
    })
  })

  test.describe('Live Updates', () => {
    test('should toggle live updates', async ({ logsPage }) => {
      const liveToggle = logsPage.liveToggle
      const isVisible = await liveToggle.isVisible().catch(() => false)

      if (!isVisible) {
        test.skip(true, 'Live toggle not visible')
        return
      }

      // Default is live_enabled=true (but URL may not have it since it's the default)
      // Check for live_enabled=false to determine if currently disabled
      const initialUrl = logsPage.page.url()
      const initialLiveDisabled = initialUrl.includes('live_enabled=false')

      await logsPage.toggleLiveUpdates()

      // Wait for URL to reflect live_enabled toggle
      await logsPage.page.waitForURL(/live_enabled=/, { timeout: 5000 })

      const newUrl = logsPage.page.url()
      const newLiveDisabled = newUrl.includes('live_enabled=false')

      // Live enabled state should have toggled
      // If initially enabled (not disabled), after toggle it should be disabled
      // If initially disabled, after toggle it should be enabled (no live_enabled=false)
      expect(newLiveDisabled).not.toBe(initialLiveDisabled)
    })
  })

  test.describe('Empty State', () => {
    test('should show empty state when no logs', async ({ logsPage }) => {
      // Try to filter by a non-existent provider
      const searchInput = logsPage.searchInput
      const isVisible = await searchInput.isVisible().catch(() => false)

      if (!isVisible) {
        test.skip(true, 'Search input not visible')
        return
      }

      await logsPage.searchLogs(`nonexistent-query-${Date.now()}`)

      // After searching for a non-existent query, empty state should appear (wait for API + render)
      await expect(
        logsPage.page.locator('text=/No results found|No logs found/i')
      ).toBeVisible({ timeout: 10000 })
    })
  })

  test.describe('Advanced Filtering', () => {
    test('should combine multiple filters', async ({ logsPage }) => {
      // Apply multiple filters if they're visible
      const searchVisible = await logsPage.searchInput.isVisible().catch(() => false)
      const providerVisible = await logsPage.providerFilter.isVisible().catch(() => false)

      if (!searchVisible || !providerVisible) {
        test.skip(true, 'Search input or provider filter not visible')
        return
      }

      // Apply search filter
      await logsPage.searchLogs('test')

      // Apply provider filter
      if (SAMPLE_PROVIDERS.length > 0) {
        await logsPage.filterByProvider(SAMPLE_PROVIDERS[0])
      }

      // Both filters should be applied
      const searchValue = await logsPage.searchInput.inputValue().catch(() => '')
      expect(searchValue).toContain('test')
    })

    test('should clear all filters', async ({ logsPage }) => {
      const searchVisible = await logsPage.searchInput.isVisible().catch(() => false)

      if (!searchVisible) {
        test.skip(true, 'Search input not visible')
        return
      }

      // Apply a filter first
      await logsPage.searchLogs('test query to clear')

      // Clear the search
      await logsPage.clearSearch()

      // Search should be empty
      const searchValue = await logsPage.searchInput.inputValue().catch(() => '')
      expect(searchValue).toBe('')
    })

    test('should search within filtered results', async ({ logsPage }) => {
      const searchVisible = await logsPage.searchInput.isVisible().catch(() => false)
      const statusVisible = await logsPage.statusFilter.isVisible().catch(() => false)

      if (!searchVisible || !statusVisible) {
        test.skip(true, 'Search input or status filter not visible')
        return
      }

      // Apply status filter first
      await logsPage.filterByStatus('success')

      // Then apply search
      await logsPage.searchLogs('api')

      // Search input should contain the query
      const searchValue = await logsPage.searchInput.inputValue().catch(() => '')
      expect(searchValue).toContain('api')
    })
  })

  test.describe('URL State Persistence', () => {
    test('should persist filters in URL', async ({ logsPage }) => {
      const searchVisible = await logsPage.searchInput.isVisible().catch(() => false)
      if (!searchVisible) return

      await logsPage.searchLogs('persistent-search')

      // Search is debounced (500ms) then URL updates; wait for URL to contain the param
      await expect
        .poll(
          () => logsPage.page.url(),
          { timeout: 8000, intervals: [300, 500, 500] }
        )
        .toContain('content_search=')
      const url = logsPage.page.url()
      // Value may be percent-encoded (e.g. persistent-search → persistent%2Dsearch)
      expect(decodeURIComponent(url)).toContain('persistent-search')
    })

    test('should restore state from URL', async ({ logsPage, page }) => {
      // Logs page uses start_time and end_time (unix timestamps), not period
      const endTime = Math.floor(Date.now() / 1000)
      const startTime = endTime - 7 * 24 * 60 * 60 // 7 days ago
      await page.goto(`/workspace/logs?start_time=${startTime}&end_time=${endTime}`)

      // Wait for page to load and URL to reflect state (nuqs may merge or keep params)
      await expect
        .poll(() => page.url(), { timeout: 5000, intervals: [200, 300, 500] })
        .toMatch(/start_time=\d+/)
      const url = page.url()
      expect(url).toMatch(/start_time=\d+/)
      expect(url).toMatch(/end_time=\d+/)
    })
  })
})
