import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Page object for the LLM Logs page
 */
export class LogsPage extends BasePage {
  // Main elements
  readonly logsTable: Locator
  readonly filtersSection: Locator
  readonly filtersButton: Locator
  readonly statsCards: Locator

  // Filter elements
  readonly providerFilter: Locator
  readonly modelFilter: Locator
  readonly statusFilter: Locator
  readonly searchInput: Locator
  readonly dateRangePicker: Locator
  readonly liveToggle: Locator

  // Table elements
  readonly tableRows: Locator
  readonly paginationControls: Locator
  readonly nextPageBtn: Locator
  readonly prevPageBtn: Locator

  // Log detail sheet
  readonly logDetailSheet: Locator
  readonly closeDetailSheetBtn: Locator

  constructor(page: Page) {
    super(page)

    // Main elements
    this.logsTable = page.locator('[data-testid="logs-table"]').or(page.locator('table'))
    // The filters section is the container with search input and filters button
    this.filtersSection = page.locator('input[placeholder="Search logs"]').locator('..')
    this.filtersButton = page.getByRole('button', { name: /Filters/i })
    this.statsCards = page.locator('[data-testid="stats-cards"]').or(page.locator('text=Total Requests').locator('..').locator('..'))

    // Filter elements - filters are inside a popover opened by the Filters button
    this.providerFilter = page.locator('[data-testid="filter-provider"]').or(
      page.locator('button').filter({ hasText: /Provider/i })
    )
    this.modelFilter = page.locator('[data-testid="filter-model"]').or(
      page.locator('button').filter({ hasText: /Model/i })
    )
    this.statusFilter = page.locator('[data-testid="filter-status"]').or(
      page.locator('button').filter({ hasText: /Status/i })
    )
    this.searchInput = page.locator('[data-testid="filter-search"]').or(
      page.getByPlaceholder('Search logs')
    )
    this.dateRangePicker = page.locator('[data-testid="filter-date-range"]').or(
      page.locator('button').filter({ hasText: /Last/i })
    )
    this.liveToggle = page.locator('[data-testid="live-toggle"]').or(
      page.getByRole('button', { name: /Live updates/i })
    )

    // Table elements - exclude the "Listening for logs" row which is not a data row
    this.tableRows = this.logsTable.locator('tbody tr').filter({ hasNot: page.locator('text=Listening for logs') }).filter({ hasNot: page.locator('text=Live updates paused') }).filter({ hasNot: page.locator('text=Not connected') }).filter({ hasNot: page.locator('text=No results found') })
    // LLM logs pagination (data-testid added to logsTable.tsx)
    this.paginationControls = page.getByTestId('pagination')
    this.nextPageBtn = page.getByTestId('next-page')
    this.prevPageBtn = page.getByTestId('prev-page')

    // Log detail sheet - Sheet component with role="dialog"
    this.logDetailSheet = page.locator('[role="dialog"]')
    this.closeDetailSheetBtn = page.locator('[role="dialog"]').locator('button').filter({ has: page.locator('svg.lucide-x') })
  }

  /**
   * Navigate to the logs page
   */
  async goto(): Promise<void> {
    await this.page.goto('/workspace/logs')
    await waitForNetworkIdle(this.page)
    // Wait for table or empty state to be visible
    await this.logsTable.or(this.page.locator('text=/No logs found|No results found/i')).waitFor({ state: 'visible', timeout: 10000 })
  }

  /**
   * Navigate to the logs page with a small page size so pagination can be tested with fewer total logs.
   */
  async gotoWithSmallPageSize(limit = 5): Promise<void> {
    await this.page.goto(`/workspace/logs?limit=${limit}&offset=0`)
    await waitForNetworkIdle(this.page)
    await this.logsTable.or(this.page.locator('text=/No logs found|No results found/i')).waitFor({ state: 'visible', timeout: 10000 })
    // The useTablePageSize hook may override the limit from URL, causing a re-render.
    // Wait for pagination to become visible, retrying if the dynamic page size effect causes a brief re-render.
    await this.page.waitForTimeout(1500) // Allow useTablePageSize effect to settle
    await waitForNetworkIdle(this.page)
    await this.paginationControls.waitFor({ state: 'visible', timeout: 10000 })
  }

