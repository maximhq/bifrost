import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

// The notification panel's "Background jobs" section renders whatever
// GET /api/sidekiq/jobs returns. The endpoint is mocked so a job can be held in
// the running state, which a real one never stays in long enough to assert on.

interface MockJob {
  id: string
  kind: string
  status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled'
  progress?: { done: number; total: number }
  message?: string
  last_error?: string
  cancellable: boolean
  created_at: string
  updated_at: string
  completed_at?: string
}

const now = new Date().toISOString()

function runningJob(): MockJob {
  return {
    id: 'job-running',
    kind: 'logs_recalculate_cost',
    status: 'running',
    progress: { done: 25, total: 100 },
    cancellable: true,
    created_at: now,
    updated_at: now,
  }
}

async function mockJobs(page: Page, jobs: MockJob[]) {
  const cancelled: string[] = []
  await page.route('**/api/sidekiq/jobs', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ jobs }) }),
  )
  await page.route('**/api/sidekiq/jobs/*/cancel', (route) => {
    const id = route.request().url().split('/').at(-2) ?? ''
    cancelled.push(id)
    const job = jobs.find((j) => j.id === id)
    if (job) Object.assign(job, { status: 'cancelled', cancellable: false, completed_at: new Date().toISOString() })
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ cancelled: !!job }) })
  })
  return cancelled
}

