import { expect, test } from '../../core/fixtures/base.fixture'

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
  })
  test.describe('Phrase Suggestions', () => {
    // Everything the page reads about the router and its suggestions is mocked,
    // so these run against any dev server, with or without real suggestions.
    const OPEN = [
      {
        id: 'sug-refund',
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
        embedding_signature: 'ns',
        run_id: 'run-1',
        last_seen_at: '2026-11-04T03:00:00Z',
        created_at: '2026-11-04T03:00:00Z',
        updated_at: '2026-11-04T03:00:00Z',
      },
      {
        id: 'sug-migration',
        status: 'open',
        tier: 'COMPLEX',
        phrases: ['plan a database migration'],
        category: 'uncovered',
        request_count: 12,
        coverage: 1,
        nearest_similarity: 0.21,
        example_log_ids: [],
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
      triggered_by: 'schedule',
      status: 'succeeded',
      requests_read: 900,
      clusters: 30,
      created: 2,
      refreshed: 0,
      superseded: 0,
      cost: 0.01,
      started_at: '2026-11-04T03:00:00Z',
      finished_at: '2026-11-04T03:02:00Z',
    }
    const CONFIG = {
      classifier: 'semantic',
      keywords: { simple_keywords: ['hello'], medium_keywords: ['summarize this support thread'], complex_keywords: ['design a system'] },
      semantic: { provider: 'openai', embedding_model: 'text-embedding-3-small', timeout: '2000ms', min_similarity: 0.4, vector_store: 'embedded', fallback: 'none' },
      suggestions: { enabled: true, sample_rate: 0.1, lookback_days: 7, schedule: { every: '1d', at: '03:00', timezone: 'UTC' }, max_clustered_requests: 20000, min_cluster_requests: 6 },
    }
    let saved: Record<string, unknown>[] = []
    let statusCalls: { id: string; status: string }[] = []
    let runStarts = 0

    test.beforeEach(async ({ page }) => {
      saved = []
      statusCalls = []
      runStarts = 0
      await page.route(
        (url) => url.pathname === '/api/routing/complexity-analyzer-config',
        async (route) => {
          if (route.request().method() === 'PUT') {
            const body = route.request().postDataJSON()
            saved.push(body)
            return route.fulfill({ json: body })
          }
          return route.fulfill({ json: CONFIG })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/routing/complexity-analyzer-status',
        (route) =>
          route.fulfill({
            json: { state: 'ready', loaded: 3, total: 3, cached_phrases: 3, suggestion_default_instructions: 'Write short reference phrases.' },
          }),
      )
      await page.route(
        (url) => url.pathname === '/api/routing/complexity-suggestions',
        (route) => {
          const status = new URL(route.request().url()).searchParams.get('status')
          return route.fulfill({
            json: {
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
        (url) => url.pathname === '/api/routing/complexity-suggestions/sug-refund',
        (route) =>
          route.fulfill({
            json: {
              suggestion: OPEN[0],
              examples: [{ log_id: 'log-1', text: 'where is my refund for order 12' }, { log_id: 'log-gone' }],
            },
          }),
      )
      await page.route(
        (url) => /^\/api\/routing\/complexity-suggestions\/[^/]+\/status$/.test(url.pathname),
        (route) => {
          const id = new URL(route.request().url()).pathname.split('/')[4]
          const { status } = route.request().postDataJSON()
          statusCalls.push({ id, status })
          return route.fulfill({ json: { ...OPEN[0], id, status } })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/routing/complexity-suggestions/runs',
        (route) => {
          if (route.request().method() !== 'POST') return route.fulfill({ json: { runs: [LAST_RUN] } })
          runStarts++
          return route.fulfill({ status: 202, json: { run_id: 'complexity_suggestions-manual-1' } })
        },
      )
      await page.route(
        (url) => url.pathname === '/api/routing/complexity-suggestions/test',
        (route) => {
          const body = route.request().postDataJSON()
          return route.fulfill({
            json: {
              phrases: body.phrases.map((p: { tier: string; text: string }) => ({
                ...p,
                nearest_existing: { tier: 'MEDIUM', phrase: 'summarize this support thread', similarity: 0.46, accepted: true, candidate: false },
              })),
              examples: [
                {
                  log_id: 'log-1',
                  text: 'where is my refund for order 12',
                  current: { tier: 'MEDIUM', phrase: 'summarize this support thread', similarity: 0.46, accepted: true, candidate: false },
                  with_phrases: { tier: body.phrases[0].tier, phrase: body.phrases[0].text, similarity: 0.91, accepted: true, candidate: true },
                  changed: true,
                },
              ],
              taken_over: 1,
            },
          })
        },
      )
    })

    test.afterEach(async ({ page }) => {
      await page.unrouteAll({ behavior: 'ignoreErrors' })
    })

    test('should list pending suggestions with their reasons and examples', async ({ page }) => {
      await page.goto('/workspace/complexity-router')
      await expect(page.getByTestId('complexity-router-tab-suggestions-count')).toHaveText('2', { timeout: 10000 })
      await page.getByTestId('complexity-router-tab-suggestions').click()

      const row = page.getByTestId('complexity-suggestion-row-sug-refund')
      await expect(row).toContainText('where is my refund')
      await expect(row).toContainText('Weak match')
      await expect(row).toContainText('214 requests')
      await expect(page.getByTestId('complexity-suggestions-next-run')).toBeVisible()

      await page.getByTestId('complexity-suggestion-expand-sug-refund').click()
      const details = page.getByTestId('complexity-suggestion-details-sug-refund')
      await expect(details).toContainText('summarize this support thread')
      const examples = page.getByTestId('complexity-suggestion-examples-sug-refund')
      await expect(examples).toContainText('where is my refund for order 12')
      await expect(examples).toContainText('1 example is not shown')

      await page.getByTestId('complexity-suggestions-search-input').fill('migration')
      await expect(page.getByTestId('complexity-suggestion-row-sug-refund')).toHaveCount(0)
      await expect(page.getByTestId('complexity-suggestion-row-sug-migration')).toBeVisible()

      await page.getByTestId('complexity-suggestions-list-dismissed').click()
      await expect(page.getByTestId('complexity-suggestion-row-sug-old')).toHaveCount(0)
      await page.getByTestId('complexity-suggestions-search-input').fill('')
      await expect(page.getByTestId('complexity-suggestion-row-sug-old')).toContainText('translate this paragraph')
    })

    test('should stage a suggestion, edit it, and accept it when the config is saved', async ({ page }) => {
      await page.goto('/workspace/complexity-router?tab=suggestions')
      await page.getByTestId('complexity-suggestion-add-sug-refund').click()

      // Staged: gone from the list, shown in the staged panel, and the save is pending.
      await expect(page.getByTestId('complexity-suggestion-row-sug-refund')).toHaveCount(0)
      const stagedInput = page.getByTestId('complexity-suggestions-staged-input-sug-refund-0')
      await expect(stagedInput).toHaveValue('where is my refund')
      await expect(page.getByTestId('complexity-router-footer-note')).toContainText('1 suggested phrase staged')

      await stagedInput.fill('where is my refund for an order')
      await stagedInput.press('Enter')
      await page.getByTestId('complexity-router-save-changes-button').click()

      await expect.poll(() => saved.length).toBe(1)
      const keywords = saved[0].keywords as Record<string, string[]>
      expect(keywords.simple_keywords).toEqual(['hello', 'where is my refund for an order'])
      // The suggestions block rides along unchanged.
      expect(saved[0].suggestions).toMatchObject({ enabled: true, lookback_days: 7, schedule: { every: '1d', at: '03:00', timezone: 'UTC' } })
      await expect.poll(() => statusCalls).toEqual([{ id: 'sug-refund', status: 'accepted' }])
    })

    test('should take a staged suggestion back without saving it', async ({ page }) => {
      await page.goto('/workspace/complexity-router?tab=suggestions')
      await page.getByTestId('complexity-suggestion-tier-select-sug-migration').click()
      await page.getByRole('option', { name: 'Medium' }).click()
      await page.getByTestId('complexity-suggestion-add-sug-migration').click()
      await expect(page.getByTestId('complexity-suggestions-staged-medium')).toContainText('+1 new')

      await page.getByTestId('complexity-suggestions-staged-undo-sug-migration').click()
      await expect(page.getByTestId('complexity-suggestion-row-sug-migration')).toBeVisible()
      await expect(page.getByTestId('complexity-router-save-changes-button')).toBeDisabled()
    })

    test('should dismiss a suggestion and start a run', async ({ page }) => {
      await page.goto('/workspace/complexity-router?tab=suggestions')
      await page.getByTestId('complexity-suggestion-dismiss-sug-migration').click()
      await expect.poll(() => statusCalls).toEqual([{ id: 'sug-migration', status: 'dismissed' }])

      await page.getByTestId('complexity-suggestions-run-now-button').click()
      await expect.poll(() => runStarts).toBe(1)
    })

    test('should test an edited phrase before staging it', async ({ page }) => {
      await page.goto('/workspace/complexity-router?tab=suggestions')
      await page.getByTestId('complexity-suggestion-test-sug-refund').click()
      const dialog = page.getByTestId('complexity-phrase-test-dialog')
      await expect(dialog).toBeVisible()
      await page.getByTestId('complexity-phrase-test-phrase-input-0').fill('refund status for my order')
      await page.getByTestId('complexity-phrase-test-run-button').click()
      await expect(page.getByTestId('complexity-phrase-test-taken-over')).toContainText('100%')
      await expect(page.getByTestId('complexity-phrase-test-example-log-1')).toContainText('where is my refund for order 12')

      await page.getByTestId('complexity-phrase-test-add-button').click()
      await expect(dialog).toBeHidden()
      await expect(page.getByTestId('complexity-suggestions-staged-input-sug-refund-0')).toHaveValue('refund status for my order')
    })

    test('should save a weekly schedule from the settings sheet', async ({ page }) => {
      await page.goto('/workspace/complexity-router?tab=suggestions')
      await page.getByTestId('complexity-suggestions-settings-button').click()
      await expect(page.getByTestId('complexity-suggestions-settings-sheet')).toBeVisible()

      await page.getByTestId('complexity-suggestions-every-select').click()
      await page.getByTestId('complexity-suggestions-every-option-1w').click()
      await expect(page.getByTestId('complexity-suggestions-weekday-select')).toBeVisible()
      await page.getByTestId('complexity-suggestions-timezone-input').fill('Asia/Kolkata')
      await page.getByTestId('complexity-suggestions-settings-save-button').click()

      await expect.poll(() => saved.length).toBe(1)
      expect(saved[0].suggestions).toMatchObject({
        enabled: true,
        schedule: { every: '1w', at: '03:00', weekday: 'monday', timezone: 'Asia/Kolkata' },
      })
    })
  })
})
