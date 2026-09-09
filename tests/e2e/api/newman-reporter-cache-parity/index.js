'use strict';

/**
 * Newman Cache Parity Reporter
 *
 * Sibling of newman-reporter-token-parity, and it exists for the same reason: newman's JSON
 * reporter cannot hand a test script's runtime state back to a post-run process.
 * `--reporter-json-export` serializes the PRE-RUN `collection.variable` / `environment.values`
 * definitions, never what a test script wrote via pm.collectionVariables.set(). The only live,
 * structured hook newman exposes to a reporter is the `console` event, which fires with the raw
 * (unstringified-for-terminal-display) argument array for every console.log() in a test script.
 *
 * Two generated folders emit cache measurements this way, and this reporter collects BOTH:
 *
 *   CACHE_ANCHOR_REPORT  - Cross-Cut Round 34, Mid-Conversation System Cache-Anchor Parity
 *                          (runners/lib/midconv-system-cache-parity.mjs). One row per
 *                          (cell, leg) where leg is direct Bedrock Converse / Bifrost
 *                          /anthropic/v1/messages / Bifrost /openai/v1/responses. The direct leg
 *                          is the baseline the Bifrost legs are compared against.
 *   CACHE_MATRIX_REPORT  - Cross-Cut Round 35, Cross-Provider Prompt-Cache Matrix
 *                          (runners/lib/crossprovider-cache-matrix.mjs). One row per
 *                          (cell, arm), carrying `series` for implicit-caching cells.
 *
 * Before this reporter existed both markers were emitted and nothing consumed them - they landed
 * in tmp/newman-cli.log as raw text, so a cache run produced pass/fail but no comparison table.
 *
 * The harness forks one newman PROCESS per provider by default (PARALLEL=1, the default of the
 * run-provider-harness-test Makefile target), so several reporter instances run concurrently in
 * separate processes. Each writes its OWN fragment file (--reporter-cache-parity-out) rather than
 * racing on a shared path; render-cache-parity-report.mjs merges every fragment afterwards. Note
 * that Round 35 in particular is meant to run with PARALLEL=0 (its rows match every provider
 * fork), but the fragment-per-process design holds either way.
 *
 * Options:
 *   --reporter-cache-parity-out <path>   Fragment output path (default: tmp/harness-cache-parity-<pid>.json)
 *   --reporter-cache-parity-silent       Suppress the per-cell capture log line
 *
 * Note: newman/commander's option-key derivation does not reliably strip a HYPHENATED reporter
 * name from its own multi-word flags (verified empirically for token-parity:
 * --reporter-token-parity-out arrives as options.tokenParityOut, not options.out, unlike
 * single-word reporter names such as dbverify's --reporter-dbverify-db-url -> options.dbUrl).
 * "cache-parity" is hyphenated too, so every option is read under every spelling we could
 * plausibly hit rather than relying on one.
 */

const fs = require('fs');
const path = require('path');

// kind is stamped onto each row so the renderer can split the two matrices into their own
// tables without re-deriving which marker a row came from.
const MARKERS = {
  CACHE_ANCHOR_REPORT: 'anchor',
  CACHE_MATRIX_REPORT: 'matrix',
};

function firstDefined(options, keys) {
  for (const k of keys) {
    if (options && options[k] !== undefined) return options[k];
  }
  return undefined;
}

module.exports = function (newman, options) {
  const silent = !!firstDefined(options, ['silent', 'cacheParitySilent', 'cache-parity-silent']);
  const outPath =
    firstDefined(options, ['out', 'cacheParityOut', 'cache-parity-out']) ||
    path.join('tmp', `harness-cache-parity-${process.pid}.json`);

  const reports = [];

  newman.on('console', function (err, args) {
    if (err || !args || !Array.isArray(args.messages) || args.messages.length < 2) return;
    const kind = MARKERS[args.messages[0]];
    if (!kind) return;
    let parsed;
    try {
      parsed = JSON.parse(args.messages[1]);
    } catch (e) {
      console.warn(`[cache-parity] failed to parse ${args.messages[0]} payload: ${e.message}`);
      return;
    }
    parsed.kind = kind;
    reports.push(parsed);
    if (!silent) {
      console.log(`[cache-parity] captured ${kind}/${parsed.cell}${parsed.leg ? '/' + parsed.leg : ''}`);
    }
  });

  newman.on('done', function () {
    if (reports.length === 0) return;
    fs.mkdirSync(path.dirname(outPath), { recursive: true });
    fs.writeFileSync(outPath, JSON.stringify(reports, null, 2));
    console.log(`[cache-parity] wrote ${reports.length} report(s) to ${outPath}`);
  });
};
