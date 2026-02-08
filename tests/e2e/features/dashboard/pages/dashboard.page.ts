import { Page, Locator, expect } from '@playwright/test'
import { BasePage } from '../../../core/pages/base.page'
import { waitForNetworkIdle } from '../../../core/utils/test-helpers'

/**
 * Page object for the Dashboard page
 */
export class DashboardPage extends BasePage {
  // Main elements
  readonly pageTitle: Locator
  readonly dateTimePicker: Locator
  
  // Chart cards
  readonly logVolumeChart: Locator
  readonly tokenUsageChart: Locator
  readonly costChart: Locator
  readonly modelUsageChart: Locator
  
  // Chart type toggles
  readonly volumeChartToggle: Locator
  readonly tokenChartToggle: Locator
  readonly costChartToggle: Locator
  readonly modelChartToggle: Locator
  
  // Model filters
  readonly costModelFilter: Locator
  readonly usageModelFilter: Locator

  constructor(page: Page) {
    super(page)
    
    // Main elements
    this.pageTitle = page.getByRole('heading', { name: /Dashboard/i })
    this.dateTimePicker = page.locator('[data-testid="dashboard-date-picker"]')
    
    // Chart cards - using data-testid for robust selectors
    this.logVolumeChart = page.locator('[data-testid="chart-log-volume"]')
    this.tokenUsageChart = page.locator('[data-testid="chart-token-usage"]')
    this.costChart = page.locator('[data-testid="chart-cost"]')
    this.modelUsageChart = page.locator('[data-testid="chart-model-usage"]')
    
    // Chart type toggles - using data-testid with actions suffix
    this.volumeChartToggle = page.locator('[data-testid="chart-log-volume-actions"]').locator('button').filter({ has: page.locator('svg') })
    this.tokenChartToggle = page.locator('[data-testid="chart-token-usage-actions"]').locator('button').filter({ has: page.locator('svg') })
    this.costChartToggle = page.locator('[data-testid="chart-cost-actions"]').locator('button').filter({ has: page.locator('svg') })
    this.modelChartToggle = page.locator('[data-testid="chart-model-usage-actions"]').locator('button').filter({ has: page.locator('svg') })
    
    // Model filters - using data-testid with actions suffix and combobox role
    this.costModelFilter = page.locator('[data-testid="chart-cost-actions"]').locator('[role="combobox"]').first()
    this.usageModelFilter = page.locator('[data-testid="chart-model-usage-actions"]').locator('[role="combobox"]').first()
  }

  /**
   * Navigate to the dashboard page
   */
  async goto(): Promise<void> {
    await this.page.goto('/workspace/dashboard')
    await waitForNetworkIdle(this.page)
    // Wait for charts to load
    await this.waitForChartsToLoad()
  }

  /**
   * Check if dashboard is loaded
   */
  async isLoaded(): Promise<boolean> {
    try {
      await expect(this.pageTitle).toBeVisible({ timeout: 5000 })
      return true
    } catch {
      return false
    }
  }

  /**
   * Close any open popups (date picker, dropdowns, etc.)
   */
  async closePopups(): Promise<void> {
    // Check for open date picker dialog and close it
    const datePickerDialog = this.page.locator('[data-radix-popper-content-wrapper]')
    if (await datePickerDialog.isVisible().catch(() => false)) {
      await this.page.keyboard.press('Escape')
      await datePickerDialog.waitFor({ state: 'hidden', timeout: 2000 }).catch(() => {})
    }
    // Check for open listbox and close it
    const listbox = this.page.locator('[role="listbox"]')
    if (await listbox.isVisible().catch(() => false)) {
      await this.page.keyboard.press('Escape')
      await listbox.waitFor({ state: 'hidden', timeout: 2000 }).catch(() => {})
    }
  }

