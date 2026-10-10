import { expect, test } from '../../core/fixtures/base.fixture'

test.describe('Complexity Router', () => {
  test.describe('OpenAI Decision Model', () => {
    // The saved config, a custom OpenAI-based provider, and the model listing
    // are mocked, so the test does not depend on OpenAI Decisions access or on
    // which models the catalog holds. The listing answers with the decision
    // models only when the picker asks for them, which is what proves the
    // picker filters on decisions.
    const CUSTOM_OPENAI = 'my-openai'
    let decisionProvider = 'openai'
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
                model: 'gpt-6-luna',
                previous_message_count: 2,
                timeout: '3000ms',
              },
            },
          })
        },
      )
      // The configured providers, plus a custom provider built on OpenAI.
      await page.route(
        (url) => url.pathname === '/api/providers',
        async (route) => {
          if (route.request().method() !== 'GET') return route.continue()
          const response = await route.fetch()
          const body = await response.json()
          body.providers = [
            ...(body.providers ?? []).filter((p: { name: string }) => p.name !== CUSTOM_OPENAI),
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
          const models =
            params.get('decisions') === 'true'
              ? [
                  { name: 'gpt-6-luna', provider: decisionProvider },
                  { name: 'gpt-6-luna-2026-09-01', provider: decisionProvider },
                ]
              : [{ name: 'gpt-4o', provider: decisionProvider }]
          await route.fulfill({ json: { models, total: models.length } })
        },
      )
    })

    for (const provider of ['openai', CUSTOM_OPENAI]) {
      test(`should list only OpenAI decision models for the ${provider} provider`, async ({ page }) => {
        decisionProvider = provider
        await page.goto('/workspace/complexity-router')
        await page.getByTestId('complexity-router-decision-settings-button').click()
        await expect(page.getByTestId('complexity-router-decision-sheet')).toBeVisible({ timeout: 5000 })

        const modelSelect = page.getByTestId('complexity-router-decision-model-select')
        await expect(modelSelect).toBeVisible({ timeout: 10000 })
        await expect(modelSelect).toContainText('gpt-6-luna')

        await modelSelect.click()
        await expect(page.getByRole('option', { name: 'gpt-6-luna-2026-09-01' })).toBeVisible()
        await expect(page.getByRole('option', { name: 'gpt-4o' })).toHaveCount(0)
        expect(modelQueries.length).toBeGreaterThan(0)
        expect(modelQueries.every((params) => params.get('decisions') === 'true')).toBe(true)
      })
    }
  })
})
