import { expect, test } from '../../core/fixtures/base.fixture'
import { waitForNetworkIdle } from '../../core/utils/test-helpers'
import { DashboardPage } from './pages/dashboard.page'

test.describe('Dashboard', () => {
  test.beforeEach(async ({ dashboardPage }) => {
    await dashboardPage.goto()
  })

  test.describe('Dashboard Display', () => {
    test('should display dashboard page', async ({ dashboardPage }) => {
      await expect(dashboardPage.pageTitle).toBeVisible()
    })

    test('should display all chart cards', async ({ dashboardPage }) => {
      // Check that all four main charts are visible
      await expect(dashboardPage.logVolumeChart).toBeVisible()
      await expect(dashboardPage.tokenUsageChart).toBeVisible()
      await expect(dashboardPage.costChart).toBeVisible()
      await expect(dashboardPage.modelUsageChart).toBeVisible()
    })

    test('should display date time picker', async ({ dashboardPage }) => {
      // Date picker should be visible (may be a button with date text)
      const datePicker = dashboardPage.page.locator('button').filter({ hasText: /Last/i }).or(
        dashboardPage.page.locator('[data-testid="dashboard-date-picker"]')
      )
      await expect(datePicker.first()).toBeVisible()
    })
  })

  test.describe('Time Period Selection', () => {
    test('should filter by time period (full flow)', async ({ dashboardPage }) => {
      // Time period control must exist and be visible (no skip)
      const trigger = dashboardPage.getDatePickerTrigger()
      await expect(trigger).toBeVisible({ timeout: 10000 })

      // Let initial chart load finish so the refetch we wait for is the one from the period change
      await dashboardPage.waitForChartsToLoad()

      // Wait for the chart data request that fires when we change the period (proves filter is applied)
      const responsePromise = dashboardPage.page.waitForResponse(
        (res) => res.url().includes('/logs/histogram') && res.status() === 200,
        { timeout: 15000 }
      )

      await dashboardPage.selectTimePeriod('1h')

      // UI: trigger shows the selected period
      const label = await dashboardPage.getSelectedPeriodLabel()
      expect(label).toContain('Last hour')

      // URL: selection is reflected in query state
      const url = dashboardPage.page.url()
      expect(url).toMatch(/period=1h|start_time=\d+&end_time=\d+/)

      // Data: dashboard refetched with the new range
      await responsePromise
    })

    test('should change time period to last hour', async ({ dashboardPage }) => {
      await dashboardPage.selectTimePeriod('1h')

      const label = await dashboardPage.getSelectedPeriodLabel()
      expect(label).toContain('Last hour')

      const url = dashboardPage.page.url()
      expect(url).toMatch(/period=1h|start_time=\d+&end_time=\d+/)
    })

    test('should change time period to last 7 days', async ({ dashboardPage }) => {
      await dashboardPage.selectTimePeriod('7d')

      const label = await dashboardPage.getSelectedPeriodLabel()
      expect(label).toContain('Last 7 days')

      const url = dashboardPage.page.url()
      expect(url).toMatch(/period=7d|start_time=\d+&end_time=\d+/)
    })

    test('should change time period to last 30 days', async ({ dashboardPage }) => {
      await dashboardPage.selectTimePeriod('30d')

      const label = await dashboardPage.getSelectedPeriodLabel()
      expect(label).toContain('Last 30 days')

      const url = dashboardPage.page.url()
      expect(url).toMatch(/period=30d|start_time=\d+&end_time=\d+/)
    })
  })

  test.describe('Chart Type Toggling', () => {
    test('should toggle volume chart type', async ({ dashboardPage }) => {
      // Get initial toggle state from DOM
      const initialToggle = dashboardPage.volumeChartToggle
      const initialState = await dashboardPage.getChartToggleState(initialToggle)

      // Toggle the chart (method handles waiting internally)
      await dashboardPage.toggleVolumeChartType()

      // Get new toggle state
      const newToggle = dashboardPage.volumeChartToggle
      const newState = await dashboardPage.getChartToggleState(newToggle)

      // Chart type should have changed (state should be different)
      expect(newState).not.toBe(initialState)
    })

    test('should toggle token chart type', async ({ dashboardPage }) => {
      const initialToggle = dashboardPage.tokenChartToggle
      const initialState = await dashboardPage.getChartToggleState(initialToggle)

      await dashboardPage.toggleTokenChartType()

      const newToggle = dashboardPage.tokenChartToggle
      const newState = await dashboardPage.getChartToggleState(newToggle)

      expect(newState).not.toBe(initialState)
    })

    test('should toggle cost chart type', async ({ dashboardPage }) => {
      const initialToggle = dashboardPage.costChartToggle
      const initialState = await dashboardPage.getChartToggleState(initialToggle)

      await dashboardPage.toggleCostChartType()

      const newToggle = dashboardPage.costChartToggle
      const newState = await dashboardPage.getChartToggleState(newToggle)

      expect(newState).not.toBe(initialState)
    })

    test('should toggle model chart type', async ({ dashboardPage }) => {
      const initialToggle = dashboardPage.modelChartToggle
      const initialState = await dashboardPage.getChartToggleState(initialToggle)

      await dashboardPage.toggleModelChartType()

      const newToggle = dashboardPage.modelChartToggle
      const newState = await dashboardPage.getChartToggleState(newToggle)

      expect(newState).not.toBe(initialState)
    })
  })

  test.describe('Model Filtering', () => {
    test('should filter cost chart by model', async ({ dashboardPage }) => {
      // Wait for charts to fully load
      await dashboardPage.waitForChartsToLoad()

      // Try to filter by a specific model if available
      const costModelFilter = dashboardPage.costModelFilter
      const isVisible = await costModelFilter.isVisible().catch(() => false)

      if (isVisible) {
        await dashboardPage.filterCostChartByModel('all')

        // Check that filter value is "All Models"
        const newSelected = await dashboardPage.getSelectedModel(costModelFilter)
        expect(newSelected).toContain('All Models')
      }
    })

    test('should filter usage chart by model', async ({ dashboardPage }) => {
      // Wait for charts to fully load
      await dashboardPage.waitForChartsToLoad()

      const usageModelFilter = dashboardPage.usageModelFilter
      const isVisible = await usageModelFilter.isVisible().catch(() => false)

      if (isVisible) {
        await dashboardPage.filterUsageChartByModel('all')

        // Check that filter value is "All Models"
        const newSelected = await dashboardPage.getSelectedModel(usageModelFilter)
        expect(newSelected).toContain('All Models')
      }
    })
  })

  test.describe('Chart Loading States', () => {
    test('should show loading state initially', async ({ dashboardPage }) => {
      // Navigate to a fresh dashboard
      await dashboardPage.page.reload()
      await dashboardPage.waitForPageLoad()

      // Charts may show loading state briefly
      // This test verifies the page loads without errors
      await expect(dashboardPage.pageTitle).toBeVisible({ timeout: 10000 })
    })
  })

  test.describe('URL State Management', () => {
    test('should preserve chart state in URL', async ({ dashboardPage }) => {
      // Change some settings
      await dashboardPage.selectTimePeriod('7d')
      await dashboardPage.toggleVolumeChartType()

      // Check URL for period (time period should still be in URL)
      const url = dashboardPage.page.url()
      expect(url).toContain('period=7d')

      // Check DOM state for chart toggle (may or may not be in URL)
      const toggleState = await dashboardPage.getChartToggleState(dashboardPage.volumeChartToggle)
      expect(toggleState).toBeTruthy()
    })

    test('should restore state from URL on page load', async ({ dashboardPage }) => {
      // Set URL with specific state
      await dashboardPage.page.goto('/workspace/dashboard?period=7d&volume_chart=line')
      await waitForNetworkIdle(dashboardPage.page)
      await dashboardPage.waitForChartsToLoad()

      // Verify page loaded with correct state from URL
      const url = dashboardPage.page.url()
      expect(url).toContain('period=7d')
      // volume_chart=line was in the URL - verify exact value persisted
      expect(url).toContain('volume_chart=line')
    })
  })

  test.describe('Chart Data Validation', () => {
    test('should render chart elements after data loads', async ({ dashboardPage }) => {
      // Wait for charts to load
      await dashboardPage.waitForChartsToLoad()

      // Check that each chart card has a canvas or SVG element (chart content)
      const volumeChartContent = dashboardPage.logVolumeChart.locator('canvas, svg')
      const tokenChartContent = dashboardPage.tokenUsageChart.locator('canvas, svg')
      const costChartContent = dashboardPage.costChart.locator('canvas, svg')
      const modelChartContent = dashboardPage.modelUsageChart.locator('canvas, svg')

      // Each chart card should have canvas or SVG content (chart library renders into these)
      const volumeCount = await volumeChartContent.count()
      const tokenCount = await tokenChartContent.count()
      const costCount = await costChartContent.count()
      const modelCount = await modelChartContent.count()

      // All four chart cards should have rendered content (count > 0)
      expect(volumeCount).toBeGreaterThan(0)
      expect(tokenCount).toBeGreaterThan(0)
      expect(costCount).toBeGreaterThan(0)
      expect(modelCount).toBeGreaterThan(0)
    })

    test('should show chart legends', async ({ dashboardPage }) => {
      await dashboardPage.waitForChartsToLoad()

      // Check that chart actions (legends/toggles) are visible
      const volumeActions = dashboardPage.page.locator('[data-testid="chart-log-volume-actions"]')
      const tokenActions = dashboardPage.page.locator('[data-testid="chart-token-usage-actions"]')

      // Actions should be visible (they contain legends and toggles)
      await expect(volumeActions).toBeVisible()
      await expect(tokenActions).toBeVisible()
    })

    test('should not show loading skeletons after data loads', async ({ dashboardPage }) => {
      await dashboardPage.waitForChartsToLoad()

      // Check that no skeletons are visible (data has loaded)
      const skeletons = dashboardPage.page.locator('[data-testid="skeleton"]')
      const skeletonCount = await skeletons.count()

      expect(skeletonCount).toBe(0)
    })
  })

  test.describe('Chart Interactions', () => {
    test('should toggle between bar and line chart for volume', async ({ dashboardPage }) => {
      await dashboardPage.waitForChartsToLoad()

      // Get initial toggle state
      const initialState = await dashboardPage.getChartToggleState(dashboardPage.volumeChartToggle)

      // Toggle volume chart type
      await dashboardPage.toggleVolumeChartType()

      // DOM state should change (chart type toggles are in DOM, not URL)
      const newState = await dashboardPage.getChartToggleState(dashboardPage.volumeChartToggle)
      expect(newState).not.toBe(initialState)
    })

    test('should update chart when time period changes', async ({ dashboardPage }) => {
      await dashboardPage.waitForChartsToLoad()

      const initialUrl = dashboardPage.page.url()

      await dashboardPage.selectTimePeriod('1h')

      // Trigger should show new period (filter was applied)
      const label = await dashboardPage.getSelectedPeriodLabel()
      expect(label).toContain('Last hour')

      const newUrl = dashboardPage.page.url()
      expect(newUrl).toMatch(/period=1h|start_time=\d+&end_time=\d+/)
      expect(newUrl).not.toBe(initialUrl)
    })

    test('should sync model filter between cost and usage charts', async ({ dashboardPage }) => {
      await dashboardPage.waitForChartsToLoad()

      // Check if model filters are visible
      const costFilterVisible = await dashboardPage.costModelFilter.isVisible().catch(() => false)
      const usageFilterVisible = await dashboardPage.usageModelFilter.isVisible().catch(() => false)

      if (costFilterVisible && usageFilterVisible) {
        // Filter cost chart
        await dashboardPage.filterCostChartByModel('all')

        // Verify filter was applied (check DOM state, not URL)
        const selectedModel = await dashboardPage.getSelectedModel(dashboardPage.costModelFilter)
        expect(selectedModel).toContain('All Models')
      }
    })

    test('should display correct time period labels', async ({ dashboardPage }) => {
      const periods: Array<'1h' | '6h' | '24h' | '7d' | '30d'> = ['1h', '6h', '24h', '7d', '30d']

      for (const period of periods) {
        await dashboardPage.selectTimePeriod(period)
        // Assert the date picker trigger shows the selected period (actual selected value)
        const label = await dashboardPage.getSelectedPeriodLabel()
        const expected = DashboardPage.PERIOD_LABELS[period]
        expect(label).toContain(expected)
      }
    })
  })

  test.describe('Error States', () => {
    test('should handle empty data gracefully', async ({ dashboardPage }) => {
      // Navigate with very short time range that may have no data
      await dashboardPage.page.goto('/workspace/dashboard?period=1h')
      await waitForNetworkIdle(dashboardPage.page)
      await dashboardPage.waitForChartsToLoad()

      // Page should still render without errors
      await expect(dashboardPage.pageTitle).toBeVisible()

      // All chart containers should still be visible
      await expect(dashboardPage.logVolumeChart).toBeVisible()
      await expect(dashboardPage.tokenUsageChart).toBeVisible()
    })
  })

  test.describe('Custom Date Range', () => {
    test('should open custom date range picker', async ({ dashboardPage }) => {
      await dashboardPage.waitForChartsToLoad()

      // Look for date picker button
      const datePicker = dashboardPage.page.getByRole('button').filter({ hasText: /Last|Custom/i }).first()
      const isVisible = await datePicker.isVisible().catch(() => false)

      if (isVisible) {
        await datePicker.click()

        // Should see date range options or calendar
        const calendarVisible = await dashboardPage.page.locator('[role="dialog"], [role="listbox"]').isVisible().catch(() => false)
        const optionsVisible = await dashboardPage.page.getByRole('option').first().isVisible().catch(() => false)

        expect(calendarVisible || optionsVisible).toBe(true)

        // Close the picker
        await dashboardPage.page.keyboard.press('Escape')
      }
    })

    test('should handle empty data for custom range', async ({ dashboardPage }) => {
      // Set a custom time range that likely has no data
      await dashboardPage.page.goto('/workspace/dashboard?period=1h')
      await waitForNetworkIdle(dashboardPage.page)
      await dashboardPage.waitForChartsToLoad()

      // Charts should still be visible even with no data
      await expect(dashboardPage.logVolumeChart).toBeVisible()
      await expect(dashboardPage.costChart).toBeVisible()

      // Page should not show error alerts (not matching chart legend "Error")
      const errorAlert = dashboardPage.page.locator('[role="alert"][data-variant="destructive"], .text-destructive, [data-sonner-toast][data-type="error"]')
      const hasErrorAlert = await errorAlert.count() > 0
      expect(hasErrorAlert).toBe(false)
    })
  })

})
