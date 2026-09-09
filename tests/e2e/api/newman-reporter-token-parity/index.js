'use strict';

/**
 * Newman Token Parity Reporter
 *
 * The Direct-Provider vs Bifrost Token Parity Matrix (tests/e2e/api/runners/lib/
 * token-parity-matrix.mjs) can't hand its per-cell direct-vs-bifrost usage numbers back to a
 * post-run script through newman's JSON reporter - `--reporter-json-export` does not persist
 * pm.collectionVariables mutations anywhere in its output (verified empirically: the exported
 * report's `collection.variable`/`environment.values` reflect the pre-run static definitions
 * only, never what test scripts wrote at runtime). The one live, structured hook newman does
 * expose to reporters is the `console` event, which fires with the raw (unstringified-for-
 * terminal-display) arguments array for every console.log() call in a test script. Each parity
 * cell's final round emits `console.log("TOKEN_PARITY_REPORT", JSON.stringify({...}))`; this
 * reporter collects those and writes them to a JSON fragment file.
 *
 * The harness runs one newman process PER PROVIDER by default (PARALLEL=1, the default - see
 * the run-provider-harness-test Makefile target), so multiple reporter instances run
 * concurrently across separate OS processes. Each instance writes its OWN fragment file
 * (--reporter-token-parity-out, one per provider fork) rather than racing on a shared file;
 * render-token-parity-report.mjs merges every tmp/harness-token-parity-*.json fragment into the
 * final tmp/harness-token-parity.md table after all forks complete (same merge-after-fork shape
 * already used for tmp/newman-report-*.json).
 *
 * Options:
 *   --reporter-token-parity-out <path>   Fragment output path (default: tmp/harness-token-parity-<pid>.json)
 *   --reporter-token-parity-silent       Suppress the per-cell capture log line
 *
 * Note: newman/commander's option-key derivation does not reliably strip a HYPHENATED reporter
 * name from its own multi-word flags (verified empirically: --reporter-token-parity-out arrives
 * as options.tokenParityOut, not options.out, unlike single-word reporter names such as
 * dbverify's --reporter-dbverify-db-url -> options.dbUrl). Every option is read under every
 * spelling we've observed or could plausibly hit, rather than relying on one.
 */

const fs = require('fs');
const path = require('path');

const MARKER = 'TOKEN_PARITY_REPORT';

function firstDefined(options, keys) {
  for (const k of keys) {
    if (options && options[k] !== undefined) return options[k];
  }
  return undefined;
}

module.exports = function (newman, options) {
  const silent = !!firstDefined(options, ['silent', 'tokenParitySilent', 'token-parity-silent']);
  const outPath =
    firstDefined(options, ['out', 'tokenParityOut', 'token-parity-out']) ||
    path.join('tmp', `harness-token-parity-${process.pid}.json`);

  const reports = [];

  newman.on('console', function (err, args) {
    if (err || !args || !Array.isArray(args.messages) || args.messages.length < 2) return;
    if (args.messages[0] !== MARKER) return;
    let parsed;
    try {
      parsed = JSON.parse(args.messages[1]);
    } catch (e) {
      console.warn(`[token-parity] failed to parse report payload: ${e.message}`);
      return;
    }
    reports.push(parsed);
    if (!silent) {
      console.log(`[token-parity] captured ${parsed.backend}/${parsed.modality}`);
    }
  });

  newman.on('done', function () {
    if (reports.length === 0) return;
    fs.mkdirSync(path.dirname(outPath), { recursive: true });
    fs.writeFileSync(outPath, JSON.stringify(reports, null, 2));
    console.log(`[token-parity] wrote ${reports.length} report(s) to ${outPath}`);
  });
};
