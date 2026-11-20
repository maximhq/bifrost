import { expect, test } from '../../core/fixtures/base.fixture'
import { assertToast } from '../../core/utils/test-helpers'

// The routers, their status and defaults, the logs rankings and the providers
// are mocked, so these tests depend neither on an embedding provider nor on
// what the gateway holds.
test.describe('Routers', () => {
  const KEYWORDS = { simple_keywords: ['hello'], medium_keywords: ['summarize this thread'], complex_keywords: ['design a system'] }
  const EVERY_TIER = ['SIMPLE', 'MEDIUM', 'COMPLEX'].map((tier) => ({ tier, provider: 'openai', model: 'gpt-4o-mini', weight: 1 }))
  const ROUTERS = [
    {
      id: 'general',
      name: 'General',
      classifier: 'semantic',
      keywords: KEYWORDS,
      semantic: { provider: 'openai', embedding_model: 'text-embedding-3-small', timeout: '2000ms', min_similarity: 0.4, vector_store: 'embedded', fallback: 'none' },
      routing: { enabled: false, scope: 'global', priority: 0 },
    },
    {
      id: 'support',
      name: 'Support',
      description: 'Help desk traffic',
      classifier: 'decision',
      keywords: KEYWORDS,
      decision: { provider: 'openai', model: 'gpt-4o-mini', previous_message_count: 1, timeout: '3000ms', min_confidence: 0.7 },
      targets: EVERY_TIER,
      routing: { enabled: true, scope: 'team', scope_id: 'team-support', cel_expression: 'model == "support-assistant"', priority: 2 },
    },
    {
      id: 'billing',
      name: 'Billing',
      classifier: 'llm',
      keywords: KEYWORDS,
      llm: { provider: 'openai', model: 'gpt-4o-mini', timeout: '4000ms', message_history_count: 1 },
      routing: { enabled: false, scope: 'global', priority: 1 },
    },
  ]
  type MockRouter = Record<string, unknown> & { id: string; routing: Record<string, unknown> }
  // What the gateway holds during one test: a create, a save or a delete
  // changes it, so the reads after it see the change as the gateway's would.
  let routers: MockRouter[] = []
  let createdBodies: Record<string, unknown>[] = []
  let targetBodies: Record<string, unknown>[] = []
  let routingBodies: Record<string, unknown>[] = []
  let updateBodies: Record<string, unknown>[] = []
  let deleted: string[] = []

  test.beforeEach(async ({ page }) => {
    routers = structuredClone(ROUTERS)
    createdBodies = []
    targetBodies = []
    routingBodies = []
    updateBodies = []
    deleted = []
    await page.route(
      (url) => url.pathname === '/api/routing/model-routers',
      async (route) => {
        if (route.request().method() === 'POST') {
          const body = route.request().postDataJSON() as Record<string, unknown>
          createdBodies.push(body)
          const created: MockRouter = { ...body, id: 'engineering', routing: { enabled: false, scope: 'global', priority: 3 } }
          routers.push(created)
          return route.fulfill({ json: created })
        }
        return route.fulfill({ json: { routers, count: routers.length } })
      },
    )
    await page.route(
      (url) => url.pathname === '/api/routing/model-routers/defaults',
      (route) =>
        route.fulfill({
          json: {
            config: { classifier: 'semantic', keywords: KEYWORDS },
            // As the gateway returns it: shipped guidance for every tier.
            decision_defaults: {
              criteria: {
                SIMPLE: { definition: 'Short, direct answers.', signals: ['a lookup'], examples: [] },
                MEDIUM: { definition: 'Routine work.', signals: ['a draft'], examples: [] },
                COMPLEX: { definition: 'Design and reasoning.', signals: ['trade-offs'], examples: [] },
              },
            },
            llm_default_prompt: 'Classify the request.',
          },
        }),
    )
    await page.route(
      (url) => /^\/api\/routing\/model-routers\/[^/]+\/status$/.test(url.pathname),
      (route) => route.fulfill({ json: { state: 'ready', loaded: 3, total: 3, cached_phrases: 3 } }),
    )
    await page.route(
      (url) => /^\/api\/routing\/model-routers\/[^/]+\/(targets|routing)$/.test(url.pathname),
      (route) => {
        const body = route.request().postDataJSON() as Record<string, unknown>
        const [, , , , id, endpoint] = new URL(route.request().url()).pathname.split('/')
        const router = routers.find((candidate) => candidate.id === id)
        if (!router) return route.fulfill({ status: 404, json: { error: { message: 'model router not found' } } })
        if (endpoint === 'targets') {
          targetBodies.push(body)
          router.targets = body.targets
          return route.fulfill({ json: router })
        }
        routingBodies.push(body)
        // The gateway changes only the fields a routing update sends.
        router.routing = { ...router.routing, ...body }
        return route.fulfill({ json: router })
      },
    )
    await page.route(
      (url) => /^\/api\/routing\/model-routers\/[^/]+$/.test(url.pathname) && !url.pathname.endsWith('/defaults'),
      async (route) => {
        const id = new URL(route.request().url()).pathname.split('/').pop() ?? ''
        const router = routers.find((candidate) => candidate.id === id)
        if (!router) return route.fulfill({ status: 404, json: { error: { message: 'model router not found' } } })
        if (route.request().method() === 'DELETE') {
          deleted.push(id)
          routers = routers.filter((candidate) => candidate.id !== id)
          return route.fulfill({ json: { message: 'Model router deleted successfully' } })
        }
        if (route.request().method() === 'PUT') {
          const body = route.request().postDataJSON() as Record<string, unknown>
          updateBodies.push(body)
          // The gateway keeps a name or description the body leaves out.
          Object.assign(router, body)
        }
        return route.fulfill({ json: router })
      },
    )
    // By router for the figures, and by tier, filtered to one router, for the split.
    await page.route(
      (url) => url.pathname === '/api/logs/rankings/by-dimension',
      (route) => {
        const url = new URL(route.request().url())
        if (url.searchParams.get('dimension') === 'complexity_tier') {
          const tiers = url.searchParams.get('model_router_ids') === 'support'
            ? [{ id: 'SIMPLE', total_requests: 30000 }, { id: 'MEDIUM', total_requests: 9000 }, { id: 'COMPLEX', total_requests: 2960 }]
            : []
          return route.fulfill({ json: { rankings: tiers.map((tier) => ({ ...tier, total_tokens: 0, total_cost: 0 })), dimension: 'complexity_tier' } })
        }
        return route.fulfill({
          json: {
            rankings: [
              {
                id: 'support',
                name: 'Support',
                total_requests: 41960,
                total_tokens: 0,
                total_cost: 12.5,
                trend: { has_previous_period: true, requests_trend: 4, tokens_trend: null, cost_trend: 18 },
              },
            ],
            dimension: 'model_router',
          },
        })
      },
    )
    await page.route(
      (url) => url.pathname === '/api/providers',
      (route) => route.fulfill({ json: { providers: [{ name: 'openai', provider_status: 'active', keys: [] }] } }),
    )
    await page.route(
      (url) => url.pathname === '/api/keys',
      (route) => route.fulfill({ json: [{ key_id: 'openai-key', name: 'OpenAI', provider: 'openai', enabled: true }] }),
    )
  })

  test.afterEach(async ({ page }) => {
    await page.unrouteAll({ behavior: 'ignoreErrors' })
  })

  test('should list routers with their classifier, scope, priority, traffic and switch', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    const support = page.getByTestId('model-router-row-support')
    await expect(support).toContainText('Decision model')
    await expect(support).toContainText('Team')
    await expect(support).toContainText('2')
    await expect(support).toContainText('41,960')
    await expect(page.getByTestId('model-router-enabled-support-switch')).toBeChecked()

    const billing = page.getByTestId('model-router-row-billing')
    await expect(billing).toContainText('LLM classifier')
    await expect(page.getByTestId('model-router-enabled-billing-switch')).not.toBeChecked()
  })

  test('should turn a router on from the list, sending only the switch', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    const billingSwitch = page.getByTestId('model-router-enabled-billing-switch')
    await billingSwitch.click()
    await expect.poll(() => routingBodies.length).toBe(1)
    expect(routingBodies[0]).toEqual({ enabled: true })
    // The list is read again after the save and keeps the router on.
    await expect(billingSwitch).toBeChecked()
    await page.reload()
    await expect(billingSwitch).toBeChecked()
  })

  test('should edit and delete a router from its row menu', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    await page.getByTestId('model-router-actions-general-btn').click()
    await page.getByTestId('model-router-edit-general-btn').click()
    await expect(page.getByTestId('model-router-sheet')).toBeVisible()
    await page.getByTestId('model-router-sheet-cancel-button').click()
    // The sheet blocks clicks until it has finished closing.
    await expect(page.getByTestId('model-router-sheet')).toHaveCount(0)

    await page.getByTestId('model-router-actions-general-btn').click()
    await page.getByTestId('model-router-delete-general-btn').click()
    await page.getByRole('alertdialog').getByRole('button', { name: 'Delete' }).click()
    await assertToast(page, 'Router deleted', 'success')
    expect(deleted).toEqual(['general'])
    await expect(page.getByTestId('model-router-row-general')).toHaveCount(0)
    await expect(page.getByTestId('model-router-row-support')).toBeVisible()
  })

  test('should open a router on its overview, with what it still needs and its traffic', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    await page.getByTestId('model-router-row-billing').click()
    await expect(page).toHaveURL(/router=billing/)
    await expect(page.getByTestId('model-router-tab-overview')).toHaveAttribute('data-state', 'active')

    await expect(page.getByTestId('model-router-setup-classifier')).toHaveAttribute('data-done', 'true')
    await expect(page.getByTestId('model-router-setup-models')).toHaveAttribute('data-done', 'false')
    await expect(page.getByTestId('model-router-fact-models')).toContainText('0 models · 0 of 3 tiers')
    await expect(page.getByTestId('model-router-fact-routing')).toContainText('Priority 1')
    await expect(page.getByTestId('model-router-metric-requests')).toContainText('No requests yet')
    await expect(page.getByTestId('model-router-status')).toContainText('This router is off')
    await expect(page.getByTestId('model-router-view-requests-button')).toHaveAttribute('href', /model_router_ids=.*billing/)

    await page.getByTestId('model-router-setup-models-action').click()
    await expect(page.getByTestId('model-router-tab-models')).toHaveAttribute('data-state', 'active')
  })

  test('should show a busy router its requests split by tier and their cost against the week before', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=support')
    await expect(page.getByTestId('model-router-setup')).toHaveCount(0)
    await expect(page.getByTestId('model-router-metric-requests')).toContainText('Last 7 days')
    await expect(page.getByTestId('model-router-metric-tiers')).toHaveText('Simple 30K · Medium 9K · Complex 2.96K')
    await expect(page.getByTestId('model-router-metric-cost-trend')).toContainText('18.0% vs previous period')
    await expect(page.getByTestId('model-router-status')).toContainText('Taking requests')
    // The figures are not links; the status line is the way into the logs.
    await expect(page.getByTestId('model-router-metric-cost')).not.toHaveAttribute('role', 'button')
    await page.getByTestId('model-router-view-requests-button').click()
    await expect(page).toHaveURL(/\/workspace\/logs.*model_router_ids/)
  })

  test('should create a router in the wizard and open it', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    await page.getByTestId('model-router-create-button').click()
    await expect(page.getByTestId('model-router-wizard')).toBeVisible()

    // A name is required before the next step.
    await page.getByTestId('model-router-wizard-next').click()
    await expect(page.getByTestId('model-router-wizard-step-classifier')).toBeDisabled()
    await page.getByTestId('model-router-name-input').fill('Engineering assistant')
    await page.getByTestId('model-router-description-input').fill('Coding agents and IDE traffic.')
    await page.getByTestId('model-router-wizard-next').click()

    await expect(page.getByTestId('complexity-router-classifier-option-llm')).toBeVisible()
    await page.getByTestId('complexity-router-classifier-option-semantic').click()
    await page.getByTestId('model-router-wizard-next').click()
    // Semantic needs an embedding model, so its sheet opens first; the router
    // can still be created with its phrases and configured later.
    await expect(page.getByTestId('complexity-router-embedding-sheet')).toBeVisible()
    await page.getByTestId('complexity-router-embedding-sheet-close-button').click()
    await expect(page.getByTestId('model-router-starter-phrases-notice')).toBeVisible()

    await page.getByTestId('model-router-wizard-create').click()
    await expect.poll(() => createdBodies.length).toBe(1)
    expect(createdBodies[0]).toMatchObject({
      name: 'Engineering assistant',
      description: 'Coding agents and IDE traffic.',
      classifier: 'semantic',
      keywords: KEYWORDS,
    })
    expect(createdBodies[0]).not.toHaveProperty('suggestions')
    await expect(page).toHaveURL(/router=engineering/)
    // The new router opens on its overview, read back as the gateway saved it.
    await expect(page.getByTestId('model-router-detail')).toBeVisible()
    await expect(page.getByTestId('model-router-tab-overview')).toHaveAttribute('data-state', 'active')
    await expect(page.getByTestId('model-router-status')).toContainText('This router is off')
  })

  test('should keep the classifier folded on the Configuration tab until asked', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=general&tab=configuration')
    await expect(page.getByTestId('model-router-classifier-summary')).toContainText('text-embedding-3-small')
    await expect(page.getByTestId('complexity-router-classifier-option-semantic')).toHaveCount(0)
    await page.getByTestId('model-router-classifier-change-button').click()
    await expect(page.getByTestId('complexity-router-classifier-option-llm')).toBeVisible()
  })

  test('should save the model each tier gets on the Models tab', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=billing&tab=models')
    await expect(page.getByTestId('model-router-tier-simple')).toContainText("Keeps the request's model")
    await expect(page.getByTestId('model-router-models-save-button')).toBeDisabled()

    await page.getByTestId('model-router-tier-simple-add-button').click()
    await page.getByTestId('model-router-models-save-button').click()
    // A target needs a provider or a model.
    await expect(page.getByTestId('model-router-tier-simple-error')).toBeVisible()
    expect(targetBodies).toHaveLength(0)

    // With a provider it saves, and the tab reads the saved target back.
    await page.getByTestId('model-routing-target-simple-0-provider-select').click()
    await page.getByRole('option', { name: 'OpenAI' }).click()
    await page.getByTestId('model-router-models-save-button').click()
    await expect.poll(() => targetBodies.length).toBe(1)
    expect(targetBodies[0].targets).toEqual([expect.objectContaining({ tier: 'SIMPLE', provider: 'openai' })])
    await expect(page.getByTestId('model-router-tier-simple-error')).toHaveCount(0)
    await expect(page.getByTestId('model-router-models-save-button')).toBeDisabled()
    await page.reload()
    await expect(page.getByTestId('model-router-tier-simple')).not.toContainText("Keeps the request's model")
  })

  test('should save a router routing from its Routing tab', async ({ page }) => {
    await page.goto('/workspace/model-routers?router=billing&tab=routing')
    await expect(page.getByTestId('model-router-routing-row')).toContainText('Every request in scope')
    await page.getByTestId('model-router-routing-row').click()
    await expect(page.getByTestId('model-router-sheet')).toBeVisible()
    await page.getByTestId('model-router-routing-priority-input').fill('5')
    await page.getByTestId('model-router-sheet-save-button').click()
    await expect.poll(() => routingBodies.length).toBe(1)
    // Only the field that changed: the gateway keeps the scope, condition and switch.
    expect(routingBodies[0]).toEqual({ priority: 5 })
  })

  test('should send only the name when a router is renamed from its sheet', async ({ page }) => {
    await page.goto('/workspace/model-routers')
    await page.getByTestId('model-router-actions-support-btn').click()
    await page.getByTestId('model-router-edit-support-btn').click()
    await expect(page.getByTestId('model-router-sheet')).toBeVisible()
    await page.getByTestId('model-router-name-input').fill('Support desk')
    await page.getByTestId('model-router-sheet-save-button').click()
    await assertToast(page, 'Router saved', 'success')
    expect(updateBodies).toEqual([{ name: 'Support desk' }])
    expect(routingBodies).toHaveLength(0)
  })

  test('should point the Complexity Router page at Routers', async ({ page }) => {
    // The Complexity Router page reads its own saved config and classifier
    // status, which are mocked like the routers so the notice does not depend
    // on what the gateway holds.
    await page.route(
      (url) => url.pathname === '/api/routing/complexity-analyzer-config',
      async (route) => {
        if (route.request().method() !== 'GET') return route.continue()
        await route.fulfill({ json: { classifier: 'semantic', keywords: KEYWORDS } })
      },
    )
    await page.route(
      (url) => url.pathname === '/api/routing/complexity-analyzer-status',
      async (route) => {
        if (route.request().method() !== 'GET') return route.continue()
        await route.fulfill({ json: { state: 'ready', loaded: 3, total: 3, cached_phrases: 3 } })
      },
    )
    await page.goto('/workspace/complexity-router')
    await expect(page.getByTestId('complexity-router-routers-notice')).toBeVisible()
    await page.getByTestId('complexity-router-routers-notice-link').click()
    await expect(page).toHaveURL(/\/workspace\/model-routers/)
  })
})
