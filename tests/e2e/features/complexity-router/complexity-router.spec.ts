import { expect, test } from '../../core/fixtures/base.fixture'
import type { Page } from '@playwright/test'

// setPhraseSuggestionsFlag answers the dashboard's flag list with the phrase
// suggestions flag set as asked, whatever the gateway's own value is. The
// suggestions dashboard ships behind it, off by default.
async function setPhraseSuggestionsFlag(page: Page, enabled: boolean) {
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    const others = body.flags.filter((flag) => flag.id !== 'phrase-suggestions')
    body.flags = [...others, { id: 'phrase-suggestions', enabled }]
    await route.fulfill({ response, json: body })
  })
}

test.describe('Complexity Router', () => {
  test.describe('Decision Model Picker', () => {
    // The saved config, the providers (OpenRouter, Cloudflare, Perplexity, Ollama and a custom OpenAI-based one),
    // and the model listing are mocked, so the test does not depend on decisions
    // access or on which models the catalog holds. The listing answers with the
    // decision models only when the picker asks for them, which is what proves
    // the picker filters on decisions.
    const CUSTOM_OPENAI = 'my-openai'
    let decisionProvider = 'openai'
    let savedModel = 'gpt-6-luna'
    let decisionModels: string[] = []
    let chatModels: string[] = []
    let modelQueries: URLSearchParams[] = []

    test.beforeEach(async ({ page }) => {
      modelQueries = []
      await page.route(
        (url) => url.pathname === '/api/routing/complexity-analyzer-config',
        async (route) => {
          if (route.request().method() !== 'GET') return route.continue()
          await route.fulfill({
            json: {
              classifier: 'decision',
              keywords: {
                simple_keywords: ['simple'],
                medium_keywords: ['medium'],
                complex_keywords: ['complex'],
              },
              decision: {
                provider: decisionProvider,
                model: savedModel,
                previous_message_count: 2,
                timeout: '3000ms',
                min_confidence: 0.7,
              },
            },
          })
        },
      )
      // The configured providers, plus OpenRouter, Cloudflare, Perplexity, Ollama, Azure, Databricks and a custom provider built on OpenAI.
      await page.route(
        (url) => url.pathname === '/api/providers',
        async (route) => {
          if (route.request().method() !== 'GET') return route.continue()
          const response = await route.fetch()
          const body = await response.json()
          body.providers = [
            ...(body.providers ?? []).filter(
              (p: { name: string }) => ![CUSTOM_OPENAI, 'openrouter', 'cloudflare', 'perplexity', 'ollama', 'azure', 'databricks'].includes(p.name),
            ),
            { name: 'openrouter', provider_status: 'active', keys: [] },
            { name: 'cloudflare', provider_status: 'active', keys: [] },
            { name: 'perplexity', provider_status: 'active', keys: [] },
            { name: 'ollama', provider_status: 'active', keys: [] },
            { name: 'azure', provider_status: 'active', keys: [] },
            { name: 'databricks', provider_status: 'active', keys: [] },
            {
              name: CUSTOM_OPENAI,
              provider_status: 'active',
              keys: [],
              custom_provider_config: { base_provider_type: 'openai', is_key_less: true },
            },
          ]
          await route.fulfill({ response, json: body })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/models',
        async (route) => {
          const params = new URL(route.request().url()).searchParams
          if (params.get('provider') !== decisionProvider) return route.continue()
          modelQueries.push(params)
          const names = params.get('decisions') === 'true' ? decisionModels : chatModels
          const models = names.map((name) => ({ name, provider: decisionProvider }))
          await route.fulfill({ json: { models, total: models.length } })
        },
      )
    })

    // The page polls /api/providers; a poll still inside route.fetch when the page
    // closes would otherwise fail the test.
    test.afterEach(async ({ page }) => {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
    })

    const cases = [
      ...['openai', CUSTOM_OPENAI].map((provider) => ({
        provider,
        saved: 'gpt-6-luna',
        decisions: ['gpt-6-luna', 'gpt-6-luna-2026-09-01'],
        chat: ['gpt-4o'],
      })),
      // OpenRouter lists a chat router under Typesafe's namespace; only the models
      // the datasheet marks as decision models, Clef included, are offered.
      {
        provider: 'openrouter',
        saved: '~typesafe/jev-latest',
        decisions: ['~typesafe/jev-latest', 'typesafe/jev-1.13', 'cloudflare/clef'],
        chat: ['typesafe/jev-router'],
      },
      // Cloudflare's catalog is mostly chat and embedding models; only Clef is offered.
      {
        provider: 'cloudflare',
        saved: '@cf/cloudflare/clef-flash',
        decisions: ['@cf/cloudflare/clef', '@cf/cloudflare/clef-flash'],
        chat: ['@cf/meta/llama-4-scout-17b-16e-instruct'],
      },
      // Perplexity's catalog is its Sonar chat models plus the deciders; only the deciders are offered.
      {
        provider: 'perplexity',
        saved: 'pplx-decider-v1.1-27b',
        decisions: ['pplx-decider-v1-27b', 'pplx-decider-v1.1-27b'],
        chat: ['sonar-pro'],
      },
      // Ollama lists every locally pulled model; only the System One models are offered.
      {
        provider: 'ollama',
        saved: 'tev1:0.8b-q8_0',
        decisions: ['tev1:0.8b-q8_0', 'nimble'],
        chat: ['llama3.1:latest'],
      },
      // Azure lists every deployment; only Microsoft's decision models are offered.
      {
        provider: 'azure',
        saved: 'Microsoft-Decision-1',
        decisions: ['Microsoft-Decision-1'],
        chat: ['gpt-4o'],
      },
      // Databricks' catalog is mostly chat serving endpoints; only ai_decide and the System One models are offered.
      {
        provider: 'databricks',
        saved: 'ai_decide',
        decisions: ['ai_decide', 'system.ai.openjev-qwen35-4b'],
        chat: ['databricks-claude-sonnet-4'],
      },
    ]

    for (const { provider, saved, decisions, chat } of cases) {
      test(`should list only decision models for the ${provider} provider`, async ({ page }) => {
        decisionProvider = provider
        savedModel = saved
        decisionModels = decisions
        chatModels = chat
        await page.goto('/workspace/complexity-router')
        await page.getByTestId('complexity-router-decision-settings-button').click()
        await expect(page.getByTestId('complexity-router-decision-sheet')).toBeVisible({ timeout: 5000 })

        const modelSelect = page.getByTestId('complexity-router-decision-model-select')
        await expect(modelSelect).toBeVisible({ timeout: 10000 })
        await expect(modelSelect).toContainText(saved)

        await modelSelect.click()
        await expect(page.getByRole('option', { name: decisions[decisions.length - 1] })).toBeVisible()
        for (const name of chat) {
          await expect(page.getByRole('option', { name })).toHaveCount(0)
        }
        expect(modelQueries.length).toBeGreaterThan(0)
        expect(modelQueries.every((params) => params.get('decisions') === 'true')).toBe(true)
      })
    }

    test('should show the saved confidence threshold in the model configuration sheet', async ({ page }) => {
      decisionProvider = 'openai'
      savedModel = 'gpt-6-luna'
      decisionModels = ['gpt-6-luna']
      chatModels = ['gpt-4o-mini']
      await page.goto('/workspace/complexity-router')
      await page.getByTestId('complexity-router-decision-settings-button').click()
      await expect(page.getByTestId('complexity-router-decision-sheet')).toBeVisible({ timeout: 5000 })

      await expect(page.getByTestId('complexity-router-decision-sheet')).toContainText('Confidence threshold')
      await expect(page.getByTestId('complexity-router-decision-min-confidence-input')).toHaveValue('0.7')
    })

    // Phrase suggestions are a model router's: the complexity config is saved
    // without a suggestions block, even one it was stored with earlier, which
    // the gateway would refuse.
    test('should save the complexity config without phrase suggestions', async ({ page }) => {
      decisionProvider = 'openai'
      savedModel = 'gpt-6-luna'
      decisionModels = ['gpt-6-luna']
      chatModels = ['gpt-4o-mini']
      const saved: Record<string, unknown>[] = []
      // Registered after beforeEach, so it answers first.
      await page.route(
        (url) => url.pathname === '/api/routing/complexity-analyzer-config',
        async (route) => {
          if (route.request().method() === 'PUT') {
            const body = route.request().postDataJSON()
            saved.push(body)
            return route.fulfill({ json: body })
          }
          await route.fulfill({
            json: {
              classifier: 'decision',
              keywords: { simple_keywords: ['simple'], medium_keywords: ['medium'], complex_keywords: ['complex'] },
              decision: { provider: 'openai', model: 'gpt-6-luna', previous_message_count: 2, timeout: '3000ms', min_confidence: 0.7 },
              // As a gateway from before suggestions moved to routers served it.
              suggestions: {
                enabled: true,
                sample_rate: 0.1,
                lookback_days: 7,
                provider: 'openai',
                model: 'gpt-5',
                tier: { classifier: 'decision', provider: 'typesafe', model: 'jev-latest' },
              },
            },
          })
        },
      )
      await page.goto('/workspace/complexity-router')
      await page.getByTestId('complexity-router-decision-settings-button').click()
      const threshold = page.getByTestId('complexity-router-decision-min-confidence-input')
      await expect(threshold).toHaveValue('0.7', { timeout: 5000 })
      await threshold.fill('0.8')
      await page.getByTestId('complexity-router-decision-sheet-save-button').click()

      await expect.poll(() => saved.length, { timeout: 5000 }).toBe(1)
      expect(saved[0]).not.toHaveProperty('suggestions')
      expect((saved[0].decision as { min_confidence: number }).min_confidence).toBe(0.8)
    })
  })
  test.describe('Phrase Suggestions', () => {
    // Everything the page reads about the router and its suggestions is mocked,
    // so these run against any dev server, with or without real suggestions.
    // Suggestions live on a router's page, so these open a router; its id is
    // the one the suggestion tables key the complexity config's rows by.
    const OPEN = [
      {
        id: 'sug-refund',
        router_id: 'default',
        router_version: 'bank-v2',
        stale: false,
        status: 'open',
        tier: 'SIMPLE',
        phrases: ['where is my refund'],
        category: 'weak',
        reason: 'refund status questions',
        request_count: 214,
        coverage: 0.8,
        nearest_phrase: 'summarize this support thread',
        nearest_tier: 'MEDIUM',
        nearest_similarity: 0.46,
        example_log_ids: ['log-1', 'log-gone'],
        // Where the group's requests route today.
        current_routing: { MEDIUM: 180, none: 34 },
        embedding_signature: 'ns',
        run_id: 'run-1',
        last_seen_at: '2026-11-04T03:00:00Z',
        created_at: '2026-11-04T03:00:00Z',
        updated_at: '2026-11-04T03:00:00Z',
      },
      {
        id: 'sug-migration',
        router_id: 'default',
        router_version: 'bank-v1',
        stale: true,
        status: 'open',
        tier: 'COMPLEX',
        phrases: ['plan a database migration'],
        category: 'uncovered',
        request_count: 12,
        coverage: 1,
        nearest_similarity: 0.21,
        example_log_ids: [],
        current_routing: { none: 12 },
        embedding_signature: 'ns',
        run_id: 'run-1',
        last_seen_at: '2026-11-04T03:00:00Z',
        created_at: '2026-11-04T03:00:00Z',
        updated_at: '2026-11-04T03:00:00Z',
      },
    ]
    const DISMISSED = [{ ...OPEN[1], id: 'sug-old', status: 'dismissed', phrases: ['translate this paragraph'] }]
    const LAST_RUN = {
      id: 'run-1',
      router_id: 'default',
      router_version: 'bank-v2',
      window_start: '2026-10-28T03:00:00Z',
      triggered_by: 'schedule',
      status: 'succeeded',
      requests_read: 900,
      requests_clustered: 900,
      requests_assigned: 0,
      requests_skipped: 0,
      requests_grouped: 640,
      clusters: 30,
      clusters_decided: 3,
      clusters_over_limit: 0,
      clusters_asked: 27,
      clusters_done: 27,
      clusters_failed: 0,
      clusters_covered: 20,
      clusters_incoherent: 5,
      clusters_unreadable: 0,
      created: 2,
      refreshed: 0,
      superseded: 0,
      cost: 0.01,
      started_at: '2026-11-04T03:00:00Z',
      finished_at: '2026-11-04T03:02:00Z',
    }
    const IMPACT = {
      window_seconds: 7 * 24 * 3600,
      requests: 640,
      total: 640,
      tiers: [
        { tier: 'SIMPLE', live: 100, draft: 280, model: { provider: 'openai', model: 'gpt-4.1-mini' } },
        { tier: 'MEDIUM', live: 300, draft: 154, model: { provider: 'openai', model: 'gpt-4.1' } },
        { tier: 'COMPLEX', live: 206, draft: 206 },
        { tier: 'none', live: 34, draft: 0 },
      ],
      changed_count: 214,
      changed: [{ log_id: 'log-1', from: 'MEDIUM', to: 'SIMPLE', phrase: 'where is my refund', similarity: 0.91, text: 'where is my refund for order 12' }],
      attention: [{ phrase: 'where is my refund', tier: 'SIMPLE', from_tier: 'MEDIUM', requests: 9, log_id: 'log-1', text: 'where is my refund for order 12' }],
      live_cost: 4.2,
      draft_cost: 1.05,
      savings: 3.15,
      priced: 200,
      unpriced: 14,
    }
    const CONFIG = {
      id: 'default',
      name: 'Complexity router',
      classifier: 'semantic',
      keywords: { simple_keywords: ['hello'], medium_keywords: ['summarize this support thread'], complex_keywords: ['design a system'] },
      semantic: { provider: 'openai', embedding_model: 'text-embedding-3-small', timeout: '2000ms', min_similarity: 0.4, vector_store: 'embedded', fallback: 'none' },
      // As the gateway returns it: the models a run uses are filled in.
      suggestions: {
        enabled: true,
        sample_rate: 0.1,
        lookback_days: 7,
        schedule: { every: '1d', start_at: '2026-11-12T03:00:00Z' },
        max_clustered_requests: 20000,
        min_cluster_requests: 6,
        provider: 'openai',
        model: 'gpt-4.1-mini',
        tier: { classifier: 'llm', provider: 'openai', model: 'gpt-4.1-mini' },
      },
      // The gateway always returns a router's routing; the router page reads it.
      routing: { enabled: false, scope: 'global', priority: 0 },
    }
    const TIER_PROMPT =
      'Pick the tier by the work asked.\n\nTier definitions:\n- "SIMPLE": short answers\n\n' +
      "Requests this router's administrator assigned to each tier; requests like these belong in that tier:\n" +
      'SIMPLE:\n- hello\nMEDIUM:\n- summarize this support thread\nCOMPLEX:\n- design a system'
    const TIER_CRITERIA = {
      SIMPLE: { definition: 'One short answer.', signals: ['a lookup'], examples: ['hello'] },
      MEDIUM: { definition: 'Routine multi-step work.', signals: ['a draft'], examples: ['summarize this support thread'] },
      COMPLEX: { definition: 'Design with trade-offs.', signals: ['trade-offs'], examples: ['design a system'] },
    }
    let saved: Record<string, unknown>[] = []
    let statusCalls: { id: string; status: string }[] = []
    let runStarts = 0
    let runWindows: (string | null)[] = []
    let listRouters: (string | null)[] = []

    test.beforeEach(async ({ page }) => {
      saved = []
      statusCalls = []
      runStarts = 0
      runWindows = []
      listRouters = []
      await setPhraseSuggestionsFlag(page, true)
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default',
        async (route) => {
          if (route.request().method() === 'PUT') {
            const body = route.request().postDataJSON()
            saved.push(body)
            // A config save leaves the routing as it was.
            return route.fulfill({ json: { ...body, id: 'default', routing: CONFIG.routing } })
          }
          return route.fulfill({ json: CONFIG })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/status',
        (route) =>
          route.fulfill({
            json: {
              state: 'ready',
              loaded: 3,
              total: 3,
              cached_phrases: 3,
              suggestion_default_instructions: 'Write short reference phrases.',
              suggestion_default_tier_instructions: 'Pick the tier by the work asked.',
              // Rendered by the gateway from this router: its own phrases are the examples.
              suggestion_default_tier_prompt: TIER_PROMPT,
              suggestion_default_tier_criteria: TIER_CRITERIA,
            },
          }),
      )
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions',
        (route) => {
          const params = new URL(route.request().url()).searchParams
          const status = params.get('status')
          listRouters.push(new URL(route.request().url()).pathname.split('/')[4]) // the router the call is under
          return route.fulfill({
            json: {
              router_id: 'default',
              router_version: 'bank-v2',
              suggestions: status === 'dismissed' ? DISMISSED : OPEN,
              counts: { open: OPEN.length, dismissed: DISMISSED.length },
              enabled: true,
              last_run: LAST_RUN,
              next_run_at: '2026-11-05T03:00:00Z',
            },
          })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/sug-refund',
        (route) =>
          route.fulfill({
            json: {
              suggestion: OPEN[0],
              examples: [{ log_id: 'log-1', text: 'where is my refund for order 12' }, { log_id: 'log-gone' }],
            },
          }),
      )
      await page.route(
        (url) => /^\/api\/routing\/model-routers\/default\/suggestions\/[^/]+\/status$/.test(url.pathname),
        (route) => {
          const id = new URL(route.request().url()).pathname.split('/')[6]
          const { status } = route.request().postDataJSON()
          statusCalls.push({ id, status })
          return route.fulfill({ json: { ...OPEN[0], id, status } })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/runs',
        (route) => {
          if (route.request().method() !== 'POST') return route.fulfill({ json: { runs: [LAST_RUN] } })
          runStarts++
          runWindows.push(new URL(route.request().url()).searchParams.get('window'))
          return route.fulfill({ status: 202, json: { run_id: '6f1d2c3a-8b4e-4f5a-9c7d-1e2f3a4b5c6d' } })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/runs/estimate',
        (route) => {
          const window = new URL(route.request().url()).searchParams.get('window')
          const requests = window === '24h' ? 1200 : 8400
          return route.fulfill({ json: { requests, clustered: requests, assigned: 0, skipped: 0, max_model_calls: 400 } })
        },
      )
    })

    test.afterEach(async ({ page }) => {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
    })

    test('should hide the Suggestions tab while its feature flag is off', async ({ page }) => {
      // Registered after beforeEach, so it answers first.
      await setPhraseSuggestionsFlag(page, false)
      // The tab is also hidden while the flags are still loading, so the
      // absence below only means something once the off flag has arrived.
      const flags = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/feature-flags')
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await flags
      // The router has loaded, so the tabs it would show are rendered.
      await expect(page.getByTestId('model-router-tab-models')).toBeVisible({ timeout: 10000 })
      await expect(page.getByTestId('model-router-tab-overview')).toHaveAttribute('data-state', 'active')
      await expect(page.getByTestId('complexity-router-tab-suggestions')).toHaveCount(0)
    })

    test('should list pending suggestions with their reasons and examples', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default')
      await expect(page.getByTestId('complexity-router-tab-suggestions-count')).toHaveText('2', { timeout: 10000 })
      await page.getByTestId('complexity-router-tab-suggestions').click()

      const row = page.getByTestId('complexity-suggestion-row-sug-refund')
      await expect(row).toContainText('where is my refund')
      await expect(row).toContainText('Weak match')
      await expect(page.getByTestId('complexity-suggestion-count-sug-refund')).toHaveText('214')
      // Why it is here: where its requests route today.
      await expect(page.getByTestId('complexity-suggestion-split-sug-refund')).toHaveText('180 Medium · 34 no match')
      await expect(page.getByTestId('complexity-suggestion-proposal-sug-refund')).toContainText('Simple')
      await expect(page.getByTestId('complexity-suggestion-accept-sug-refund')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestions-next-run')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestions-last-run')).toContainText('900 analyzed · 2 groups')
      await expect(page.getByTestId('complexity-suggestion-stale-sug-migration')).toContainText('Phrases changed since')
      await expect(page.getByTestId('complexity-suggestion-stale-sug-refund')).toHaveCount(0)
      expect(listRouters.every((id) => id === 'default')).toBe(true)

      await page.getByTestId('complexity-suggestion-expand-sug-refund').click()
      const details = page.getByTestId('complexity-suggestion-details-sug-refund')
      await expect(details).toContainText('summarize this support thread')
      const examples = page.getByTestId('complexity-suggestion-examples-sug-refund')
      await expect(examples).toContainText('where is my refund for order 12')
      await expect(examples).toContainText('1 example is not shown')
      await expect(details).toContainText('Where the proposal comes from')
      await expect(details).toContainText('refund status questions')

      await page.getByTestId('complexity-suggestions-list-dismissed').click()
      await expect(page.getByTestId('complexity-suggestion-row-sug-old')).toContainText('translate this paragraph')
      await expect(page.getByTestId('complexity-suggestion-restore-sug-old')).toBeVisible()
    })

    test('should not offer actions on the last list while the selected one loads', async ({ page }) => {
      // The dismissed list is held until released, so the switch can be seen
      // before it arrives.
      let release: () => void = () => {}
      const held = new Promise<void>((resolve) => {
        release = resolve
      })
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions' && url.searchParams.get('status') === 'dismissed',
        async (route) => {
          await held
          await route.fulfill({
            json: {
              router_id: 'default',
              router_version: 'bank-v2',
              suggestions: DISMISSED,
              counts: { open: OPEN.length, dismissed: DISMISSED.length },
              enabled: true,
              last_run: LAST_RUN,
            },
          })
        },
      )
      await page.goto('/workspace/model-routers?router=default')
      await page.getByTestId('complexity-router-tab-suggestions').click()
      await expect(page.getByTestId('complexity-suggestion-row-sug-refund')).toBeVisible()

      await page.getByTestId('complexity-suggestions-list-dismissed').click()
      // The pending rows are not shown as dismissed ones, with a Restore that would fail.
      await expect(page.getByTestId('complexity-suggestion-restore-sug-refund')).toHaveCount(0)
      await expect(page.getByTestId('complexity-suggestion-row-sug-refund')).toHaveCount(0)
      await expect(page.getByTestId('complexity-suggestions-list-loading')).toBeVisible()

      release()
      await expect(page.getByTestId('complexity-suggestion-restore-sug-old')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestions-list-loading')).toHaveCount(0)
    })

    test('should load more suggestions than one page holds', async ({ page }) => {
      // More pending groups than a page holds; this route, added last, answers
      // before the shared one and serves the requested page.
      const MANY = Array.from({ length: 120 }, (_, i) => ({ ...OPEN[0], id: `sug-many-${i}`, request_count: 500 - i, phrases: [`question ${i}`] }))
      const offsets: number[] = []
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions',
        (route) => {
          const params = new URL(route.request().url()).searchParams
          const limit = Number(params.get('limit') ?? 100)
          const offset = Number(params.get('offset') ?? 0)
          const rows = params.get('status') === 'dismissed' ? [] : MANY
          if (limit > 1) offsets.push(offset)
          return route.fulfill({
            json: {
              router_id: 'default',
              router_version: 'bank-v2',
              suggestions: rows.slice(offset, offset + limit),
              total: rows.length,
              limit,
              offset,
              counts: { open: MANY.length },
              enabled: true,
              last_run: LAST_RUN,
              next_run_at: '2026-11-05T03:00:00Z',
            },
          })
        },
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')

      await expect(page.getByTestId('complexity-suggestion-row-sug-many-99')).toBeVisible({ timeout: 10000 })
      await expect(page.getByTestId('complexity-suggestion-row-sug-many-100')).toHaveCount(0)
      await expect(page.getByTestId('complexity-suggestions-shown')).toHaveText('Showing 100 of 120')

      await page.getByTestId('complexity-suggestions-load-more').click()
      await expect(page.getByTestId('complexity-suggestion-row-sug-many-119')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestion-row-sug-many-0')).toBeVisible()
      // Everything is loaded: no button, no "showing" line.
      await expect(page.getByTestId('complexity-suggestions-list-footer')).toHaveCount(0)
      expect(offsets).toContain(100)
    })

    test('should stage a suggestion, edit it, and accept it when the config is saved', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestion-accept-sug-refund').click()

      // Accepted into the draft: the row stays, marked added, its tier counts
      // it, and the draft bar shows.
      await expect(page.getByTestId('complexity-suggestion-added-sug-refund')).toHaveText('Added · Simple')
      await expect(page.getByTestId('complexity-suggestions-zone-new-simple')).toHaveText('+1 new')
      await expect(page.getByTestId('complexity-suggestions-draft-message')).toHaveText('1 group added from Suggestions. Not live yet.')
      const stagedInput = page.getByTestId('complexity-suggestion-phrase-input-sug-refund')
      await expect(stagedInput).toHaveValue('where is my refund')

      await stagedInput.fill('where is my refund for an order')
      await stagedInput.press('Enter')
      // Staged phrases are part of the configuration draft, saved from its tab.
      await page.getByTestId('model-router-tab-configuration').click()
      await expect(page.getByTestId('complexity-router-footer-note')).toContainText('1 suggested phrase staged')
      await page.getByTestId('complexity-router-save-changes-button').click()

      await expect.poll(() => saved.length).toBe(1)
      const keywords = saved[0].keywords as Record<string, string[]>
      expect(keywords.simple_keywords).toEqual(['hello', 'where is my refund for an order'])
      // The suggestions block rides along unchanged, in the schedule shape the gateway accepts.
      expect(saved[0].suggestions).toMatchObject({ enabled: true, lookback_days: 7, schedule: { every: '1d', start_at: '2026-11-12T03:00:00Z' } })
      expect((saved[0].suggestions as { schedule: Record<string, unknown> }).schedule).not.toHaveProperty('at')
      await expect.poll(() => statusCalls).toEqual([{ id: 'sug-refund', status: 'accepted' }])
    })

    test('should take a staged suggestion back without saving it', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestion-set-tier-sug-migration').click()
      await page.getByTestId('complexity-suggestion-set-tier-sug-migration-medium').click()
      await expect(page.getByTestId('complexity-suggestions-zone-new-medium')).toHaveText('+1 new')
      await expect(page.getByTestId('complexity-suggestion-added-sug-migration')).toHaveText('Added · Medium')

      await page.getByTestId('complexity-suggestion-undo-sug-migration').click()
      await expect(page.getByTestId('complexity-suggestion-added-sug-migration')).toHaveCount(0)
      await expect(page.getByTestId('complexity-suggestion-accept-sug-migration')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestions-draft-bar')).toHaveCount(0)
      await page.getByTestId('model-router-tab-configuration').click()
      await expect(page.getByTestId('complexity-router-save-changes-button')).toBeDisabled()
    })

    test('should add a selection to a tier, and a dragged row to the tier it is dropped on', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      const header = page.getByTestId('complexity-suggestions-list-header')
      await expect(header).toContainText('2 groups')
      await expect(page.getByTestId('complexity-suggestions-bulk-accept')).toHaveText('Accept all proposed (2)')

      await page.getByTestId('complexity-suggestion-select-sug-refund').click()
      await expect(header).toContainText('1 selected')
      await expect(page.getByTestId('complexity-suggestions-bulk-dismiss')).toHaveText('Dismiss 1')
      await expect(page.getByTestId('complexity-suggestions-bulk-accept')).toHaveText('Accept proposed tier (1)')
      await page.getByTestId('complexity-suggestions-bulk-tier-complex').click()
      await expect(page.getByTestId('complexity-suggestion-added-sug-refund')).toHaveText('Added · Complex')
      await expect(page.getByTestId('complexity-suggestions-zone-new-complex')).toHaveText('+1 new')

      await page.getByTestId('complexity-suggestion-row-sug-migration').dragTo(page.getByTestId('complexity-suggestions-zone-simple'))
      await expect(page.getByTestId('complexity-suggestion-added-sug-migration')).toHaveText('Added · Simple')
      await expect(page.getByTestId('complexity-suggestions-draft-message')).toHaveText('2 groups added from Suggestions. Not live yet.')
      await page.getByTestId('model-router-tab-configuration').click()
      await expect(page.getByTestId('complexity-router-footer-note')).toContainText('2 suggested phrases staged')
    })

    test('should dismiss a suggestion and start a run', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestion-dismiss-sug-migration').click()
      await expect.poll(() => statusCalls).toEqual([{ id: 'sug-migration', status: 'dismissed' }])

      await page.getByTestId('complexity-suggestions-run-now-button').click()
      const dialog = page.getByTestId('complexity-suggestions-run-dialog')
      await expect(dialog).toBeVisible()
      // One line of copy; the estimate only gates Start and shows nothing when
      // there is something to run on.
      await expect(dialog).toContainText('Runs in the background inside your gateway, outside the request path.')
      await expect(dialog).not.toContainText('requests in the window')
      await expect(page.getByTestId('complexity-suggestions-run-estimate')).toHaveCount(0)
      const startButton = page.getByTestId('complexity-suggestions-run-start-button')
      await expect(startButton).toBeEnabled()
      // The 7-day lookback offers 24 hours and 7 days, and starts on 7 days.
      await page.getByTestId('complexity-suggestions-run-window-select').click()
      await expect(page.getByTestId('complexity-suggestions-run-window-14d')).toHaveCount(0)
      await page.getByTestId('complexity-suggestions-run-window-24h').click()
      await expect(startButton).toBeEnabled()
      await startButton.click()
      await expect.poll(() => runStarts).toBe(1)
      expect(runWindows).toEqual(['24h'])
      await expect(dialog).toBeHidden()
    })

    test('should show a started run\'s suggestions without a reload', async ({ page }) => {
      const STARTED = '6f1d2c3a-8b4e-4f5a-9c7d-1e2f3a4b5c6d'
      const FRESH = { ...OPEN[0], id: 'sug-fresh', phrases: ['reset my password'], request_count: 9 }
      let listedAfterStart = 0
      // The gateway queues a run before the run records itself, so the lists
      // refetched right after the start (the tab's and the tab count's) still
      // show the previous run.
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions',
        (route) => {
          if (runStarts === 0) return route.fallback()
          listedAfterStart++
          const fresh = listedAfterStart > 2
          return route.fulfill({
            json: {
              router_id: 'default',
              suggestions: fresh ? [...OPEN, FRESH] : OPEN,
              counts: { open: OPEN.length + (fresh ? 1 : 0), dismissed: DISMISSED.length },
              enabled: true,
              last_run: fresh ? { ...LAST_RUN, id: STARTED, triggered_by: 'manual' } : LAST_RUN,
              next_run_at: '2026-11-05T03:00:00Z',
            },
          })
        },
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-run-now-button').click()
      await expect(page.getByTestId('complexity-suggestions-run-start-button')).toBeEnabled()
      await page.getByTestId('complexity-suggestions-run-start-button').click()
      await expect.poll(() => runStarts).toBe(1)
      // Until the run records itself and ends, the card shows it in progress.
      await expect(page.getByTestId('complexity-suggestions-run-progress')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestions-run-now-button')).toHaveCount(0)

      await expect(page.getByTestId('complexity-suggestion-row-sug-fresh')).toBeVisible({ timeout: 15000 })
      await expect(page.getByTestId('complexity-router-tab-suggestions-count')).toHaveText('3')
      await expect(page.getByTestId('complexity-suggestions-run-now-button')).toBeEnabled()
    })

    test('should mark a suggestion that adds two phrases', async ({ page }) => {
      const PAIR = { ...OPEN[0], id: 'sug-pair', phrases: ['translate this to formal german', 'formal german translation please'] }
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions',
        (route) =>
          route.fulfill({
            json: { suggestions: [PAIR], counts: { open: 1, dismissed: 0 }, enabled: true, last_run: LAST_RUN },
          }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      const row = page.getByTestId('complexity-suggestion-row-sug-pair')
      await expect(row).toContainText('translate this to formal german')
      await expect(row).toContainText('formal german translation please')
    })

    test('should decide suggestion tiers with a decision model, sending only the tiers edited', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-settings-button').click()
      const sheet = page.getByTestId('complexity-suggestions-settings-sheet')
      await expect(sheet).toBeVisible()
      // No fallback model to explain away: both steps name their model.
      await expect(sheet).not.toContainText('LLM fallback model')
      await expect(page.getByTestId('complexity-suggestions-phrase-section')).toBeVisible()

      await page.getByTestId('complexity-suggestions-tier-classifier-select').click()
      await page.getByTestId('complexity-suggestions-tier-classifier-option-decision').click()
      await expect(page.getByTestId('complexity-suggestions-tier-provider-select')).toBeVisible()
      // The question shows the shipped text until it is edited.
      const question = page.getByTestId('complexity-suggestions-tier-instructions-input')
      await expect(question).toHaveValue('Pick the tier by the work asked.')
      // The editor is named by its heading.
      await expect(page.getByRole('textbox', { name: 'Question' })).toHaveAttribute('data-testid', 'complexity-suggestions-tier-instructions-input')
      await expect(page.getByTestId('complexity-suggestions-tier-instructions-state')).toHaveText('Default')
      await question.fill('Judge the work each request asks for.')
      await expect(page.getByTestId('complexity-suggestions-tier-instructions-state')).toHaveText('Edited')

      // Each tier's choice shows the router's own phrases as its examples.
      const complex = page.getByTestId('complexity-suggestions-tier-criteria-tier-complex')
      await expect(complex).toContainText('Design with trade-offs.')
      await expect(complex).toContainText('design a system')
      await page.getByTestId('complexity-suggestions-tier-criteria-complex-definition-edit-button').click()
      await page.getByTestId('complexity-suggestions-tier-criteria-complex-definition-input').fill('Multi-step design work.')
      await page.getByTestId('complexity-suggestions-settings-save-button').click()

      await expect.poll(() => saved.length).toBe(1)
      expect(saved[0].suggestions).toMatchObject({
        provider: 'openai',
        model: 'gpt-4.1-mini',
        // The router has no decision model of its own, so the shipped one.
        tier: {
          classifier: 'decision',
          provider: 'typesafe',
          model: 'jev-latest',
          instructions: 'Judge the work each request asks for.',
          // Only the edited tier is frozen; the others stay generated.
          criteria: { COMPLEX: { definition: 'Multi-step design work.', signals: ['trade-offs'], examples: ['design a system'] } },
        },
      })
      const tier = (saved[0].suggestions as { tier: { criteria: Record<string, unknown> } }).tier
      expect(Object.keys(tier.criteria)).toEqual(['COMPLEX'])
    })

    test('should show and edit the whole llm tier prompt, examples included', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-settings-button').click()
      const prompt = page.getByTestId('complexity-suggestions-tier-prompt-input')
      // The editor is named by its heading.
      await expect(page.getByRole('textbox', { name: 'Tier selection prompt' })).toHaveAttribute('data-testid', 'complexity-suggestions-tier-prompt-input')
      // Unedited, the editor holds the generated prompt, examples and all.
      await expect(prompt).toHaveValue(TIER_PROMPT)
      await expect(page.getByTestId('complexity-suggestions-tier-prompt-state')).toHaveText('Default')
      await expect(page.getByTestId('complexity-suggestions-tier-prompt-reset-button')).toHaveCount(0)

      const edited = TIER_PROMPT.replace('MEDIUM:\n- summarize this support thread', 'MEDIUM:\n- summarize this support thread\n- draft a refund reply')
      await prompt.fill(edited)
      await expect(page.getByTestId('complexity-suggestions-tier-prompt-state')).toHaveText('Edited')
      // Back to generated: the editor shows the generated prompt again.
      await page.getByTestId('complexity-suggestions-tier-prompt-reset-button').click()
      await expect(prompt).toHaveValue(TIER_PROMPT)
      await expect(page.getByTestId('complexity-suggestions-tier-prompt-state')).toHaveText('Default')

      await prompt.fill(edited)
      await page.getByTestId('complexity-suggestions-settings-save-button').click()
      await expect.poll(() => saved.length).toBe(1)
      expect(saved[0].suggestions).toMatchObject({ tier: { classifier: 'llm', prompt: edited } })
    })

    test('should not save suggestions turned on without a model to write phrases', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default',
        (route) => {
          if (route.request().method() === 'PUT') return route.fallback()
          const { provider: _p, model: _m, tier: _t, ...rest } = CONFIG.suggestions
          return route.fulfill({ json: { ...CONFIG, suggestions: { ...rest, enabled: false } } })
        },
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-settings-button').click()
      await page.getByTestId('complexity-suggestions-enabled-switch').click()
      await page.getByTestId('complexity-suggestions-settings-save-button').click()
      await expect(page.getByTestId('complexity-suggestions-settings-sheet')).toContainText('Choose the model that writes phrases')
      await expect(page.getByTestId('complexity-suggestions-settings-sheet')).toContainText('Choose the model that decides tiers')
      expect(saved).toHaveLength(0)
    })

    test('should not show or start on an estimate for another window', async ({ page }) => {
      // The 7-day estimate answers; the 24-hour one is held, then fails.
      let release!: () => void
      const held = new Promise<void>((resolve) => (release = resolve))
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/runs/estimate',
        async (route) => {
          if (new URL(route.request().url()).searchParams.get('window') !== '24h') return route.fallback()
          await held
          return route.fulfill({ status: 500, json: { error: { message: 'log store unavailable' } } })
        },
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-run-now-button').click()
      const estimate = page.getByTestId('complexity-suggestions-run-estimate')
      const startButton = page.getByTestId('complexity-suggestions-run-start-button')
      await expect(startButton).toBeEnabled()

      await page.getByTestId('complexity-suggestions-run-window-select').click()
      await page.getByTestId('complexity-suggestions-run-window-24h').click()
      // While the new window is estimated, the run cannot start on the old
      // window's estimate.
      await expect(startButton).toBeDisabled()

      release()
      await expect(estimate).toContainText('Could not estimate the run')
      await expect(startButton).toBeDisabled()
      expect(runStarts).toBe(0)
    })

    test('should not start a run on a window with nothing captured', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/runs/estimate',
        (route) => route.fulfill({ json: { requests: 0, clustered: 0, assigned: 0, skipped: 0, max_model_calls: 0 } }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-run-now-button').click()
      await expect(page.getByTestId('complexity-suggestions-run-estimate')).toContainText('No requests have been captured in this window yet.')
      await expect(page.getByTestId('complexity-suggestions-run-start-button')).toBeDisabled()
    })

    test('should say a first run embeds earlier requests from their logs', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/runs/estimate',
        (route) => route.fulfill({ json: { requests: 1300, clustered: 1000, assigned: 300, skipped: 0, max_model_calls: 30, backfill: 1200 } }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-run-now-button').click()
      await expect(page.getByTestId('complexity-suggestions-run-backfill')).toContainText(
        '1200 earlier requests, from before suggestions were on, are embedded from their logs first.',
      )
      await expect(page.getByTestId('complexity-suggestions-run-start-button')).toBeEnabled()
    })

    test('should say why nothing is captured when the vector store is Chromem', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/status',
        (route) =>
          route.fulfill({
            json: {
              state: 'ready',
              loaded: 3,
              total: 3,
              cached_phrases: 3,
              capture_available: false,
              capture_unavailable_reason: 'embedded_vector_store',
              suggestion_default_instructions: 'Write short reference phrases.',
              suggestion_default_tier_instructions: 'Pick the tier by the work asked.',
              suggestion_default_tier_prompt: TIER_PROMPT,
              suggestion_default_tier_criteria: TIER_CRITERIA,
            },
          }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      const callout = page.getByTestId('complexity-suggestions-capture-unavailable')
      await expect(callout).toContainText('Chromem')
      await expect(callout).toContainText('shared vector store')
      // Nothing to run on, so no Run now.
      await expect(page.getByTestId('complexity-suggestions-run-now-button')).toHaveCount(0)
    })

    test('should say that leaving the semantic classifier turns suggestions off', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=configuration')
      const note = page.getByTestId('complexity-router-footer-note')
      // The classifier cards are folded away until the operator asks to change it.
      await page.getByTestId('model-router-classifier-change-button').click()
      await page.getByTestId('complexity-router-classifier-option-llm').click()
      await expect(note).toContainText('Saving turns phrase suggestions off: they only work with the semantic classifier.')
      // Back on semantic, nothing about suggestions changes.
      await page.getByTestId('complexity-router-classifier-option-semantic').click()
      await expect(note).not.toContainText('phrase suggestions')
    })

    test('should say which groups accept-all could not add', async ({ page }) => {
      // Two groups proposing the same phrase: once the first is staged, the
      // second has nothing new to add.
      const TWIN = { ...OPEN[0], id: 'sug-twin', request_count: 9 }
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions',
        (route) =>
          route.fulfill({
            json: { suggestions: [OPEN[0], TWIN], counts: { open: 2, dismissed: 0 }, enabled: true, last_run: LAST_RUN },
          }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-bulk-accept').click()
      await expect(page.getByTestId('complexity-suggestion-added-sug-refund')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestion-added-sug-twin')).toHaveCount(0)
      await expect(page.getByText('1 group not added: its phrases are already in the phrase lists or the draft.')).toBeVisible()
    })

    test('should leave the tier to the user when the tier model was not confident', async ({ page }) => {
      const UNSURE = { ...OPEN[1], id: 'sug-unsure', tier: 'MEDIUM', confidence: 'low', phrases: ['plan the rollout of this flag'] }
      // Registered after beforeEach, so it answers first.
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions',
        (route) =>
          route.fulfill({
            json: { suggestions: [OPEN[0], UNSURE], counts: { open: 2, dismissed: 0 }, enabled: true, last_run: LAST_RUN },
          }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await expect(page.getByTestId('complexity-suggestion-proposal-sug-unsure')).toContainText('You decide')
      await expect(page.getByTestId('complexity-suggestion-accept-sug-unsure')).toHaveCount(0)
      // Accepting every proposal leaves the undecided group pending.
      await expect(page.getByTestId('complexity-suggestions-bulk-accept')).toHaveText('Accept all proposed (1)')
      await page.getByTestId('complexity-suggestions-bulk-accept').click()
      await expect(page.getByTestId('complexity-suggestion-added-sug-refund')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestion-added-sug-unsure')).toHaveCount(0)
      await page.getByTestId('complexity-suggestion-set-tier-sug-unsure').click()
      await page.getByTestId('complexity-suggestion-set-tier-sug-unsure-complex').click()
      await expect(page.getByTestId('complexity-suggestion-added-sug-unsure')).toHaveText('Added · Complex')
    })

    test("should break down how a run's window became its groups", async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-breakdown-toggle').click()
      const breakdown = page.getByTestId('complexity-suggestions-breakdown')
      await expect(page.getByTestId('complexity-suggestions-breakdown-window')).toContainText('900')
      await expect(page.getByTestId('complexity-suggestions-breakdown-ungrouped')).toContainText('260')
      await expect(page.getByTestId('complexity-suggestions-breakdown-grouped')).toContainText('30 groups')
      await expect(page.getByTestId('complexity-suggestions-breakdown-covered')).toContainText('20 groups')
      await expect(page.getByTestId('complexity-suggestions-breakdown-shown')).toContainText('2 groups')
      // A step that took nothing away is not listed.
      await expect(page.getByTestId('complexity-suggestions-breakdown-skipped')).toHaveCount(0)
      await expect(breakdown).not.toContainText("past this run's limits")
    })

    test("should show a running run's progress and cancel it", async ({ page }) => {
      let cancelled: string | null = null
      const RUNNING = { ...LAST_RUN, id: 'run-live', status: 'running', stage: 'suggesting', clusters_asked: 20, clusters_done: 5, finished_at: undefined }
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions',
        (route) =>
          route.fulfill({
            json: {
              suggestions: OPEN,
              counts: { open: OPEN.length, dismissed: 0 },
              enabled: true,
              last_run: cancelled ? { ...RUNNING, status: 'cancelled', stage: undefined } : RUNNING,
            },
          }),
      )
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/runs/run-live/cancel',
        (route) => {
          cancelled = new URL(route.request().url()).pathname.split('/')[4] // the router the cancel is under
          return route.fulfill({ json: { ...RUNNING, status: 'cancelled' } })
        },
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await expect(page.getByTestId('complexity-suggestions-run-progress')).toContainText('Analyzing the last 7 days')
      await expect(page.getByTestId('complexity-suggestions-run-stage')).toHaveText('Proposing tiers: 5 of 20 groups…')
      await page.getByTestId('complexity-suggestions-cancel-run-button').click()
      await expect.poll(() => cancelled).toBe('default')
      await expect(page.getByTestId('complexity-suggestions-last-run')).toContainText('cancelled', { timeout: 10000 })
      await expect(page.getByTestId('complexity-suggestions-run-progress')).toHaveCount(0)
    })

    test("should review a draft's impact and apply it from the draft bar", async ({ page }) => {
      let replayed: Record<string, string[]> | null = null
      let replayFloor: unknown
      let replays = 0
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/impact',
        (route) => {
          replays++
          replayed = route.request().postDataJSON().phrases
          replayFloor = route.request().postDataJSON().min_similarity
          return route.fulfill({ json: IMPACT })
        },
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestion-accept-sug-refund').click()
      await page.getByTestId('complexity-suggestions-draft-review').click()

      const impact = page.getByTestId('complexity-suggestions-impact')
      await expect(impact).toBeVisible()
      await expect(page).toHaveURL(/view=impact/)
      // The staged phrase is sent, keyed by its tier; the gateway adds it to
      // the router's saved phrases for the replay.
      await expect.poll(() => replayed).toEqual({ SIMPLE: ['where is my refund'] })
      await expect(page.getByTestId('complexity-suggestions-impact-summary')).toContainText('214 requests (33.4%) would change tier.')
      // One draft, one replay: an answer must not set off another request.
      await page.waitForTimeout(1500)
      expect(replays).toBe(1)
      await expect(page.getByTestId('complexity-suggestions-impact-savings')).toContainText('$3.15')
      await expect(page.getByTestId('complexity-suggestions-impact-mix-simple')).toContainText('+180')
      await expect(page.getByTestId('complexity-suggestions-impact-attention')).toContainText('9 requests may have moved to Simple by mistake')
      await page.getByTestId('complexity-suggestions-impact-pricing-toggle').click()
      await expect(page.getByTestId('complexity-suggestions-impact-pricing')).toContainText('openai/gpt-4.1-mini')
      await expect(page.getByTestId('complexity-suggestions-impact-pricing')).toContainText('No model set for this tier')

      await page.getByTestId('complexity-suggestions-draft-apply').click()
      await expect.poll(() => saved.length).toBe(1)
      expect((saved[0].keywords as Record<string, string[]>).simple_keywords).toEqual(['hello', 'where is my refund'])
      await expect.poll(() => statusCalls).toEqual([{ id: 'sug-refund', status: 'accepted' }])
      await expect(page.getByTestId('complexity-suggestions-draft-bar')).toHaveCount(0)
      // Applied: the impact of a draft that is now live has nothing to show.
      await expect(page).not.toHaveURL(/view=impact/)
      // The draft is replayed at the similarity floor it would save.
      expect(replayFloor).toBe(0.4)
    })

    test('should keep the impact view and say why when applying the draft fails', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/impact',
        (route) => route.fulfill({ json: IMPACT }),
      )
      // Registered last, so it answers saves first; reads fall through.
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default',
        (route) =>
          route.request().method() === 'PUT'
            ? route.fulfill({ status: 500, json: { is_bifrost_error: false, error: { message: 'the config store is unavailable' } } })
            : route.fallback(),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestion-accept-sug-refund').click()
      await page.getByTestId('complexity-suggestions-draft-review').click()
      await expect(page.getByTestId('complexity-suggestions-impact-summary')).toBeVisible()

      await page.getByTestId('complexity-suggestions-draft-apply').click()
      await expect(page.getByTestId('complexity-suggestions-draft-error')).toContainText(/the config store is unavailable/i)
      await expect(page.getByTestId('complexity-suggestions-impact')).toBeVisible()
      await expect(page).toHaveURL(/view=impact/)
      await expect(page.getByTestId('complexity-suggestions-draft-bar')).toBeVisible()
    })

    test('should show the list for an impact link with nothing staged, and the impact once a group is', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/impact',
        (route) => route.fulfill({ json: IMPACT }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions&view=impact')
      // Nothing staged: the list, not an impact view with nothing to replay.
      await expect(page.getByTestId('complexity-suggestions-tab')).toBeVisible({ timeout: 10000 })
      await expect(page.getByTestId('complexity-suggestions-impact')).toHaveCount(0)
      await expect(page).toHaveURL(/view=impact/)

      // The view is kept, so staging a group opens it.
      await page.getByTestId('complexity-suggestion-accept-sug-refund').click()
      await expect(page.getByTestId('complexity-suggestions-impact-summary')).toBeVisible()
    })

    test('should open the group a flagged phrase came from', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/suggestions/impact',
        (route) => route.fulfill({ json: IMPACT }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestion-accept-sug-refund').click()
      await page.getByTestId('complexity-suggestions-draft-review').click()
      await page.getByTestId('complexity-suggestions-impact-review-group').click()

      await expect(page).not.toHaveURL(/view=impact/)
      await expect(page.getByTestId('complexity-suggestion-details-sug-refund')).toBeVisible()
      await expect(page.getByTestId('complexity-suggestion-details-sug-migration')).toHaveCount(0)
    })

    test('should say when the tier choices cannot be loaded', async ({ page }) => {
      await page.route(
        (url) => url.pathname === '/api/routing/model-routers/default/status',
        (route) => route.fulfill({ status: 500, json: { is_bifrost_error: false, error: { message: 'status unavailable' } } }),
      )
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-settings-button').click()
      await page.getByTestId('complexity-suggestions-tier-classifier-select').click()
      await page.getByTestId('complexity-suggestions-tier-classifier-option-decision').click()
      await expect(page.getByTestId('complexity-suggestions-tier-criteria-unavailable')).toBeVisible()
    })

    test('should save an interval and a first run from the settings sheet', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-settings-button').click()
      await expect(page.getByTestId('complexity-suggestions-settings-sheet')).toBeVisible()

      await page.getByTestId('complexity-suggestions-every-count-input').fill('2')
      await page.getByTestId('complexity-suggestions-every-unit-select').click()
      await page.getByTestId('complexity-suggestions-every-unit-option-w').click()
      // Cluster sizes are not edited in the UI.
      await expect(page.getByTestId('complexity-suggestions-max-clustered-input')).toHaveCount(0)
      await expect(page.getByTestId('complexity-suggestions-min-cluster-input')).toHaveCount(0)
      const firstRun = '2030-12-02T09:30'
      await page.getByTestId('complexity-suggestions-start-at-input').fill(firstRun)
      // A first run still ahead is the next run.
      await expect(page.getByTestId('complexity-suggestions-schedule-next-run')).toContainText('2030')
      // The field is in the browser's local time; the gateway gets UTC.
      const startAt = await page.evaluate((local) => new Date(local).toISOString().replace('.000Z', 'Z'), firstRun)
      await page.getByTestId('complexity-suggestions-settings-save-button').click()

      await expect.poll(() => saved.length).toBe(1)
      expect(saved[0].suggestions).toMatchObject({
        enabled: true,
        schedule: { every: '2w', start_at: startAt },
        // Set outside the UI, they ride along unchanged.
        max_clustered_requests: 20000,
        min_cluster_requests: 6,
      })
    })

    test('should tie each settings error to its field for assistive tech', async ({ page }) => {
      await page.goto('/workspace/model-routers?router=default&tab=suggestions')
      await page.getByTestId('complexity-suggestions-settings-button').click()
      await expect(page.getByTestId('complexity-suggestions-settings-sheet')).toBeVisible()
      // Each prompt box has a name of its own, not only a heading beside it.
      await expect(page.getByRole('textbox', { name: 'Phrase generation prompt' })).toHaveAttribute(
        'data-testid',
        'complexity-suggestions-instructions-input',
      )

      const sample = page.getByTestId('complexity-suggestions-sample-input')
      const every = page.getByTestId('complexity-suggestions-every-count-input')
      const lookback = page.getByTestId('complexity-suggestions-lookback-input')
      await sample.fill('0')
      // Days go up to 30; past that the gateway wants weeks.
      await every.fill('31')
      await page.getByTestId('complexity-suggestions-settings-save-button').click()

      for (const [input, id] of [
        [sample, 'suggestions-sample-error'],
        [every, 'suggestions-every-error'],
      ] as const) {
        await expect(input).toHaveAttribute('aria-invalid', 'true')
        await expect(input).toHaveAttribute('aria-describedby', id)
        await expect(page.locator(`#${id}`)).toBeVisible()
      }
      // A valid field carries neither attribute.
      await expect(lookback).not.toHaveAttribute('aria-invalid')
      await expect(lookback).not.toHaveAttribute('aria-describedby')
      expect(saved).toHaveLength(0)

      // Fixed, the attributes go with the message.
      await sample.fill('10')
      await every.fill('30')
      await expect(sample).not.toHaveAttribute('aria-invalid')
      await expect(sample).not.toHaveAttribute('aria-describedby')
      await expect(page.locator('#suggestions-sample-error')).toHaveCount(0)
      await expect(every).not.toHaveAttribute('aria-invalid')
    })
  })
})
