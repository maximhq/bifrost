#!/usr/bin/env node
// Prints how many seconds a shard should wait before its next 429 retry, or "0" when the shard
// has no rate-limited executions and must not be retried at all.
//
// A CLI wrapper rather than logic, so the Makefile can stay shell: the policy and the header
// parsing live in lib/rate-limit-retry.mjs where unit tests can reach them.
//
// Usage:
//   node rate-limit-backoff.mjs --report tmp/newman-report-openai--tools.json --attempt 1
//   node rate-limit-backoff.mjs --report ... --query only-rate-limited   # prints 1 or 0
//
// --query only-rate-limited answers "were ALL of this shard's failures 429s?". It decides whether
// a clean retry may clear the shard's verdict: a shard that also failed an assertion must stay
// failed no matter how well the retry went, otherwise a real defect sitting next to a 429 would
// be erased by the backoff.
//
// Exit codes: 0 always (a missing or unparseable report prints 0 and is not an error - the
// caller's next step is "skip the retry", and a broken report is not worth failing the sweep).
import { readFileSync, existsSync } from "node:fs";
import {
  backoffSeconds,
  shouldRetry,
  isRetryable,
  DEFAULT_POLICY,
} from "./lib/rate-limit-retry.mjs";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) {
      const next = arr[i + 1];
      acc.push([cur.slice(2), next && !next.startsWith("--") ? next : "true"]);
    }
    return acc;
  }, [])
);

const REPORT = args.report;
const ATTEMPT = parseInt(args.attempt || "1", 10);
const QUERY = args.query && args.query !== "true" ? args.query : "backoff";

const say = (n) => {
  process.stdout.write(`${n}\n`);
  process.exit(0);
};

if (!REPORT || !existsSync(REPORT)) say(0);

let report;
try {
  report = JSON.parse(readFileSync(REPORT, "utf8"));
} catch {
  say(0);
}

const executions = report.run?.executions || [];

// Same failure predicate the merge and the analyzer use, so the three cannot disagree about what
// counts as a failed execution.
const failed = (e) =>
  (e.assertions || []).some((a) => !!a.error) ||
  (e.response?.code ?? 0) === 0 ||
  (e.response?.code ?? 0) >= 400 ||
  !e.response;

if (QUERY === "only-rate-limited") {
  const failures = executions.filter(failed);
  // Retryable, not rate-limited-only: this query decides whether a clean retry may CLEAR a shard's
  // verdict, and a shard whose only failures were 503/529 has as much claim to that as one whose
  // only failures were 429s. Both are the provider saying "not now"; neither is a defect the sweep
  // found. A shard that also failed an assertion still stays failed, which is the part that matters.
  say(failures.length > 0 && failures.every(isRetryable) ? 1 : 0);
}

if (!shouldRetry(report)) say(0);
say(backoffSeconds(executions, ATTEMPT, DEFAULT_POLICY));