  /**
   * Select a predefined time period
   */
  async selectTimePeriod(period: '1h' | '6h' | '24h' | '7d' | '30d'): Promise<void> {
    await this.closePopups()
    
    // Click the date picker
    const picker = this.page.locator('button').filter({ hasText: /Last/i }).or(this.page.locator('[data-testid="dashboard-date-picker"]'))
    await picker.first().click()
    
    // Wait for dialog to open
    await this.page.waitForSelector('[data-radix-popper-content-wrapper]', { timeout: 5000 }).catch(() => {})
    
    // Select the period from the preset buttons
    const periodLabels: Record<string, string> = {
      '1h': 'Last hour',
      '6h': 'Last 6 hours',
      '24h': 'Last 24 hours',
      '7d': 'Last 7 days',
      '30d': 'Last 30 days',
    }
    
    await this.page.getByRole('button', { name: periodLabels[period] }).click()
    
    // Wait for dialog to close
    await this.page.locator('[data-radix-popper-content-wrapper]').waitFor({ state: 'hidden', timeout: 5000 }).catch(() => {})
    
    await waitForNetworkIdle(this.page)
    // Wait for URL to update with period parameter
    await this.waitForUrlParam('period', period)
  }

  /**
   * Get the inactive toggle button (the one to click to switch chart type)
   */
  private async getInactiveToggleButton(actionsContainer: Locator): Promise<Locator> {
    const buttons = actionsContainer.locator('button')
    const count = await buttons.count()
    
    // Find the button that is NOT active (doesn't have bg-secondary class or active attribute)
    for (let i = 0; i < count; i++) {
      const btn = buttons.nth(i)
      const className = await btn.getAttribute('class').catch(() => '')
      const hasActive = await btn.evaluate((el) => el.hasAttribute('active')).catch(() => false)
      
      // If button doesn't have active styling, return it
      if (!className?.includes('bg-secondary') && !hasActive) {
        return btn
      }
    }
    
    // Fallback to second button (usually the inactive one)
    return buttons.nth(1)
  }

  /**
   * Toggle chart type for volume chart (clicks inactive button to switch)
   */
  async toggleVolumeChartType(): Promise<void> {
    await this.dismissToasts()
    await this.closePopups()
    const actionsContainer = this.page.locator('[data-testid="chart-log-volume-actions"]')
    const toggleBtn = await this.getInactiveToggleButton(actionsContainer)
    await toggleBtn.waitFor({ state: 'visible' })
    await toggleBtn.click()
    await this.page.waitForLoadState('networkidle').catch(() => {})
  }

  /**
   * Toggle chart type for token chart
   */
  async toggleTokenChartType(): Promise<void> {
    await this.dismissToasts()
    await this.closePopups()
    const actionsContainer = this.page.locator('[data-testid="chart-token-usage-actions"]')
    const toggleBtn = await this.getInactiveToggleButton(actionsContainer)
    await toggleBtn.waitFor({ state: 'visible' })
    await toggleBtn.click()
    await this.page.waitForLoadState('networkidle').catch(() => {})
  }

  /**
   * Toggle chart type for cost chart
   */
  async toggleCostChartType(): Promise<void> {
    await this.dismissToasts()
    await this.closePopups()
    const actionsContainer = this.page.locator('[data-testid="chart-cost-actions"]')
    const toggleBtn = await this.getInactiveToggleButton(actionsContainer)
    await toggleBtn.waitFor({ state: 'visible' })
    await toggleBtn.click()
    await this.page.waitForLoadState('networkidle').catch(() => {})
  }

  /**
   * Toggle chart type for model chart
   */
  async toggleModelChartType(): Promise<void> {
    await this.dismissToasts()
    await this.closePopups()
    const actionsContainer = this.page.locator('[data-testid="chart-model-usage-actions"]')
    const toggleBtn = await this.getInactiveToggleButton(actionsContainer)
    await toggleBtn.waitFor({ state: 'visible' })
    await toggleBtn.click()
    await this.page.waitForLoadState('networkidle').catch(() => {})
  }

