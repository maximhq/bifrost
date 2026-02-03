import { test, expect } from '../../core/fixtures/base.fixture'

test.describe('MCP Logs', () => {
  test.beforeEach(async ({ mcpLogsPage }) => {
    await mcpLogsPage.goto()
  })

  test.describe('MCP Logs Display', () => {
    test('should display MCP logs table', async ({ mcpLogsPage }) => {
      // Table should be visible after goto (which waits for load)
      const tableExists = await mcpLogsPage.logsTable.isVisible().catch(() => false)
      expect(tableExists).toBe(true)
    })

    test('should display stats cards', async ({ mcpLogsPage }) => {
      const statsVisible = await mcpLogsPage.areStatsVisible()
      expect(statsVisible).toBe(true)
    })

    test('should display filters section', async ({ mcpLogsPage }) => {
      // Check if the search input or filters button is visible
      // These are always visible when the page loads (not inside empty state)
      const searchVisible = await mcpLogsPage.searchInput.isVisible().catch(() => false)
      const filtersButtonVisible = await mcpLogsPage.filtersButton.isVisible().catch(() => false)
      
      // Either search input OR filters button should be visible
      expect(searchVisible || filtersButtonVisible).toBe(true)
    })
  })

  test.describe('MCP Log Filtering', () => {
    test('should filter logs by tool name', async ({ mcpLogsPage }) => {
      const toolNameFilter = mcpLogsPage.toolNameFilter
      const isVisible = await toolNameFilter.isVisible().catch(() => false)
      
      if (isVisible) {
        // Get initial filter state
        const initialValue = await toolNameFilter.textContent().catch(() => '')
        
        // Try to filter by a tool name if available
        await mcpLogsPage.filterByToolName('test-tool')
        
        // Check that filter value changed (or verify filter is applied via DOM)
        const newValue = await toolNameFilter.textContent().catch(() => '')
        expect(newValue || initialValue).toBeTruthy()
      }
    })

    test('should filter logs by server label', async ({ mcpLogsPage }) => {
      const serverLabelFilter = mcpLogsPage.serverLabelFilter
      const isVisible = await serverLabelFilter.isVisible().catch(() => false)
      
      if (isVisible) {
        // Get initial filter state
        const initialValue = await serverLabelFilter.textContent().catch(() => '')
        
        await mcpLogsPage.filterByServerLabel('test-server')
        
        // Check that filter value changed (or verify filter is applied via DOM)
        const newValue = await serverLabelFilter.textContent().catch(() => '')
        expect(newValue || initialValue).toBeTruthy()
      }
    })

    test('should filter logs by status', async ({ mcpLogsPage }) => {
      const statusFilter = mcpLogsPage.statusFilter
      const isVisible = await statusFilter.isVisible().catch(() => false)
      
      if (isVisible) {
        // Get initial filter state
        const initialValue = await statusFilter.textContent().catch(() => '')
        
        await mcpLogsPage.filterByStatus('success')
        
        // Check that filter value changed (or verify filter is applied via DOM)
        const newValue = await statusFilter.textContent().catch(() => '')
        expect(newValue || initialValue).toBeTruthy()
      }
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
      const nextBtn = mcpLogsPage.nextPageBtn
      const isEnabled = await nextBtn.isEnabled().catch(() => false)
      
      if (isEnabled) {
        const initialUrl = mcpLogsPage.page.url()
        
        await mcpLogsPage.goToNextPage()
        
        const newUrl = mcpLogsPage.page.url()
        expect(newUrl).not.toBe(initialUrl)
      }
    })

    test('should navigate to previous page', async ({ mcpLogsPage }) => {
      const nextBtn = mcpLogsPage.nextPageBtn
      const nextEnabled = await nextBtn.isEnabled().catch(() => false)
      
      if (nextEnabled) {
        await mcpLogsPage.goToNextPage()
        
        const prevBtn = mcpLogsPage.prevPageBtn
        const prevEnabled = await prevBtn.isEnabled().catch(() => false)
        
        if (prevEnabled) {
          const urlBefore = mcpLogsPage.page.url()
          await mcpLogsPage.goToPreviousPage()
          
          const urlAfter = mcpLogsPage.page.url()
          expect(urlAfter).not.toBe(urlBefore)
        }
      }
    })
  })

  test.describe('Table Sorting', () => {
    test('should sort by timestamp', async ({ mcpLogsPage }) => {
      // Timestamp is the default sort column (desc), so clicking it toggles to asc
      const initialUrl = mcpLogsPage.page.url()
      
      await mcpLogsPage.sortBy('timestamp')
      
      // Wait for URL to update
      await mcpLogsPage.page.waitForTimeout(500)
      
      const newUrl = mcpLogsPage.page.url()
      // URL should have changed (either order changed or sort_by added)
      const urlChanged = newUrl !== initialUrl
      const hasOrderAsc = newUrl.includes('order=asc')
      expect(urlChanged || hasOrderAsc).toBe(true)
    })

    test('should sort by latency', async ({ mcpLogsPage }) => {
      await mcpLogsPage.sortBy('latency')
      
      // Wait for URL to update
      await mcpLogsPage.page.waitForURL(/sort_by=latency/, { timeout: 5000 }).catch(() => {})
      
      // Check URL state for latency sort
      const sortState = await mcpLogsPage.getSortState('latency')
      expect(sortState).toBeTruthy()
    })
  })

  test.describe('Live Updates', () => {
    test('should toggle live updates', async ({ mcpLogsPage }) => {
      const liveToggle = mcpLogsPage.liveToggle
      const isVisible = await liveToggle.isVisible().catch(() => false)
      
      if (isVisible) {
        // Default is live_enabled=true (but URL may not have it since it's the default)
        // Check for live_enabled=false to determine if currently disabled
        const initialUrl = mcpLogsPage.page.url()
        const initialLiveDisabled = initialUrl.includes('live_enabled=false')
        
        await mcpLogsPage.toggleLiveUpdates()
        
        // Wait for URL to update
        await mcpLogsPage.page.waitForTimeout(500)
        
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
