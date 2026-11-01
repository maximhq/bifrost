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
