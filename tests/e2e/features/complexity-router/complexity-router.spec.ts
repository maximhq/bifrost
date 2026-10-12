import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Complexity Router', () => {
  test.describe('Decision Model Picker', () => {
    // The saved config, the providers (OpenRouter and a custom OpenAI-based one),
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
              },
            },
          })
        },
      )
      // The configured providers, plus OpenRouter and a custom provider built on OpenAI.
      await page.route(
        (url) => url.pathname === '/api/providers',
        async (route) => {
          if (route.request().method() !== 'GET') return route.continue()
          const response = await route.fetch()
          const body = await response.json()
          body.providers = [
            ...(body.providers ?? []).filter((p: { name: string }) => p.name !== CUSTOM_OPENAI && p.name !== 'openrouter'),
            { name: 'openrouter', provider_status: 'active', keys: [] },
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
  })
})
