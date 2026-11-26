import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// A management write never runs on the model's say-so: Warp ends the turn with
// a confirmation card, and the write runs only when the person presses Confirm,
// which sends the card's token back on the card's thread. The model and the
// write are mocked here (the chat is a fixed SSE body), so this pins what the
// panel sends: only a press of Confirm carries the token, Cancel and stray keys
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
  bifrost_mcp_available: true,
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
      action: { tool: 'createVirtualKey', arguments: '{"name":"team-a"}' },
    },
  },
  { type: 'done', finish_reason: 'question', conversation_id: conversationId },
])

// An ordinary question, which letter keys and Enter can answer.
const pickStream = sse([
  { type: 'start' },
  {
    type: 'question',
    question: {
      question: 'Which team?',
      kind: 'scope',
      options: [
        { label: 'team-a', hint: 'team-a' },
        { label: 'team-b', hint: 'team-b' },
      ],
      allow_other: true,
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
async function mockWarp(page: Page, firstStream = cardStream, laterStreams: string[] = []): Promise<SentTurn[]> {
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
    return route.fulfill({ status: 200, contentType: 'text/event-stream', body: sent.length === 1 ? firstStream : (laterStreams[sent.length - 2] ?? answerStream) })
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
  await expect(page.getByTestId('warp-question')).toContainText('Create virtual key')
  await expect(page.getByTestId('warp-confirm-arguments')).toContainText('team-a')
  // The card is part of the input box, not a separate panel above it.
  await expect(page.getByTestId('warp-composer-tray').getByTestId('warp-question')).toBeVisible()
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
    await page.getByTestId('warp-question-option-confirm').click()

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1]).toEqual({ content: token, conversationId })
    await expect(page.getByText('Done.')).toBeVisible()
    // The answered card is gone, so its Confirm cannot be pressed a second time.
    await expect(page.getByTestId('warp-question')).toHaveCount(0)
    await expect(page.getByTestId('warp-question-option-confirm')).toHaveCount(0)
    expect(sent.filter((turn) => turn.content === token)).toHaveLength(1)
  })

  test('Cancel never sends the token', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await page.getByTestId('warp-question-option-cancel').click()

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1]).toEqual({ content: 'cancel', conversationId })
    await expect(page.getByTestId('warp-question')).toHaveCount(0)
    expect(sent.some((turn) => turn.content.startsWith('confirm:'))).toBe(false)
  })

  test('Enter and letter keys do not answer a confirmation card', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    // Focus off the composer, so the keys reach the card's document handler, which picks an option on any other card.
    await page.getByTestId('warp-question').getByText('Create virtual key').click()
    await page.keyboard.press('Enter')
    await page.keyboard.press('a')
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')

    await expect(page.getByTestId('warp-question')).toBeVisible()
    expect(sent).toHaveLength(1)
  })

  // Enter and letters typed elsewhere stay inert, but a focused Confirm is a deliberate press: a keyboard
  // user must be able to run the write, so native button activation is kept.
  test('Enter on a focused Confirm sends the token once', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    const confirmBtn = page.getByTestId('warp-question-option-confirm')
    await confirmBtn.focus()
    await expect(confirmBtn).toBeFocused()
    await page.keyboard.press('Enter')

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1]).toEqual({ content: token, conversationId })
    await page.waitForTimeout(400)
    expect(sent.filter((turn) => turn.content === token)).toHaveLength(1)
  })

  test('Esc cancels a confirmation card', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    await page.getByTestId('warp-question').getByText('Create virtual key').click()
    await page.keyboard.press('Escape')

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1]).toEqual({ content: 'cancel', conversationId })
  })
})

// An answered card stays on screen while the tray folds it back into the input, and that retained
// element still held its key handler and its pending Confirm. Both reached the model after the
// person had already moved on.
test.describe('Warp card while the tray closes', () => {
  test.beforeEach(async ({ dashboardPage }) => {
    await enableWarpFlag(dashboardPage.page)
  })

  test('a message sent while Confirm is drawing does not send the token', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    const sent = await mockWarp(page)
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()
    await askForWrite(page)

    // Confirm holds its answer 220 ms for the check animation, and the closed card stays mounted 180 ms
    // after the next message. A message 100 ms in lands inside that window, so the held Confirm would fire.
    await page.getByTestId('warp-question-option-confirm').click()
    await page.waitForTimeout(100)
    const composer = page.getByTestId('warp-composer-input')
    await composer.fill('cancel')
    await composer.press('Enter')

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1]).toEqual({ content: 'cancel', conversationId })
    await expect(page.getByText('Done.')).toBeVisible()
    // Well past the 220 ms the Confirm was held for.
    await page.waitForTimeout(600)
    expect(sent.some((turn) => turn.content === token)).toBe(false)
    expect(sent).toHaveLength(2)
  })

  test('Enter right after a keyboard answer does not send it again', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    const sent = await mockWarp(page, pickStream)
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()

    await page.getByTestId('topbar-warp-btn').click()
    const composer = page.getByTestId('warp-composer-input')
    await composer.fill('Make a key.')
    await composer.press('Enter')
    const card = page.getByTestId('warp-question')
    await expect(card).toContainText('Which team?')

    // Focus off the composer so the keys reach the card's document handler.
    await card.getByText('Which team?').click()
    await page.keyboard.press('b')
    await page.keyboard.press('Enter')

    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1].content).toBe('team-b')
    await expect(page.getByText('Done.')).toBeVisible()
    await page.waitForTimeout(400)
    expect(sent).toHaveLength(2)
  })

  test('a confirmation that arrives while the last card folds back can be answered', async ({ dashboardPage }) => {
    const page = dashboardPage.page
    const secondToken = 'confirm:fedcba9876543210fedcba9876543210'
    const sent = await mockWarp(page, cardStream, [cardStream.replace(token, secondToken)])
    await dashboardPage.goto()
    await expect(dashboardPage.pageTitle).toBeVisible()
    await askForWrite(page)

    // The mocked reply to Cancel is another card, so it lands inside the old card's 180 ms fold-back.
    await page.getByTestId('warp-question-option-cancel').click()
    await expect.poll(() => sent.length).toBe(2)
    expect(sent[1].content).toBe('cancel')

    const confirmBtn = page.getByTestId('warp-question-option-confirm')
    await expect(confirmBtn).toBeEnabled()
    await confirmBtn.click()
    await expect.poll(() => sent.length).toBe(3)
    expect(sent[2]).toEqual({ content: secondToken, conversationId })
  })
})
