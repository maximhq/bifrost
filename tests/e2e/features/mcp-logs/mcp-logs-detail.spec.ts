import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

const listLog = {
  id: 'mcp-detail-e2e',
  request_id: 'req-mcp-detail-e2e',
  llm_request_id: 'llm-mcp-detail-e2e',
  timestamp: '2026-05-10T10:00:00Z',
  tool_name: 'search_docs',
  server_label: 'docs',
  arguments: 'preview-only',
  latency: 42,
  cost: 0.001,
  status: 'success',
  metadata: {
    source: 'e2e-list',
  },
  created_at: '2026-05-10T10:00:00Z',
}

const detailLog = {
  ...listLog,
  arguments: {
    query: 'hydrated-search-term',
    limit: 5,
  },
  result: {
    answer: 'hydrated-result-value',
    citations: ['doc-1'],
  },
  metadata: {
    source: 'e2e-detail',
  },
}

test.describe('MCP Log Detail Hydration', () => {
  test('opens a row through the detail endpoint and renders hydrated arguments and result', async ({ mcpLogsPage, page }) => {
    let detailRequestCount = 0

    await page.route('**/api/mcp-logs**', async (route) => {
      const requestUrl = new URL(route.request().url())
      const pathname = requestUrl.pathname

      if (pathname === '/api/mcp-logs/mcp-detail-e2e') {
        detailRequestCount += 1
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(detailLog),
        })
        return
      }

      if (pathname === '/api/mcp-logs/stats') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            total_executions: 1,
            success_rate: 100,
            average_latency: 42,
            total_cost: 0.001,
          }),
        })
        return
      }

      if (pathname === '/api/mcp-logs/filterdata') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            tool_names: ['search_docs'],
            server_labels: ['docs'],
            virtual_keys: [],
          }),
        })
        return
      }

      if (pathname === '/api/mcp-logs') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            logs: [listLog],
            pagination: {
              limit: 50,
              offset: 0,
              sort_by: 'timestamp',
              order: 'desc',
              total_count: 1,
            },
            stats: {
              total_executions: 1,
              success_rate: 100,
              average_latency: 42,
              total_cost: 0.001,
            },
            has_logs: true,
          }),
        })
        return
      }

      await route.continue()
    })

    await mcpLogsPage.goto()
    await expect(mcpLogsPage.logsTable).toBeVisible()
    await expect(page.getByText('search_docs')).toBeVisible()

    await mcpLogsPage.viewLogDetails(0)

    await expect
      .poll(() => detailRequestCount, { timeout: 5000, intervals: [100, 200, 500] })
      .toBeGreaterThan(0)
    await expect(mcpLogsPage.logDetailSheet.getByText('Arguments')).toBeVisible()
    await expect(mcpLogsPage.logDetailSheet.getByText('Result')).toBeVisible()
    await expect(page.getByText('hydrated-search-term')).toBeVisible()
    await expect(page.getByText('hydrated-result-value')).toBeVisible()

    // The LLM request ID is shown once, as the header link to the LLM log, not repeated in Request Details.
    await expect(mcpLogsPage.logDetailSheet.getByTestId('mcplogdetails-llm-request-id-link')).toHaveText(listLog.llm_request_id)
    await expect(mcpLogsPage.logDetailSheet.getByText(listLog.llm_request_id, { exact: true })).toHaveCount(1)

    // An ordinary successful execution has nothing to explain, so More details opens on Timings.
    await expect(mcpLogsPage.logDetailSheet.getByTestId('mcplogdetails-status-description')).toHaveCount(0)
  })

  test('opens the sheet from a selected_log link when that log is not on the loaded page', async ({ mcpLogsPage, page }) => {
    // The list returns a different log, as it would when the linked log is
    // older than the default 1h window or has been pushed to a later page.
    const otherLog = { ...listLog, id: 'mcp-other-e2e', request_id: 'req-mcp-other-e2e', tool_name: 'other_tool' }
    let detailRequestCount = 0

    await page.route('**/api/mcp-logs**', async (route) => {
      const pathname = new URL(route.request().url()).pathname

      if (pathname === '/api/mcp-logs/mcp-detail-e2e') {
        detailRequestCount += 1
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(detailLog) })
        return
      }

      if (pathname === '/api/mcp-logs') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            logs: [otherLog],
            pagination: { limit: 50, offset: 50, sort_by: 'timestamp', order: 'desc', total_count: 51 },
            stats: { total_executions: 51, success_rate: 100, average_latency: 42, total_cost: 0.001 },
            has_logs: true,
          }),
        })
        return
      }

      await route.continue()
    })

    await page.goto('/workspace/mcp-logs?offset=50&selected_log=mcp-detail-e2e')
    await expect(mcpLogsPage.logsTable).toBeVisible()

    await expect
      .poll(() => detailRequestCount, { timeout: 5000, intervals: [100, 200, 500] })
      .toBeGreaterThan(0)
    await expect(mcpLogsPage.logDetailSheet).toBeVisible()
    await expect(mcpLogsPage.logDetailSheet.getByText('hydrated-search-term')).toBeVisible()
    await expect(mcpLogsPage.logDetailSheet.getByText('hydrated-result-value')).toBeVisible()
  })
})

