import type { Page, Route } from '@playwright/test'

const mockLogsConfig = {
  client_config: {
    drop_excess_requests: false,
    initial_pool_size: 1000,
    prometheus_labels: [],
    enable_logging: true,
    disable_content_logging: false,
    disable_db_pings_in_health: false,
    log_retention_days: 30,
    enforce_auth_on_inference: false,
    allow_direct_keys: true,
    allowed_origins: [],
    allowed_headers: [],
    max_request_body_size_mb: 50,
    enable_litellm_fallbacks: false,
    mcp_agent_depth: 5,
    mcp_tool_execution_timeout: 30,
    mcp_tool_sync_interval: 60,
    async_job_result_ttl: 3600,
    required_headers: [],
    logging_headers: [],
    hide_deleted_virtual_keys_in_filters: false,
    header_filter_config: { allowlist: [], denylist: [] },
  },
  framework_config: {
    id: 1,
    pricing_url: '',
    pricing_sync_interval: 3600,
  },
  is_db_connected: false,
  is_cache_connected: false,
  is_logs_connected: true,
}

const mockMCPConfig = {
  ...mockLogsConfig,
  is_db_connected: true,
}

const mockLogEntry = {
  id: 'log-1',
  timestamp: '2026-03-07T06:00:00.000Z',
  created_at: '2026-03-07T06:00:00.000Z',
  status: 'success',
  object: 'chat.completion',
  provider: 'openai',
  model: 'gpt-4o-mini',
  number_of_retries: 0,
  fallback_index: 0,
  selected_key_id: 'key-1',
  stream: false,
  latency: 14580.9,
  token_usage: {
    prompt_tokens: 120000,
    completion_tokens: 375454,
    total_tokens: 495454,
  },
  cost: 0.0312,
  input_history: [{ role: 'user', content: 'Summarize the recent logs.' }],
  responses_input_history: [],
}

const mockMCPLogEntry = {
  id: 'mcp-log-1',
  timestamp: '2026-03-07T06:00:00.000Z',
  status: 'success',
  tool_name: 'search_docs',
  server_label: 'docs-server',
  latency: 9280.45,
  cost: 0.0185,
}

async function fulfillJson(route: Route, body: unknown): Promise<void> {
  await route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(body),
  })
}

export async function mockLogsOverviewApis(page: Page): Promise<void> {
  await page.route('**/api/config**', async (route) => {
    await fulfillJson(route, mockLogsConfig)
  })

  await page.route('**/api/logs/filterdata**', async (route) => {
    await fulfillJson(route, {
      providers: ['openai'],
      models: ['gpt-4o-mini'],
      statuses: ['success', 'error'],
      virtual_keys: [],
    })
  })

  await page.route('**/api/logs**', async (route) => {
    const pathname = new URL(route.request().url()).pathname

    if (pathname.endsWith('/logs/stats')) {
      await fulfillJson(route, {
        total_requests: 5225,
        success_rate: 94.45,
        average_latency: 14580.9,
        total_tokens: 495454,
        total_cost: 12.3456,
      })
      return
    }

    if (pathname.endsWith('/logs/histogram')) {
      await fulfillJson(route, { buckets: [], bucket_size_seconds: 3600 })
      return
    }

    await fulfillJson(route, {
      logs: [mockLogEntry],
      pagination: { limit: 25, offset: 0, sort_by: 'timestamp', order: 'desc' },
      stats: {
        total_requests: 5225,
        success_rate: 94.45,
        average_latency: 14580.9,
        total_tokens: 495454,
        total_cost: 12.3456,
      },
      has_logs: true,
    })
  })
}

export async function mockMCPLogsOverviewApis(page: Page): Promise<void> {
  await page.route('**/api/config**', async (route) => {
    await fulfillJson(route, mockMCPConfig)
  })

  await page.route('**/api/mcp-logs/filterdata**', async (route) => {
    await fulfillJson(route, { tool_names: ['search_docs'], server_labels: ['docs-server'], virtual_keys: [] })
  })

  await page.route('**/api/mcp-logs**', async (route) => {
    const pathname = new URL(route.request().url()).pathname

    if (pathname.endsWith('/mcp-logs/stats')) {
      await fulfillJson(route, {
        total_executions: 1800,
        success_rate: 97.12,
        average_latency: 9280.45,
        total_cost: 5.6789,
      })
      return
    }

    await fulfillJson(route, {
      logs: [mockMCPLogEntry],
      pagination: { limit: 50, offset: 0, sort_by: 'timestamp', order: 'desc' },
      stats: {
        total_executions: 1800,
        success_rate: 97.12,
        average_latency: 9280.45,
        total_cost: 5.6789,
      },
      has_logs: true,
    })
  })
}
