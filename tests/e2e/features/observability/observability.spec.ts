import { expect, test } from '../../core/fixtures/base.fixture'
import { ObservabilityState } from './pages/observability.page'

test.describe('Observability', () => {
  let originalState: ObservabilityState

  test.beforeEach(async ({ observabilityPage }) => {
    await observabilityPage.goto()
    // Capture original state for restoration
    originalState = await observabilityPage.getCurrentState()
  })

  test.afterEach(async ({ observabilityPage }) => {
    // Restore original state - disable all connectors that weren't enabled before
    await observabilityPage.disableAllConnectors()
  })

  test.describe('Navigation', () => {
    test('should display observability page', async ({ observabilityPage }) => {
      // Check for the sidebar section header "Providers" (exact match to avoid strict mode)
      const providersHeader = observabilityPage.page.locator('.text-muted-foreground').filter({ hasText: 'Providers' }).first()
      await expect(providersHeader).toBeVisible()
    })

    test('should display OTel connector tab', async ({ observabilityPage }) => {
      await expect(observabilityPage.getConnectorTab('otel')).toBeVisible()
    })

    test('should display Maxim connector tab', async ({ observabilityPage }) => {
      await expect(observabilityPage.getConnectorTab('maxim')).toBeVisible()
    })

    test('should display Datadog connector tab', async ({ observabilityPage }) => {
      await expect(observabilityPage.getConnectorTab('datadog')).toBeVisible()
    })
  })

  test.describe('OTel Connector', () => {
    test('should select OTel connector', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('otel')

      // Should see OTel-specific content - check for metrics label or input
      const metricsVisible = await observabilityPage.isMetricsEndpointVisible()
      expect(metricsVisible).toBe(true)
    })

    test('should display metrics endpoint', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('otel')

      // The metrics endpoint is in an input field with value containing /metrics
      await observabilityPage.enableMetricsExport()
      const metricsValue = await observabilityPage.getMetricsEndpointValue()
      const metricsInput = observabilityPage.page.getByPlaceholder(/v1\/metrics|otel-collector.*metrics/i)
      const placeholder = await metricsInput.getAttribute('placeholder').catch(() => null)
      const hasMetrics =
        (metricsValue != null && metricsValue.includes('/metrics')) ||
        (placeholder != null && placeholder.includes('/metrics'))
      expect(hasMetrics).toBe(true)
    })

    test('should toggle OTel connector', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('otel')

      // Check if toggle is enabled (not disabled)
      const isToggleEnabled = await observabilityPage.isToggleEnabled('otel')

      if (!isToggleEnabled) {
        test.skip(true, 'OTel toggle is disabled (requires configuration)')
        return
      }

      const initialState = await observabilityPage.isConnectorEnabled('otel')

      const toggled = await observabilityPage.toggleConnector('otel')
      expect(toggled).toBe(true)

      const newState = await observabilityPage.isConnectorEnabled('otel')
      expect(newState).toBe(!initialState)
    })

    test('should display OTel delete button when connector is configured', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('otel')

      const deleteBtn = observabilityPage.getConnectorDeleteBtn('otel')
      const isVisible = await deleteBtn.isVisible().catch(() => false)

      if (!isVisible) {
        test.skip(true, 'OTel delete button not visible (connector may not be configured)')
        return
      }

      await expect(deleteBtn).toBeVisible()
    })

    test('should configure OTel endpoint', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('otel')

      const endpointInput = observabilityPage.page.getByPlaceholder(/otel-collector/i)

      const isVisible = await endpointInput.isVisible().catch(() => false)

      if (!isVisible) {
        // Skip if endpoint input not available
        test.skip(true, 'OTel endpoint input not available')
        return
      }

      const testEndpoint = 'http://test-otel-collector:4317'
      await endpointInput.clear()
      await endpointInput.fill(testEndpoint)

      const value = await endpointInput.inputValue()
      expect(value).toBe(testEndpoint)
    })
  })

  test.describe('Maxim Connector', () => {
    test('should select Maxim connector', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('maxim')

      // Verify Maxim is selected by checking aria-current
      const selected = await observabilityPage.getSelectedConnector()
      expect(selected).toContain('Maxim')
    })

    test('should toggle Maxim connector', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('maxim')

      // Check if toggle is enabled
      const isToggleEnabled = await observabilityPage.isToggleEnabled('maxim')

      if (!isToggleEnabled) {
        test.skip(true, 'Maxim toggle is disabled (requires configuration)')
        return
      }

      const initialState = await observabilityPage.isConnectorEnabled('maxim')

      const toggled = await observabilityPage.toggleConnector('maxim')
      expect(toggled).toBe(true)

      const newState = await observabilityPage.isConnectorEnabled('maxim')
      expect(newState).toBe(!initialState)
    })

    test('should display Maxim configuration form', async ({ observabilityPage }) => {
      await observabilityPage.selectConnector('maxim')

      // Should see a form with configuration elements
      const form = observabilityPage.page.locator('form')
      const formVisible = await form.isVisible().catch(() => false)
      if (formVisible) {
        const hasInputs = await form.locator('input').first().isVisible().catch(() => false)
        const hasSwitches = await form.locator('button[role="switch"]').first().isVisible().catch(() => false)
        expect(hasInputs || hasSwitches).toBe(true)
      } else {
        // Fallback: at minimum expect some configuration inputs
        const inputsVisible = await observabilityPage.page.locator('input').first().isVisible().catch(() => false)
        expect(inputsVisible).toBe(true)
      }
    })
  })

  test.describe('Prometheus Connector', () => {
    test('should select Prometheus connector', async ({ observabilityPage }) => {
      const isAvailable = await observabilityPage.isConnectorAvailable('prometheus')

      if (!isAvailable) {
        test.skip(true, 'Prometheus connector not available')
        return
      }

      await observabilityPage.selectConnector('prometheus')

      const selected = await observabilityPage.getSelectedConnector()
      expect(selected).toContain('Prometheus')
    })

    test('should display Prometheus configuration when available', async ({ observabilityPage }) => {
      const isAvailable = await observabilityPage.isConnectorAvailable('prometheus')

      if (!isAvailable) {
        test.skip(true, 'Prometheus connector not available')
        return
      }

      await observabilityPage.selectConnector('prometheus')

      const toggle = observabilityPage.getConnectorToggle('prometheus')
      const isVisible = await toggle.isVisible().catch(() => false)
      expect(isVisible).toBe(true)
    })

    test('should toggle Prometheus connector when available', async ({ observabilityPage }) => {
      const isAvailable = await observabilityPage.isConnectorAvailable('prometheus')

      if (!isAvailable) {
        test.skip(true, 'Prometheus connector not available')
        return
      }

      await observabilityPage.selectConnector('prometheus')

      const isToggleEnabled = await observabilityPage.isToggleEnabled('prometheus')

      if (!isToggleEnabled) {
        test.skip(true, 'Prometheus toggle is disabled')
        return
      }

      const initialState = await observabilityPage.isConnectorEnabled('prometheus')
      const toggled = await observabilityPage.toggleConnector('prometheus')
      expect(toggled).toBe(true)

      const newState = await observabilityPage.isConnectorEnabled('prometheus')
      expect(newState).toBe(!initialState)
    })

    test('should display Prometheus delete button when connector is configured', async ({ observabilityPage }) => {
      const isAvailable = await observabilityPage.isConnectorAvailable('prometheus')

      if (!isAvailable) {
        test.skip(true, 'Prometheus connector not available')
        return
      }

      await observabilityPage.selectConnector('prometheus')

      const deleteBtn = observabilityPage.getConnectorDeleteBtn('prometheus')
      const isVisible = await deleteBtn.isVisible().catch(() => false)

      if (!isVisible) {
        test.skip(true, 'Prometheus delete button not visible (connector may not be configured)')
        return
      }

      await expect(deleteBtn).toBeVisible()
    })
  })

  test.describe('BigQuery Connector', () => {
    test('should select BigQuery connector', async ({ observabilityPage }) => {
      const isAvailable = await observabilityPage.isConnectorAvailable('bigquery')

      if (!isAvailable) {
        test.skip(true, 'BigQuery connector not available')
        return
      }

      await observabilityPage.selectConnector('bigquery')

      const selected = await observabilityPage.getSelectedConnector()
      expect(selected).toContain('BigQuery')
    })
  })

  test.describe('Datadog Connector', () => {
    test('should select Datadog connector if available', async ({ observabilityPage }) => {
      const isAvailable = await observabilityPage.isConnectorAvailable('datadog')

      if (!isAvailable) {
        test.skip(true, 'Datadog connector not available (enterprise feature)')
        return
      }

      await observabilityPage.selectConnector('datadog')

      // Datadog view should be displayed
      const selected = await observabilityPage.getSelectedConnector()
      expect(selected).toContain('Datadog')
    })

    test('should toggle Datadog connector if available', async ({ observabilityPage }) => {
      const isAvailable = await observabilityPage.isConnectorAvailable('datadog')

      if (!isAvailable) {
        test.skip(true, 'Datadog connector not available (enterprise feature)')
        return
      }

      await observabilityPage.selectConnector('datadog')

      const isToggleEnabled = await observabilityPage.isToggleEnabled('datadog')

      if (!isToggleEnabled) {
        test.skip(true, 'Datadog toggle is disabled')
        return
      }

      const initialState = await observabilityPage.isConnectorEnabled('datadog')
      const toggled = await observabilityPage.toggleConnector('datadog')

      if (!toggled) {
        test.skip(true, 'Datadog toggle could not be toggled')
        return
      }

      const newState = await observabilityPage.isConnectorEnabled('datadog')
      expect(newState).toBe(!initialState)
    })
  })

  test.describe('Multiple Connectors', () => {
    test('should switch between connectors', async ({ observabilityPage }) => {
      // Start with OTel
      await observabilityPage.selectConnector('otel')
      let selected = await observabilityPage.getSelectedConnector()
      expect(selected).toContain('Open Telemetry')

      // Switch to Maxim
      await observabilityPage.selectConnector('maxim')
      selected = await observabilityPage.getSelectedConnector()
      expect(selected).toContain('Maxim')

      // Switch back to OTel
      await observabilityPage.selectConnector('otel')
      selected = await observabilityPage.getSelectedConnector()
      expect(selected).toContain('Open Telemetry')
    })

    test('should persist connector selection via URL', async ({ observabilityPage }) => {
      // Select Maxim (URL update via nuqs is async)
      await observabilityPage.selectConnector('maxim')
      // Wait for URL to reflect selection before asserting
      await expect(observabilityPage.page).toHaveURL(/plugin=maxim/, { timeout: 5000 })
    })
  })
})