const readLogs = [
  { ...listLog, id: 'mcp-read-prompt', request_id: 'req-read-prompt', tool_name: 'review', method: 'prompts/get' },
  { ...listLog, id: 'mcp-read-resource', request_id: 'req-read-resource', tool_name: 'file:///fixture/handbook.md', method: 'resources/read' },
  { ...listLog, id: 'mcp-read-tool', request_id: 'req-read-tool', tool_name: 'search_docs' },
]

test.describe('MCP Log Prompt and Resource Reads', () => {
  test('badges prompt and resource reads, not tool calls, and names the read in the detail sheet', async ({ mcpLogsPage, page }) => {
    await page.route('**/api/mcp-logs**', async (route) => {
      const pathname = new URL(route.request().url()).pathname
      const detail = readLogs.find((log) => pathname === `/api/mcp-logs/${log.id}`)
      if (detail) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(detail) })
        return
      }
      const stats = { total_executions: 3, success_rate: 100, average_latency: 42, total_cost: 0.003 }
      if (pathname === '/api/mcp-logs/stats') {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(stats) })
        return
      }
      if (pathname === '/api/mcp-logs/filterdata') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ tool_names: readLogs.map((log) => log.tool_name), server_labels: ['docs'], virtual_keys: [] }),
        })
        return
      }
      if (pathname === '/api/mcp-logs') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            logs: readLogs,
            pagination: { limit: 50, offset: 0, sort_by: 'timestamp', order: 'desc', total_count: readLogs.length },
            stats,
            has_logs: true,
          }),
        })
        return
      }
      await route.continue()
    })

    await mcpLogsPage.goto()
    await expect(mcpLogsPage.logsTable).toBeVisible()
    const badges = page.getByTestId('mcp-log-read-kind')
    await expect(badges).toHaveCount(2)
    await expect(badges).toHaveText(['Prompt', 'Resource'])

    for (const [row, label] of [[0, 'Prompt'], [1, 'Resource'], [2, 'Tool']] as const) {
      await mcpLogsPage.viewLogDetails(row)
      await expect(mcpLogsPage.logDetailSheet.getByTestId('mcplogdetails-tool-label')).toHaveText(label)
      await mcpLogsPage.closeLogDetails()
    }
  })
})

const ROUND_TRIP = '0123456789abcdef'
const roundTripLogs = [
  {
    ...listLog,
    id: 'mcp-rt-ask',
    request_id: 'req-mcp-rt-ask',
    tool_name: 'ask_environment',
    server_label: 'deploy',
    status: 'input_required',
    round_trip_id: ROUND_TRIP,
    result: { input_requests: { environment: { method: 'elicitation/create', params: { message: 'Which environment?' } } } },
    metadata: {},
  },
  {
    ...listLog,
    id: 'mcp-rt-answer',
    request_id: 'req-mcp-rt-answer',
    tool_name: 'ask_environment',
    server_label: 'deploy',
    round_trip_id: ROUND_TRIP,
    result: 'deployed to staging',
    metadata: { input_responses: { environment: { action: 'accept', content: { environment: 'staging' } } } },
  },
  {
    ...listLog,
    id: 'mcp-rt-busy',
    request_id: 'req-mcp-rt-busy',
    tool_name: 'busy',
    server_label: 'deploy',
    status: 'input_required',
    round_trip_id: 'fedcba9876543210',
    metadata: { retry_later: true },
  },
  {
    ...listLog,
    id: 'mcp-apps-call',
    request_id: 'req-mcp-apps-call',
    tool_name: 'refresh',
    server_label: 'dash',
    result: 'refreshed',
    metadata: { mcp_apps: true, app_only: true, ui_resource_uri: 'ui://dash/view', requested_name: 'refresh' },
  },
]

// stubRoundTripLogs serves roundTripLogs, narrowed by round_trip_id like the API, and records each list request's round_trip_id.
async function stubRoundTripLogs(page: Page, listedRoundTrips: string[]) {
  await page.route('**/api/mcp-logs**', async (route) => {
    const url = new URL(route.request().url())
    const detail = roundTripLogs.find((log) => url.pathname === `/api/mcp-logs/${log.id}`)
    if (detail) {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(detail) })
      return
    }
    const roundTrip = url.searchParams.get('round_trip_id') || ''
    const logs = roundTrip ? roundTripLogs.filter((log) => 'round_trip_id' in log && log.round_trip_id === roundTrip) : roundTripLogs
    const stats = { total_executions: logs.length, success_rate: 100, average_latency: 42, total_cost: 0 }
    if (url.pathname === '/api/mcp-logs/stats') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(stats) })
      return
    }
    if (url.pathname === '/api/mcp-logs/filterdata') {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ tool_names: ['ask_environment', 'busy', 'refresh'], server_labels: ['deploy', 'dash'], virtual_keys: [] }),
      })
      return
    }
    if (url.pathname === '/api/mcp-logs') {
      listedRoundTrips.push(roundTrip)
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          logs,
          pagination: { limit: 50, offset: 0, sort_by: 'timestamp', order: 'desc', total_count: logs.length },
          stats,
          has_logs: true,
        }),
      })
      return
    }
    await route.continue()
  })
}

