// Pass manifest for harness-monitor.mjs.
//
// A full `make run-provider-harness-test` invokes newman more than once: the
// main pass (one fork per provider, or a single run under PARALLEL=0) and then
// the deferred sequential cache-parity pass. Each invocation used to get its own
// monitor process with its own zeroed counters, so the terminal showed two
// unrelated tables and the second one read as if the first had been discarded.
//
// Instead the Makefile now appends one JSON line per newman invocation to
// tmp/harness-monitor-passes.jsonl, and a single monitor process follows that
// file for the lifetime of the target. Counters accumulate across passes, so
// exactly one table is ever drawn.
//
// Line shape (one per newman invocation):
//   {"mode":"parallel","statusFile":"tmp/parallel-status","launched":7}
//   {"mode":"sequential","log":"tmp/newman-cli-cache-parity.log","collection":"tmp/harness-cache-filtered.json"}
//
// Splitting these helpers out of harness-monitor.mjs is what makes them
// testable: the monitor has top-level side effects (arg validation,
// process.exit, timers, alt-screen escape codes) and cannot be imported from a
// test. Same reason ci-interval.mjs exists.

// parseManifest returns one entry per COMPLETE line.
//
// The Makefile appends each line with a single printf, which is atomic for
// pipe-buffer-sized writes, but the monitor polls the file on a timer and a
// reader can still observe a write in progress on a network or fuse-backed
// tmp/. A trailing fragment with no newline is therefore left for the next poll
// rather than parsed half-formed - a truncated line would otherwise parse as
// invalid JSON and be dropped permanently, silently losing a whole pass.
//
// Unparseable complete lines are skipped rather than thrown on: a malformed
// manifest entry should cost that pass its row, not kill the monitor and take
// the whole table with it.
export function parseManifest(text) {
  if (typeof text !== "string" || text === "") return [];
  const lines = text.split("\n");
  // Anything after the final newline is an incomplete write.
  lines.pop();
  const passes = [];
  for (const line of lines) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    try {
      const parsed = JSON.parse(trimmed);
      if (parsed && typeof parsed === "object") passes.push(parsed);
    } catch {
      // Malformed entry - skip it, keep the monitor alive.
    }
  }
  return passes;
}

// walkLeaves visits every request-bearing node in a Postman collection,
// carrying the folder names above it. Shared by the denominator counters below
// and by any caller that needs the same traversal.
export function walkLeaves(items, ancestors, visit) {
  if (!Array.isArray(items)) return;
  for (const node of items) {
    if (Array.isArray(node.item)) {
      walkLeaves(node.item, [...ancestors, node.name ?? "(root)"], visit);
    } else if (node.request) {
      visit(node, ancestors);
    }
  }
}

// countLeaves is the denominator for a per-provider filtered collection, where
// the split has already been done on disk and every leaf belongs to that fork.
export function countLeaves(collection) {
  let total = 0;
  walkLeaves(collection?.item || [], [], () => {
    total += 1;
  });
  return total;
}

// countByProvider is the denominator for a collection covering several
// providers at once (the sequential main pass, and the cache-parity pass, which
// is one newman run spanning every backend). The split has to be recomputed by
// keyword here.
//
// Leaves that match no provider (auth matrix, management APIs) are left
// uncounted on purpose: nothing in the CLI log would attribute them either, so
// counting them would leave every run stuck short of its own total.
export function countByProvider(collection, providers, itemProvider) {
  const counts = Object.fromEntries(providers.map((p) => [p, 0]));
  walkLeaves(collection?.item || [], [], (node, ancestors) => {
    const p = itemProvider(node, ancestors);
    if (p && counts[p] !== undefined) counts[p] += 1;
  });
  return counts;
}

// sumPassTotals adds up a provider's per-pass denominator contributions.
//
// Contributions are keyed by pass index rather than accumulated into a running
// sum because loadDenominators re-reads on a timer until the filtered
// collections appear on disk (they are written by a filter step that races the
// monitor's own startup). Re-reading pass 0 must overwrite pass 0's
// contribution, not add a second copy of it.
export function sumPassTotals(totalsByPass) {
  let total = 0;
  for (const key of Object.keys(totalsByPass || {})) total += totalsByPass[key] || 0;
  return total;
}
