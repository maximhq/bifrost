import { Locator, Page, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Page object for the MCP Logs page
 */
export class MCPLogsPage extends BasePage {
  // Main elements
  readonly logsTable: Locator
  readonly filtersSection: Locator
  readonly filtersButton: Locator
  readonly statsCards: Locator

  // Filter elements
  readonly toolNameFilter: Locator
  readonly serverLabelFilter: Locator
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
    this.logsTable = page.locator('[data-testid="mcp-logs-table"]').or(page.locator('table'))
    // The filters section is the container with search input and filters button
    this.filtersSection = page.locator('input[placeholder="Search MCP logs"]').locator('..')
    this.filtersButton = page.getByRole('button', { name: /Filters/i })
    this.statsCards = page.locator('[data-testid="mcp-stats-cards"]').or(
      page.locator('text=Total Executions').locator('..').locator('..')
    )

    // Filter elements - filters are inside a popover opened by the Filters button
    this.toolNameFilter = page.locator('[data-testid="filter-tool-name"]').or(
      page.locator('button').filter({ hasText: /Tool Name/i })
    )
    this.serverLabelFilter = page.locator('[data-testid="filter-server-label"]').or(
      page.locator('button').filter({ hasText: /Server/i })
    )
    this.statusFilter = page.locator('[data-testid="filter-status"]').or(
      page.locator('button').filter({ hasText: /Status/i })
    )
    this.searchInput = page.locator('[data-testid="filter-search"]').or(
      page.getByPlaceholder('Search MCP logs')
    )
    this.dateRangePicker = page.locator('[data-testid="filter-date-range"]').or(
      page.locator('button').filter({ hasText: /Last/i })
    )
    this.liveToggle = page.locator('[data-testid="live-toggle"]').or(
      page.getByRole('button', { name: /Live updates/i })
    )

    // Table elements - exclude status message rows
    this.tableRows = this.logsTable.locator('tbody tr').filter({ hasNot: page.locator('text=Listening for') }).filter({ hasNot: page.locator('text=Live updates paused') }).filter({ hasNot: page.locator('text=Not connected') }).filter({ hasNot: page.locator('text=No results found') })
    // Scope pagination to the MCP logs view (avoid matching other pages when navigating)
    const paginationContainer = page.getByTestId('pagination').filter({ has: page.locator('[data-testid="next-page"]') }).first()
    this.paginationControls = paginationContainer
    this.nextPageBtn = paginationContainer.getByRole('button', { name: 'Next page' }).or(
      paginationContainer.locator('[data-testid="next-page"]')
    )
    this.prevPageBtn = paginationContainer.getByRole('button', { name: 'Previous page' }).or(
      paginationContainer.locator('[data-testid="prev-page"]')
    )

    // Log detail sheet - Sheet component with role="dialog"
    this.logDetailSheet = page.locator('[role="dialog"]')
    this.closeDetailSheetBtn = page.locator('[role="dialog"]').locator('button').filter({ has: page.locator('svg.lucide-x') })
  }

  /**
   * Navigate to the MCP logs page
   */
  async goto(): Promise<void> {
    await this.page.goto('/workspace/mcp-logs')
    await waitForNetworkIdle(this.page)
    // Wait for table to be visible
    await this.logsTable.waitFor({ state: 'visible', timeout: 10000 }).catch(() => {})
  }

  /**
   * Open Filters popover and wait for the command list. Caller can then resolve group/option locators.
   */
  private async openFiltersPopover(): Promise<void> {
    await this.filtersButton.first().waitFor({ state: 'visible' })
    await this.filtersButton.first().click()
    await this.page.waitForSelector('[role="listbox"], [data-slot="command-list"]', { timeout: 5000 })
  }

  /**
   * Close Filters popover (Escape) and wait for network idle.
   */
  private async closeFiltersPopover(): Promise<void> {
    await this.page.keyboard.press('Escape')
    await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 }).catch(() => {})
    await waitForNetworkIdle(this.page)
  }

  /**
   * Get the first selectable option in a filter group by heading (e.g. "Tool Names", "Servers").
   * Skips "Loading..." so we only click real options.
   */
  private async getFirstOptionInGroup(groupHeading: string): Promise<Locator | null> {
    const list = this.page.locator('[data-slot="command-list"]').or(this.page.locator('[role="listbox"]'))
    const group = list.locator('[data-slot="command-group"]').filter({
      has: this.page.getByText(groupHeading, { exact: true }),
    })
    const items = group.locator('[data-slot="command-item"]').or(group.getByRole('option'))
    const count = await items.count()
    for (let i = 0; i < count; i++) {
      const item = items.nth(i)
      const text = await item.textContent().catch(() => '')
      if (text && !/loading/i.test(text)) {
        return item
      }
    }
    return null
  }

  /**
   * Open Filters popover and click an option by name. Returns true if the option was found and clicked.
   */
  private async openFiltersAndSelectOption(optionText: string | RegExp): Promise<boolean> {
    await this.openFiltersPopover()
    const re = typeof optionText === 'string' ? new RegExp(optionText, 'i') : optionText
    const option = this.page.getByRole('option', { name: re })
    const count = await option.count()
    if (count > 0) {
      await option.first().click()
      await this.closeFiltersPopover()
      return true
    }
    await this.closeFiltersPopover()
    return false
  }

  /**
   * Filter by tool name: open Filters and select the first available tool name option.
   * @returns true if at least one tool name option was found and selected
   */
  async filterByToolName(): Promise<boolean> {
    await this.openFiltersPopover()
    const first = await this.getFirstOptionInGroup('Tool Names')
    if (!first) {
      await this.closeFiltersPopover()
      return false
    }
    await first.click()
    await this.closeFiltersPopover()
    return true
  }

  /**
   * Filter by server label: open Filters and select the first available server label option.
   * @returns true if at least one server label option was found and selected
   */
  async filterByServerLabel(): Promise<boolean> {
    await this.openFiltersPopover()
    const first = await this.getFirstOptionInGroup('Servers')
    if (!first) {
      await this.closeFiltersPopover()
      return false
    }
    await first.click()
    await this.closeFiltersPopover()
    return true
  }

  /**
   * Filter by status. Opens Filters popover and toggles the given status option (e.g. success, error).
   * @returns true if the option was found and clicked
   */
  async filterByStatus(status: 'success' | 'error' | 'pending'): Promise<boolean> {
    return this.openFiltersAndSelectOption(status)
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
   * Select time period
   */
  async selectTimePeriod(period: '1h' | '6h' | '24h' | '7d' | '30d'): Promise<void> {
    await this.dateRangePicker.first().click()
    await this.page.waitForSelector('[role="listbox"], [role="menu"]', { timeout: 5000 }).catch(() => {})

    const periodLabels: Record<string, string> = {
      '1h': 'Last hour',
      '6h': 'Last 6 hours',
      '24h': 'Last 24 hours',
      '7d': 'Last 7 days',
      '30d': 'Last 30 days',
    }

    const periodButton = this.page.getByRole('button', { name: periodLabels[period] })
    if (await periodButton.count() > 0) {
      await periodButton.click()
    } else {
      await this.page.keyboard.press('Escape')
    }

    await waitForNetworkIdle(this.page)
  }

  /**
   * Toggle live updates
   */
  async toggleLiveUpdates(): Promise<void> {
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
    await expect(this.logDetailSheet).toBeVisible({ timeout: 5000 })
  }

  /**
   * Close log detail sheet
   */
  async closeLogDetails(): Promise<void> {
    if (await this.logDetailSheet.isVisible()) {
      await this.closeDetailSheetBtn.click().catch(async () => {
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
    const limit = Number.parseInt(params.get('limit') ?? '50', 10) || 50
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
    const limit = Number.parseInt(new URL(this.page.url()).searchParams.get('limit') ?? '50', 10) || 50
    const currentOffset = Number.parseInt(new URL(this.page.url()).searchParams.get('offset') ?? '0', 10)
    const expectedOffset = currentOffset + limit
    await btn.click()
    await this.page.waitForURL((url) => {
      const params = new URL(url).searchParams
      const offset = params.get('offset')
      return offset === String(expectedOffset)
    }, { timeout: 10000 })
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
    const limit = Number.parseInt(new URL(this.page.url()).searchParams.get('limit') ?? '50', 10) || 50
    const currentOffset = Number.parseInt(new URL(this.page.url()).searchParams.get('offset') ?? '0', 10)
    const expectedOffset = Math.max(0, currentOffset - limit)
    await btn.click()
    await this.page.waitForURL((url) => {
      const params = new URL(url).searchParams
      const offset = params.get('offset')
      // When going back to page 1, offset param may be removed (null) or set to "0"
      if (expectedOffset === 0) return offset === null || offset === '0'
      return offset === String(expectedOffset)
    }, { timeout: 10000 })
    await waitForNetworkIdle(this.page)
  }

  /**
   * Sort table by column - clicks the sort button in the column header
   */
  async sortBy(column: 'timestamp' | 'latency'): Promise<void> {
    await this.dismissToasts()

    // Map column names to header button text
    const columnLabels: Record<string, string> = {
      'timestamp': 'Time',
      'latency': 'Latency'
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
    // MCP logs page shows "Total Executions" not "Total Requests"
    const statsText = this.page.locator('text=Total Executions')
    return await statsText.isVisible().catch(() => false)
  }

  /**
   * Check if empty state is shown
   */
  async isEmptyStateVisible(): Promise<boolean> {
    const emptyState = this.page.locator('text=/No logs found/i').or(
      this.page.locator('text=/No data/i')
    )
    return await emptyState.isVisible().catch(() => false)
  }

  /**
   * Get sort state for a column from URL parameters
   * Returns 'asc', 'desc', or null if column is not the current sort column
   */
  async getSortState(column: 'timestamp' | 'latency'): Promise<string | null> {
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