  /**
   * Filter cost chart by model
   */
  async filterCostChartByModel(model: string): Promise<void> {
    await this.dismissToasts()
    // Wait for filter to be visible and clickable
    await this.costModelFilter.waitFor({ state: 'visible' })
    await this.costModelFilter.click()
    await this.page.waitForSelector('[role="listbox"]', { timeout: 5000 })
    
    if (model === 'all') {
      await this.page.getByRole('option', { name: 'All Models' }).click()
    } else {
      await this.page.getByRole('option', { name: model }).click()
    }
    
    // Wait for listbox to close and data to refresh
    await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 }).catch(() => {})
    await waitForNetworkIdle(this.page)
  }

  /**
   * Filter usage chart by model
   */
  async filterUsageChartByModel(model: string): Promise<void> {
    await this.dismissToasts()
    // Wait for filter to be visible and clickable
    await this.usageModelFilter.waitFor({ state: 'visible' })
    await this.usageModelFilter.click()
    await this.page.waitForSelector('[role="listbox"]', { timeout: 5000 })
    
    if (model === 'all') {
      await this.page.getByRole('option', { name: 'All Models' }).click()
    } else {
      await this.page.getByRole('option', { name: model }).click()
    }
    
    // Wait for listbox to close and data to refresh
    await this.page.waitForSelector('[role="listbox"]', { state: 'hidden', timeout: 5000 }).catch(() => {})
    await waitForNetworkIdle(this.page)
  }

  /**
   * Check if chart is visible
   */
  async isChartVisible(chartTitle: string): Promise<boolean> {
    // Map chart titles to test IDs
    const testIdMap: Record<string, string> = {
      'Request Volume': 'chart-log-volume',
      'Token Usage': 'chart-token-usage',
      'Cost': 'chart-cost',
      'Model Usage': 'chart-model-usage',
    }
    const testId = testIdMap[chartTitle]
    if (testId) {
      return await this.page.locator(`[data-testid="${testId}"]`).isVisible()
    }
    // Fallback for unknown titles
    const chart = this.page.locator(`text=${chartTitle}`).locator('..').locator('..')
    return await chart.isVisible()
  }

  /**
   * Check if chart is loading
   */
  async isChartLoading(chartTitle: string): Promise<boolean> {
    // Map chart titles to test IDs
    const testIdMap: Record<string, string> = {
      'Request Volume': 'chart-log-volume',
      'Token Usage': 'chart-token-usage',
      'Cost': 'chart-cost',
      'Model Usage': 'chart-model-usage',
    }
    const testId = testIdMap[chartTitle]
    if (testId) {
      const chartCard = this.page.locator(`[data-testid="${testId}"]`)
      const skeleton = chartCard.locator('[data-testid="skeleton"]')
      return await skeleton.isVisible().catch(() => false)
    }
    // Fallback for unknown titles
    const chartCard = this.page.locator(`text=${chartTitle}`).locator('..').locator('..')
    const skeleton = chartCard.locator('[data-testid="skeleton"]').or(chartCard.locator('.skeleton'))
    return await skeleton.isVisible().catch(() => false)
  }

  /**
   * Get URL parameters
   */
  getUrlParams(): URLSearchParams {
    return new URLSearchParams(this.page.url().split('?')[1] || '')
  }

  /**
   * Get chart toggle state (checks aria-pressed, data-state, or active class)
   */
  async getChartToggleState(toggle: Locator): Promise<string | null> {
    // Handle case where toggle might match multiple elements
    const firstToggle = toggle.first()
    
    // Try aria-pressed first (for button toggles)
    const ariaPressed = await firstToggle.getAttribute('aria-pressed').catch(() => null)
    if (ariaPressed) {
      return ariaPressed
    }
    // Try data-state (for switch components)
    const dataState = await firstToggle.getAttribute('data-state').catch(() => null)
    if (dataState) {
      return dataState
    }
    // Check if button is active (has active class or attribute)
    const classAttr = await firstToggle.getAttribute('class').catch(() => null)
    if (classAttr?.includes('bg-secondary')) {
      return 'active'
    }
    // Check for [active] attribute
    const isActive = await firstToggle.evaluate((el) => el.hasAttribute('active')).catch(() => false)
    if (isActive) {
      return 'active'
    }
    return 'inactive'
  }

  /**
   * Get selected model from filter combobox
   */
  async getSelectedModel(filter: Locator): Promise<string | null> {
    const selectedText = await filter.textContent()
    return selectedText
  }
}