test.describe('Notification panel background jobs', () => {
  test('shows a running job with its progress and a badge on the trigger', async ({ page }) => {
    await mockJobs(page, [runningJob()])
    await page.goto('/workspace/dashboard')

    await expect(page.getByTestId('topbar-notifications-badge')).toBeVisible()
    await page.getByTestId('topbar-notifications-btn').click()

    const section = page.getByTestId('sidekiq-jobs-section')
    await expect(section).toBeVisible()
    await expect(page.getByTestId('sidekiq-job-job-running')).toContainText('Recalculating log costs')
    await expect(page.getByTestId('sidekiq-job-status-job-running')).toHaveText('Running')
    await expect(page.getByTestId('sidekiq-job-progress-job-running')).toContainText('25 / 100')
  })

  test('shows completed, failed and cancelled jobs with their status and no cancel button', async ({ page }) => {
    const finished = (id: string, status: MockJob['status'], extra: Partial<MockJob> = {}): MockJob => ({
      id,
      kind: 'warp_log_embedding_backfill',
      status,
      cancellable: false,
      created_at: now,
      updated_at: now,
      completed_at: now,
      ...extra,
    })
    await mockJobs(page, [
      finished('job-done', 'completed', { progress: { done: 90, total: 100 } }),
      finished('job-failed', 'failed', { last_error: 'vector store unreachable' }),
      finished('job-cancelled', 'cancelled', { progress: { done: 30, total: 100 } }),
    ])
    await page.goto('/workspace/dashboard')
    await page.getByTestId('topbar-notifications-btn').click()

    await expect(page.getByTestId('sidekiq-job-status-job-done')).toHaveText('Completed')
    // Totals are approximate, so a completed job reads Done rather than 90 / 100.
    await expect(page.getByTestId('sidekiq-job-progress-job-done')).toContainText('Done')
    await expect(page.getByTestId('sidekiq-job-status-job-failed')).toHaveText('Failed')
    await expect(page.getByTestId('sidekiq-job-job-failed')).toContainText('vector store unreachable')
    await expect(page.getByTestId('sidekiq-job-status-job-cancelled')).toHaveText('Cancelled')
    await expect(page.getByTestId('sidekiq-job-progress-job-cancelled')).toContainText('30 / 100')
    await expect(page.locator('[data-testid^="sidekiq-job-cancel-"]')).toHaveCount(0)
  })

  test('cancelling takes a confirmation and then shows the job as cancelled', async ({ page }) => {
    const cancelled = await mockJobs(page, [runningJob()])
    await page.goto('/workspace/dashboard')
    await page.getByTestId('topbar-notifications-btn').click()

    await page.getByTestId('sidekiq-job-cancel-job-running').click()
    // First click only asks; nothing has been sent yet.
    expect(cancelled).toEqual([])

    const request = page.waitForRequest((req) => req.method() === 'POST' && req.url().endsWith('/api/sidekiq/jobs/job-running/cancel'))
    await page.getByTestId('sidekiq-job-cancel-confirm-job-running').click()
    await request

    await expect(page.getByTestId('sidekiq-job-status-job-running')).toHaveText('Cancelled')
    await expect(page.getByTestId('sidekiq-job-cancel-job-running')).toHaveCount(0)
  })

  test('tells the user when cancelling fails and leaves the job running', async ({ page }) => {
    await mockJobs(page, [runningJob()])
    // Registered after mockJobs, so this handler wins for the cancel route.
    await page.route('**/api/sidekiq/jobs/*/cancel', (route) =>
      route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: { message: 'database is down' } }) }),
    )
    await page.goto('/workspace/dashboard')
    await page.getByTestId('topbar-notifications-btn').click()

    await page.getByTestId('sidekiq-job-cancel-job-running').click()
    await page.getByTestId('sidekiq-job-cancel-confirm-job-running').click()

    await expect(page.locator('[data-sonner-toast][data-type="error"]').first()).toContainText("Couldn't cancel the job")
    await expect(page.getByTestId('sidekiq-job-status-job-running')).toHaveText('Running')
    // Cancel is available again, so the user can retry.
    await expect(page.getByTestId('sidekiq-job-cancel-job-running')).toBeVisible()
  })

  test('declining the confirmation keeps the job running', async ({ page }) => {
    const cancelled = await mockJobs(page, [runningJob()])
    await page.goto('/workspace/dashboard')
    await page.getByTestId('topbar-notifications-btn').click()

    await page.getByTestId('sidekiq-job-cancel-job-running').click()
    await page.getByRole('button', { name: 'Keep' }).click()

    await expect(page.getByTestId('sidekiq-job-cancel-job-running')).toBeVisible()
    await expect(page.getByTestId('sidekiq-job-status-job-running')).toHaveText('Running')
    expect(cancelled).toEqual([])
  })

  test('hides the section for a caller the endpoint forbids', async ({ page }) => {
    await page.route('**/api/sidekiq/jobs', (route) =>
      route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'forbidden' }) }),
    )
    await page.goto('/workspace/dashboard')
    await page.getByTestId('topbar-notifications-btn').click()

    await expect(page.getByTestId('notification-tray')).toBeVisible()
    await expect(page.getByTestId('sidekiq-jobs-section')).toHaveCount(0)
  })

  test('dismisses a finished job and clears all finished jobs, leaving running ones', async ({ page }) => {
    const finished = (id: string, status: MockJob['status']): MockJob => ({
      id,
      kind: 'warp_log_embedding_backfill',
      status,
      cancellable: false,
      created_at: now,
      updated_at: now,
      completed_at: now,
    })
    const cancelled = await mockJobs(page, [runningJob(), finished('job-done', 'completed'), finished('job-failed', 'failed'), finished('job-stopped', 'cancelled')])
    await page.goto('/workspace/dashboard')
    await page.getByTestId('topbar-notifications-btn').click()

    // A running job offers Cancel, never Dismiss.
    await expect(page.getByTestId('sidekiq-job-dismiss-job-running')).toHaveCount(0)

    await page.getByTestId('sidekiq-job-dismiss-job-done').click()
    await expect(page.getByTestId('sidekiq-job-job-done')).toHaveCount(0)
    await expect(page.getByTestId('sidekiq-job-job-failed')).toBeVisible()

    await page.getByTestId('sidekiq-jobs-clear-finished').click()
    await expect(page.getByTestId('sidekiq-job-job-failed')).toHaveCount(0)
    await expect(page.getByTestId('sidekiq-job-job-stopped')).toHaveCount(0)

    // The running job is untouched, and nothing was cancelled by clearing.
    await expect(page.getByTestId('sidekiq-job-status-job-running')).toHaveText('Running')
    await expect(page.getByTestId('sidekiq-jobs-clear-finished')).toHaveCount(0)
    expect(cancelled).toEqual([])
  })
})