  /**
   * Filter by provider
   */
  async filterByProvider(provider: string): Promise<void> {
    await this.dismissToasts()
    await this.providerFilter.first().waitFor({ state: 'visible' })
    await this.providerFilter.first().click()
    await this.page.waitForSelector('[role="listbox"]', { timeout: 5000 }).catch(() => {})

    // Try to find the provider option
    const option = this.page.getByRole('option', { name: new RegExp(provider, 'i') })
    if (await option.count() > 0) {
      await option.first().click()
    } else {
      // Close dropdown if option not found
      await this.page.keyboard.press('Escape')
    }

    // Wait for dropdown to close and data to refresh
    await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 }).catch(() => {})
    await waitForNetworkIdle(this.page)
  }

  /**
   * Filter by model
   */
  async filterByModel(model: string): Promise<void> {
    await this.dismissToasts()
    await this.modelFilter.first().waitFor({ state: 'visible' })
    await this.modelFilter.first().click()
    await this.page.waitForSelector('[role="listbox"]', { timeout: 5000 }).catch(() => {})

    const option = this.page.getByRole('option', { name: new RegExp(model, 'i') })
    if (await option.count() > 0) {
      await option.first().click()
    } else {
      await this.page.keyboard.press('Escape')
    }

    await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 }).catch(() => {})
    await waitForNetworkIdle(this.page)
  }

  /**
   * Filter by status. Opens the Filters popover and toggles the given status option (Status group uses lowercase: success, error, etc.).
   */
  async filterByStatus(status: 'success' | 'error' | 'pending'): Promise<void> {
    await this.dismissToasts()
    await this.filtersButton.first().waitFor({ state: 'visible' })
    await this.filtersButton.first().click()
    await this.page.waitForSelector('[role="listbox"], [data-slot="command-list"]', { timeout: 5000 }).catch(() => {})

    const option = this.page.getByRole('option', { name: new RegExp(status, 'i') })
    if (await option.count() > 0) {
      await option.first().click()
    } else {
      await this.page.keyboard.press('Escape')
    }

    await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 }).catch(() => {})
    await waitForNetworkIdle(this.page)
  }

  /**
   * Search logs by content
   */
  async searchLogs(query: string): Promise<void> {
    await this.searchInput.fill(query)
    // Wait for debounced search to trigger network request
    await waitForNetworkIdle(this.page)
  }

  /**
   * Clear search
   */
  async clearSearch(): Promise<void> {
    await this.searchInput.clear()
    await waitForNetworkIdle(this.page)
  }

  /**
   * Select time period. Opens the date range popover, then clicks the predefined period button.
   */
  async selectTimePeriod(period: '1h' | '6h' | '24h' | '7d' | '30d'): Promise<void> {
    await this.dismissToasts()
    const trigger = this.dateRangePicker.first()
    await trigger.waitFor({ state: 'visible' })
    // Open the time period popover by clicking the date range trigger
    await trigger.click()
    const periodLabels: Record<string, string> = {
      '1h': 'Last hour',
      '6h': 'Last 6 hours',
      '24h': 'Last 24 hours',
      '7d': 'Last 7 days',
      '30d': 'Last 30 days',
    }
    const periodButton = this.page.getByRole('button', { name: periodLabels[period] })
    // Wait for popover to open (predefined period button becomes visible)
    await periodButton.waitFor({ state: 'visible', timeout: 5000 })
    await periodButton.click()
    // Wait for popover to close and requests to settle
    await periodButton.waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {})
    await waitForNetworkIdle(this.page)
  }

  /**
   * Toggle live updates
   */
  async toggleLiveUpdates(): Promise<void> {
    await this.dismissToasts()
    await this.liveToggle.first().waitFor({ state: 'visible' })
    await this.liveToggle.first().click()
  }

  /**
   * Click on a log row to view details
   */
  async viewLogDetails(rowIndex: number = 0): Promise<void> {
    const rows = this.tableRows
    const count = await rows.count()

    if (count <= rowIndex) {
      throw new Error(`Row index ${rowIndex} out of bounds (${count} rows available)`)
    }
    await rows.nth(rowIndex).click()
    // Wait for detail sheet to appear
    await expect(this.logDetailSheet).toBeVisible({ timeout: 5000 })
  }

  /**
   * Close log detail sheet
   */
  async closeLogDetails(): Promise<void> {
    if (await this.logDetailSheet.isVisible()) {
      await this.closeDetailSheetBtn.click().catch(async () => {
        // Try pressing Escape if close button not found
        await this.page.keyboard.press('Escape')
      })
      await expect(this.logDetailSheet).not.toBeVisible({ timeout: 5000 })
    }
  }

  /**
   * Get log count from table
   */
  async getLogCount(): Promise<number> {
    return await this.tableRows.count()
  }

  /**
   * Check if log exists in table
   */
  async logExists(searchText: string): Promise<boolean> {
    const row = this.tableRows.filter({ hasText: searchText })
    return await row.count() > 0
  }

  /**
   * Get current 1-based page number from URL (offset/limit).
   */
  getCurrentPageNumber(): number {
    const url = this.page.url()
    const params = new URL(url).searchParams
    const offset = Number.parseInt(params.get('offset') ?? '0', 10)
    const limit = Number.parseInt(params.get('limit') ?? '25', 10) || 25
    return Math.floor(offset / limit) + 1
  }

  /**
   * Navigate to next page (waits for URL to update)
   */
  async goToNextPage(): Promise<void> {
    const btn = this.nextPageBtn.first()
    const isEnabled = await btn.isEnabled().catch(() => false)
    if (!isEnabled) return
    await btn.scrollIntoViewIfNeeded()
    await btn.waitFor({ state: 'visible' })
    const limit = Number.parseInt(new URL(this.page.url()).searchParams.get('limit') ?? '25', 10) || 25
    const currentOffset = Number.parseInt(new URL(this.page.url()).searchParams.get('offset') ?? '0', 10)
    const expectedOffset = currentOffset + limit
    await btn.click()
    await this.page.waitForURL(
      (url) => new URL(url).searchParams.get('offset') === String(expectedOffset),
      { timeout: 10000 }
    )
    await waitForNetworkIdle(this.page)
  }

  /**
   * Navigate to previous page (waits for URL to update)
   */
  async goToPreviousPage(): Promise<void> {
    const btn = this.prevPageBtn.first()
    const isEnabled = await btn.isEnabled().catch(() => false)
    if (!isEnabled) return
    await btn.scrollIntoViewIfNeeded()
    await btn.waitFor({ state: 'visible' })
    const limit = Number.parseInt(new URL(this.page.url()).searchParams.get('limit') ?? '25', 10) || 25
    const currentOffset = Number.parseInt(new URL(this.page.url()).searchParams.get('offset') ?? '0', 10)
    const expectedOffset = Math.max(0, currentOffset - limit)
    await btn.click()
    await this.page.waitForURL(
      (url) => {
        const offset = new URL(url).searchParams.get('offset')
        // When going back to page 1, offset param may be removed (null) or set to "0"
        if (expectedOffset === 0) return offset === null || offset === '0'
        return offset === String(expectedOffset)
      },
      { timeout: 10000 }
    )
    await waitForNetworkIdle(this.page)
  }

  /**
   * Sort table by column - clicks the sort button in the column header
   */
  async sortBy(column: 'timestamp' | 'latency' | 'tokens' | 'cost'): Promise<void> {
    await this.dismissToasts()

    // Map column names to header button text
    const columnLabels: Record<string, string> = {
      'timestamp': 'Time',
      'latency': 'Latency',
      'tokens': 'Tokens',
      'cost': 'Cost'
    }

    const label = columnLabels[column] || column
    // The sortable column headers have a button with the column name
    const sortButton = this.logsTable.getByRole('button', { name: new RegExp(label, 'i') })

    if (await sortButton.count() > 0) {
      await sortButton.first().waitFor({ state: 'visible' })
      await sortButton.first().click()
      await waitForNetworkIdle(this.page)
    }
  }

  /**
   * Check if stats cards are visible
   */
  async areStatsVisible(): Promise<boolean> {
    const statsText = this.page.locator('text=Total Requests')
    return await statsText.isVisible().catch(() => false)
  }

  /**
   * Get stats value
   */
  async getStatValue(statName: string): Promise<string | null> {
    const statCard = this.page.locator(`text=${statName}`).locator('..').locator('..')
    if (await statCard.isVisible()) {
      const value = statCard.locator('.font-mono').or(statCard.locator('text=/\\d+/'))
      return await value.textContent()
    }
    return null
  }

  /**
   * Check if empty state is shown (no logs, or no results for current filters)
   */
  async isEmptyStateVisible(): Promise<boolean> {
    const emptyState = this.page
      .locator('text=/No logs found/i')
      .or(this.page.locator('text=/No data/i'))
      .or(this.page.locator('text=/No results found/i'))
    return await emptyState.isVisible().catch(() => false)
  }

  /**
   * Get sort state for a column from URL parameters
   * Returns 'asc', 'desc', or null if column is not the current sort column
   */
  async getSortState(column: 'timestamp' | 'latency' | 'tokens' | 'cost'): Promise<string | null> {
    const url = this.page.url()
    const urlParams = new URL(url).searchParams
    const sortBy = urlParams.get('sort_by')
    const order = urlParams.get('order')

    // Check if this column is the currently sorted column
    if (sortBy === column) {
      return order || 'desc' // default is desc
    }
    return null
  }
}