test.describe('MCP Log Round Trips and MCP Apps', () => {
  test('a paused round shows the question and a Rounds tab that opens the answering retry', async ({ mcpLogsPage, page }) => {
    await stubRoundTripLogs(page, [])
    await mcpLogsPage.goto()
    await expect(mcpLogsPage.logsTable).toBeVisible()

    await mcpLogsPage.viewLogDetails(0)
    const sheet = mcpLogsPage.logDetailSheet
    await expect(sheet.getByText('Needs input').first()).toBeVisible()
    await expect(sheet.getByTestId('mcplogdetails-input-requested')).toContainText('Which environment?')
    await expect(sheet.getByTestId('mcplogdetails-status-description')).toContainText('asked the caller for input')

    const roundsTab = sheet.getByTestId('mcplogdetails-rounds-tab')
    await expect(roundsTab).toContainText('Rounds')
    await expect(roundsTab).toContainText('2')
    await roundsTab.click()
    const rounds = sheet.getByRole('tabpanel').locator('tbody tr')
    await expect(rounds).toHaveCount(2)

    await rounds.nth(1).click()
    await expect(page).toHaveURL(/selected_log=mcp-rt-answer/)
    await expect(page).toHaveURL(new RegExp(`round_trip_id=${ROUND_TRIP}`))
    await expect(mcpLogsPage.logDetailSheet.getByTestId('mcplogdetails-input-answered')).toContainText('staging')
  })

  test('a retry-later round says it asked for no input and has no Rounds tab of its own', async ({ mcpLogsPage, page }) => {
    await stubRoundTripLogs(page, [])
    await mcpLogsPage.goto()
    await expect(mcpLogsPage.logsTable).toBeVisible()

    await mcpLogsPage.viewLogDetails(2)
    const sheet = mcpLogsPage.logDetailSheet
    await expect(sheet.getByTestId('mcplogdetails-retry-later')).toContainText('Retry later')
    await expect(sheet.getByTestId('mcplogdetails-retry-later')).toContainText('without asking for input')
    await expect(sheet.getByTestId('mcplogdetails-input-requested')).toHaveCount(0)
    await expect(sheet.getByTestId('mcplogdetails-rounds-tab')).toHaveCount(0)
  })

  test('a call through MCP Apps shows what Bifrost recorded about it', async ({ mcpLogsPage, page }) => {
    await stubRoundTripLogs(page, [])
    await mcpLogsPage.goto()
    await expect(mcpLogsPage.logsTable).toBeVisible()

    await mcpLogsPage.viewLogDetails(3)
    const sheet = mcpLogsPage.logDetailSheet
    await sheet.getByText('More details').click()
    const apps = sheet.getByTestId('mcplogdetails-mcp-apps')
    for (const marker of ['Caller hosts MCP Apps', 'App-only tool', 'Opens ui://dash/view', 'Called as refresh']) {
      await expect(apps).toContainText(marker)
    }
  })

  test('the sidebar filters by round trip ID and labels every status', async ({ mcpLogsPage, page }) => {
    const listedRoundTrips: string[] = []
    await stubRoundTripLogs(page, listedRoundTrips)
    await mcpLogsPage.goto()
    await expect(mcpLogsPage.logsTable).toBeVisible()

    for (const [status, label] of [['success', 'Success'], ['error', 'Error'], ['processing', 'Processing'], ['cancelled', 'Cancelled'], ['input_required', 'Needs input']] as const) {
      await expect(page.getByTestId(`mcp-status-filter-${status}`)).toHaveText(label)
    }

    await page.getByRole('button', { name: 'Round trip ID' }).click()
    const input = page.getByTestId('mcp-round-trip-id-filter')
    await input.fill(ROUND_TRIP)
    await expect.poll(() => listedRoundTrips.at(-1), { timeout: 5000 }).toBe(ROUND_TRIP)
    await expect(mcpLogsPage.tableRows).toHaveCount(2)

    await expect(page).toHaveURL(new RegExp(`round_trip_id=${ROUND_TRIP}`))

    // Clearing it drops the filter; the unfiltered list may come from cache, so the page is checked, not a request.
    await input.fill('')
    await expect(page).not.toHaveURL(/round_trip_id=/)
    await expect(mcpLogsPage.tableRows).toHaveCount(roundTripLogs.length)
  })
})
