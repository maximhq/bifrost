import type {
  FullResult,
  Reporter,
  TestCase,
  TestError,
  TestResult,
} from "@playwright/test/reporter";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "fs";
import { dirname, join, relative } from "path";
import { E2E_ROOT, REPORTS_DIR, TIMINGS_FILE } from "../plan";

interface Entry {
  worker: string;
  file: string;
  line: number;
  title: string;
  status: TestResult["status"];
  retries: number;
  durationMs: number;
  error?: string;
  attachments: string[];
}

const ANSI = /\u001b\[[0-9;]*m/g;

/**
 * Writes reports/e2e-results.json: every failed and flaky test with its worker,
 * file:line and error, plus the targets `make run-e2e-ui RERUN=...` replays.
 * Full runs also refresh reports/e2e-timings.json, which the scheduler balances on.
 */
export default class FailuresReporter implements Reporter {
  private readonly outputFile: string;
  private readonly startedAt = Date.now();
  private final = new Map<TestCase, TestResult[]>();
  private errors: string[] = [];

  constructor(options: { outputFile?: string } = {}) {
    this.outputFile =
      options.outputFile ??
      process.env.E2E_RESULTS_FILE ??
      join(REPORTS_DIR, "e2e-results.json");
  }

  onTestEnd(test: TestCase, result: TestResult): void {
    const results = this.final.get(test) ?? [];
    results.push(result);
    this.final.set(test, results);
  }

  // Errors outside any test: a spec that fails to load, global setup, a worker crash.
  onError(error: TestError): void {
    const message = (error.message ?? error.value ?? "")
      .replace(ANSI, "")
      .split("\n")
      .slice(0, 12)
      .join("\n");
    const where = error.location
      ? `${relative(E2E_ROOT, error.location.file)}:${error.location.line}: `
      : "";
    this.errors.push(where + message);
  }

  onEnd(result: FullResult): void {
    const failed: Entry[] = [];
    const flaky: Entry[] = [];
    const totals = { passed: 0, failed: 0, flaky: 0, skipped: 0 };

    const fileMs = new Map<string, number>();
    const blockedFiles = new Set<string>();
    for (const [test, results] of this.final) {
      const file = relative(E2E_ROOT, test.location.file);
      fileMs.set(
        file,
        (fileMs.get(file) ?? 0) +
          results.reduce((sum, r) => sum + r.duration, 0),
      );
      const outcome = test.outcome();
      if (outcome === "skipped") {
        totals.skipped++;
        const annotations = [
          ...test.annotations,
          ...results.flatMap((r) => r.annotations),
        ];
        if (!annotations.some((a) => a.type === "skip" || a.type === "fixme"))
          blockedFiles.add(file);
      } else if (outcome === "expected") totals.passed++;
      else if (outcome === "flaky") totals.flaky++;
      else totals.failed++;
      if (outcome !== "unexpected" && outcome !== "flaky") continue;

      const last = results[results.length - 1];
      const firstFailure =
        results.find((r) => r.status !== "passed" && r.status !== "skipped") ??
        last;
      const entry: Entry = {
        worker: test.parent.project()?.name ?? "unknown",
        file: relative(E2E_ROOT, test.location.file),
        line: test.location.line,
        title: test.titlePath().filter(Boolean).slice(2).join(" › "),
        status: outcome === "flaky" ? "passed" : last.status,
        retries: results.length - 1,
        durationMs: results.reduce((sum, r) => sum + r.duration, 0),
        error: (firstFailure.error?.message ?? firstFailure.error?.value)
          ?.replace(ANSI, "")
          .split("\n")
          .slice(0, 12)
          .join("\n"),
        attachments: firstFailure.attachments
          .filter((a) => a.path)
          .map((a) => relative(E2E_ROOT, a.path!)),
      };
      (outcome === "flaky" ? flaky : failed).push(entry);
    }

    const byLocation = (a: Entry, b: Entry) =>
      a.file.localeCompare(b.file) || a.line - b.line;
    failed.sort(byLocation);
    flaky.sort(byLocation);

    const artifactPath = relative(join(E2E_ROOT, "../.."), this.outputFile);
    const report = {
      generatedAt: new Date().toISOString(),
      status: result.status,
      durationMs: Date.now() - this.startedAt,
      totals,
      rerun: {
        command: failed.length ? `make run-e2e-ui RERUN=${artifactPath}` : null,
        // A failure that blocked the rest of its serial group re-runs the whole file.
        targets: [
          ...new Set(
            failed.map((f) =>
              blockedFiles.has(f.file) ? f.file : `${f.file}:${f.line}`,
            ),
          ),
        ],
      },
      failed,
      flaky,
      errors: this.errors,
    };
    mkdirSync(dirname(this.outputFile), { recursive: true });
    writeFileSync(this.outputFile, JSON.stringify(report, null, 2) + "\n");
    console.log(
      `\nE2E results: ${totals.passed} passed, ${totals.failed} failed, ${totals.flaky} flaky, ${totals.skipped} skipped -> ${artifactPath}`,
    );
    if (report.rerun.command)
      console.log(`Re-run failures: ${report.rerun.command}`);
    if (this.errors.length)
      console.log(
        `${this.errors.length} error(s) outside tests; see "errors" in ${artifactPath}`,
      );
    if (process.env.E2E_RECORD_TIMINGS === "1") this.writeTimings(fileMs);
  }

  private writeTimings(fileMs: Map<string, number>): void {
    const timings: Record<string, number> = existsSync(TIMINGS_FILE)
      ? JSON.parse(readFileSync(TIMINGS_FILE, "utf8"))
      : {};
    for (const [file, ms] of fileMs)
      if (file.startsWith("features/")) timings[file] = Math.round(ms);
    const sorted = Object.fromEntries(
      Object.keys(timings)
        .sort()
        .map((k) => [k, timings[k]]),
    );
    writeFileSync(TIMINGS_FILE, JSON.stringify(sorted, null, 2) + "\n");
  }
}