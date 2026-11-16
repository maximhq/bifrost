import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// A management write never runs on the model's say-so: Warp ends the turn with
// a confirmation card, and the write runs only when the person presses Confirm,
// which sends the card's token back on the card's thread. The model and the
// write are mocked here (the chat is a fixed SSE body), so this pins what the
// panel sends: only a Confirm click carries the token, Cancel and the keyboard
// never do, and an answered card cannot be pressed again. That the server runs
// a confirmed write once, and never twice, is pinned by the Go tests in
// framework/warp/management_test.go and the warp-management harness cases.

const configuredWarp = {
  configured: true,
  enabled: true,
  provider: 'openai',
  model: 'gpt-5.6-luna',
  max_iterations: 8,
  request_timeout_seconds: 120,
  history_retention_days: 30,
  embedding_provider: 'openai',
  embedding_model: 'text-embedding-3-small',
  embedding_dimension: 1536,
  log_vector_store_namespace: 'BifrostWarpLogs',
  semantic_search_threshold: 0.5,
  semantic_search_limit: 10,
  vector_store_connected: true,
}

const conversationId = '00000000-0000-4000-8000-0000000000c1'
const token = 'confirm:0123456789abcdef0123456789abcdef'
const cardQuestion = 'Run createVirtualKey with {"name":"team-a"}?'

function sse(frames: object[]): string {
  return frames.map((frame) => `event: ${(frame as { type: string }).type}\ndata: ${JSON.stringify(frame)}\n\n`).join('')
}

const cardStream = sse([
  { type: 'start' },
  {
    type: 'question',
    question: {
      question: cardQuestion,
      kind: 'confirm',
      options: [
        { label: 'Confirm', hint: token },
        { label: 'Cancel', hint: 'cancel' },
      ],
      allow_other: false,
    },
  },
  { type: 'done', finish_reason: 'question', conversation_id: conversationId },
])

const answerStream = sse([
  { type: 'start' },
  { type: 'delta', delta: 'Done.' },
  { type: 'done', finish_reason: 'stop', conversation_id: conversationId },
])

interface SentTurn {
  content: string
  conversationId?: string
}

// Answers the first message with the card and every later one with plain text,
// recording the last user message and thread id of each request.
async function mockWarp(page: Page): Promise<SentTurn[]> {
  const sent: SentTurn[] = []
  await page.route('**/api/warp/config', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(configuredWarp) }),
  )
  await page.route('**/api/warp/log-index/status', (route) =>
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ state: 'ready', vector_store_connected: true, embedding_configured: true }),
    }),
  )
  await page.route('**/api/warp/chat', (route) => {
    const body = route.request().postDataJSON() as { messages: { content: string }[]; conversation_id?: string }
    sent.push({ content: body.messages[body.messages.length - 1].content, conversationId: body.conversation_id })
    return route.fulfill({ status: 200, contentType: 'text/event-stream', body: sent.length === 1 ? cardStream : answerStream })
  })
  return sent
}

// The warp flag is shared server state other suites toggle; it only gates the
// UI, so it is turned on in this page's view of the flags alone.
async function enableWarpFlag(page: Page) {
  await page.route('**/api/feature-flags', async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const response = await route.fetch()
    const body = (await response.json()) as { flags: { id: string; enabled: boolean }[] }
    body.flags = body.flags.map((flag) => (flag.id === 'warp' ? { ...flag, enabled: true } : flag))
    await route.fulfill({ response, json: body })
  })
}

async function askForWrite(page: Page) {
  await page.getByTestId('topbar-warp-btn').click()
  const composer = page.getByTestId('warp-composer-input')
  await expect(composer).toBeVisible()
  await composer.fill('Create a virtual key named team-a.')
  await composer.press('Enter')
  await expect(page.getByTestId('warp-question')).toContainText(cardQuestion)
}

test.describe('Warp confirmation card', () => {
  let sent: SentTurn[]

  test.beforeEach(async ({ dashboardPage }) => {
    await enableWarpFlag(dashboardPage.page)
    sent = await mockWarp(dashboardPage.page)
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()
    await askForWrite(dashboardPage.page)
  })

  test('Confirm sends the token on the card thread, once', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await page.getByTestId('warp-question-option-0').click()

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1]).toEqual({ content: token, conversationId })
    await expect(page.getByText('Done.')).toBeVisible()
    // The answered card is gone, so its Confirm cannot be pressed a second time.
    await expect(page.getByTestId('warp-question')).toHaveCount(0)
    await expect(page.getByTestId('warp-question-option-0')).toHaveCount(0)
    expect(sent.filter((turn) => turn.content === token)).toHaveLength(1)
  })

  test('Cancel never sends the token', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await page.getByTestId('warp-question-option-1').click()

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1]).toEqual({ content: 'cancel', conversationId })
    await expect(page.getByTestId('warp-question')).toHaveCount(0)
    expect(sent.some((turn) => turn.content.startsWith('confirm:'))).toBe(false)
  })

  test('Enter and letter keys do not answer a confirmation card', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    // Focus off the composer, so the keys reach the card's document handler, which picks an option on any other card.
    await page.getByTestId('warp-question').getByText(cardQuestion).click()
    await page.keyboard.press('Enter')
    await page.keyboard.press('a')
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')

    await expect(page.getByTestId('warp-question')).toBeVisible()
    expect(sent).toHaveLength(1)
  })
})
