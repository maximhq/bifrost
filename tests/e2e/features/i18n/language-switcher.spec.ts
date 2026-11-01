import { readFileSync } from 'fs'
import { resolve } from 'path'
import type { Page } from '@playwright/test'
import { expect, test } from '../../core/fixtures/base.fixture'

const LOCALE_STORAGE_KEY = 'bifrost.locale'

// Expected strings come from the shipped catalogs, so a translation edit does not break the test.
function catalog(locale: string, namespace: string): Record<string, any> {
  return JSON.parse(readFileSync(resolve(__dirname, `../../../../ui/locales/${locale}/${namespace}.json`), 'utf8'))
}

function strings(locale: string) {
  return {
    language: catalog(locale, 'common').language as string,
    observability: catalog(locale, 'shell').nav.observability as string,
  }
}

async function selectLanguage(page: Page, locale: string) {
  await page.getByTestId('language-switcher').click()
  await page.getByTestId(`language-option-${locale}`).click()
}

async function expectLanguage(page: Page, locale: string) {
  const expected = strings(locale)
  await expect(page.locator('html')).toHaveAttribute('lang', locale)
  await expect(page.getByTestId('language-switcher')).toHaveAttribute('aria-label', expected.language)
  await expect(page.getByRole('button', { name: expected.observability, exact: true })).toBeVisible()
}

test.describe('Language switcher', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/workspace/logs')
    await expectLanguage(page, 'en')
  })

  test('switches between languages and persists the choice across a reload', async ({ page }) => {
    await selectLanguage(page, 'zh-CN')
    await expectLanguage(page, 'zh-CN')

    // Non-English to non-English must replace the previous translation, not fall back to English.
    await selectLanguage(page, 'ja')
    await expectLanguage(page, 'ja')
    expect(await page.evaluate((key) => localStorage.getItem(key), LOCALE_STORAGE_KEY)).toBe('ja')

    await page.reload()
    await expectLanguage(page, 'ja')

    await selectLanguage(page, 'en')
    await expectLanguage(page, 'en')
    expect(await page.evaluate((key) => localStorage.getItem(key), LOCALE_STORAGE_KEY)).toBe('en')
  })

  test('marks only the active language in the menu', async ({ page }) => {
    await selectLanguage(page, 'ja')
    await expectLanguage(page, 'ja')

    await page.getByTestId('language-switcher').click()
    await expect(page.getByTestId('language-option-ja').locator('svg')).toHaveCount(1)
    await expect(page.getByTestId('language-option-en').locator('svg')).toHaveCount(0)
    await page.keyboard.press('Escape')
  })

  test('renders a saved non-English language on first load', async ({ page }) => {
    await page.addInitScript((key) => localStorage.setItem(key, 'ja'), LOCALE_STORAGE_KEY)
    await page.goto('/workspace/logs')
    await expectLanguage(page, 'ja')

    await selectLanguage(page, 'zh-CN')
    await expectLanguage(page, 'zh-CN')
  })
})
