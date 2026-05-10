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
})
