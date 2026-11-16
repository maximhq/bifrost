import type { FullResult, Reporter, TestCase, TestResult } from '@playwright/test/reporter'
import { mkdirSync, writeFileSync } from 'fs'
import { join } from 'path'

/**
 * Aggregates the axe results attached by features/accessibility/a11y-audit.spec.ts
 * into a coverage score and writes a11y-report/summary.{json,md}.
 *
 * Page score = axe rules passed / (rules passed + rules violated) on that page.
 * Coverage   = mean page score across every scanned page.
 *
 * Set A11Y_MIN_SCORE (0-100) to fail the run when coverage drops below it.
 */

type Violation = {
  id: string
  impact: string | null
  help: string
  helpUrl: string
  nodes: { target: string; html?: string; summary?: string }[]
}

type PageResult = {
  route: string
  finalPath: string
  passes: { id: string; nodes: number }[]
  violations: Violation[]
  incomplete: { id: string; nodes: number }[]
}

const IMPACTS = ['critical', 'serious', 'moderate', 'minor'] as const
const OUT_DIR = join(__dirname, 'a11y-report')

function pageScore(p: PageResult): number {
  const total = p.passes.length + p.violations.length
  return total === 0 ? 100 : (p.passes.length / total) * 100
}

export default class A11yReporter implements Reporter {
  private pages: PageResult[] = []
  private errored: string[] = []

  onTestEnd(test: TestCase, result: TestResult) {
    const attachment = result.attachments.find(a => a.name === 'a11y-results' && a.body)
    if (!attachment?.body) {
      if (result.status !== 'skipped') this.errored.push(test.title)
      return
    }
    this.pages.push(JSON.parse(attachment.body.toString()))
  }

  async onEnd(result: FullResult) {
    if (this.pages.length === 0) {
      console.log('\n[a11y] no results collected')
      return
    }

    // Several routes redirect to the same page (/workspace -> /workspace/home, section
    // indexes to their first tab). Count each rendered page once.
    const byPath = new Map<string, PageResult>()
    for (const p of [...this.pages].sort((a, b) => a.route.localeCompare(b.route))) {
      if (!byPath.has(p.finalPath)) byPath.set(p.finalPath, p)
    }
    const pages = [...byPath.values()].sort((a, b) => pageScore(a) - pageScore(b))

    const coverage = pages.reduce((sum, p) => sum + pageScore(p), 0) / pages.length
    const cleanPages = pages.filter(p => p.violations.length === 0).length

    const byImpact: Record<string, number> = { critical: 0, serious: 0, moderate: 0, minor: 0 }
    const byRule = new Map<string, { help: string; helpUrl: string; impact: string | null; pages: number; nodes: number }>()
    for (const p of pages) {
      for (const v of p.violations) {
        byImpact[v.impact ?? 'minor'] += v.nodes.length
        const entry = byRule.get(v.id) ?? { help: v.help, helpUrl: v.helpUrl, impact: v.impact, pages: 0, nodes: 0 }
        entry.pages += 1
        entry.nodes += v.nodes.length
        byRule.set(v.id, entry)
      }
    }
    const rules = [...byRule.entries()].sort((a, b) => b[1].pages - a[1].pages || b[1].nodes - a[1].nodes)

    const summary = {
      generatedAt: new Date().toISOString(),
      coverage: Number(coverage.toFixed(1)),
      pagesScanned: pages.length,
      pagesWithoutViolations: cleanPages,
      violatingElementsByImpact: byImpact,
      rules: Object.fromEntries(rules),
      pages: pages.map(p => ({
        path: p.finalPath,
        route: p.route,
        score: Number(pageScore(p).toFixed(1)),
        violations: p.violations,
        needsReview: p.incomplete,
      })),
      erroredRoutes: this.errored,
    }

    const md = [
      '# Accessibility coverage',
      '',
      `Coverage: **${summary.coverage}%** (WCAG 2.2 A/AA, axe-core)`,
      '',
      `Pages without violations: ${cleanPages} of ${pages.length}`,
      '',
      `Violating elements: ${IMPACTS.map(i => `${i} ${byImpact[i]}`).join(', ')}`,
      '',
      '## Rules failing',
      '',
      '| Rule | Impact | Pages | Elements |',
      '| --- | --- | --- | --- |',
      ...rules.map(([id, r]) => `| [${id}](${r.helpUrl}) | ${r.impact} | ${r.pages} | ${r.nodes} |`),
      '',
      '## Pages',
      '',
      '| Page | Score | Violations |',
      '| --- | --- | --- |',
      ...pages.map(p => `| ${p.finalPath} | ${pageScore(p).toFixed(1)}% | ${p.violations.map(v => v.id).join(', ') || '-'} |`),
      '',
    ].join('\n')

    // Playwright swallows reporter exceptions, so a write failure must fail the run
    // here or it would skip the A11Y_MIN_SCORE check below.
    try {
      mkdirSync(OUT_DIR, { recursive: true })
      writeFileSync(join(OUT_DIR, 'summary.json'), JSON.stringify(summary, null, 2))
      writeFileSync(join(OUT_DIR, 'summary.md'), md)
    } catch (err) {
      console.error(`[a11y] failed to write report to ${OUT_DIR}:`, err)
      return { status: 'failed' as const }
    }

    console.log(`\n[a11y] coverage ${summary.coverage}% across ${pages.length} pages (${cleanPages} with no violations)`)
    console.log(`[a11y] violating elements: ${IMPACTS.map(i => `${i} ${byImpact[i]}`).join(', ')}`)
    for (const [id, r] of rules.slice(0, 10)) {
      console.log(`[a11y]   ${id} (${r.impact}): ${r.pages} pages, ${r.nodes} elements`)
    }
    if (this.errored.length > 0) {
      console.log(`[a11y] ${this.errored.length} routes produced no results: ${this.errored.join(', ')}`)
    }
    console.log(`[a11y] report: ${join(OUT_DIR, 'summary.md')}`)

    const minScore = Number(process.env.A11Y_MIN_SCORE)
    if (process.env.A11Y_MIN_SCORE && coverage < minScore) {
      console.log(`[a11y] coverage ${summary.coverage}% is below A11Y_MIN_SCORE=${minScore}`)
      return { status: 'failed' as const }
    }
    return { status: result.status }
  }
}
