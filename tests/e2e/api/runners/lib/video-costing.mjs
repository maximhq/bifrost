// Decision logic for run-video-costing.mjs.
//
// Split out of the runner for the same reason as stream-probe-verdict.mjs: the
// runner has top-level side effects (arg parsing, the submit/poll loop,
// process.exit) and cannot be imported from a test. Everything here is a pure
// function of already-fetched data, so the whole verdict surface is unit
// testable with no Bifrost, no network and no provider credentials.
//
// Run the tests directly: `node video-costing.test.mjs`.

// Resolution bands, mirroring videoResolutionBand + videoOutputPerSecondRate in
// framework/modelcatalog/datasheet/cost.go. Kept as an explicit list rather than
// a range test because the Go side matches EXACTLY: a 480p clip must not bill at
// the 720p rate just because it is below it, and an unmatched size deliberately
// falls back to the unbanded rate instead of picking the nearest band.
export const VIDEO_BANDS = [
  { edge: 480, key: "480p", column: "output_cost_per_video_per_second_480p" },
  { edge: 720, key: "720p", column: "output_cost_per_video_per_second_720p" },
  { edge: 1024, key: "1024p", column: "output_cost_per_video_per_second_1024p" },
  { edge: 1080, key: "1080p", column: "output_cost_per_video_per_second_1080p" },
  { edge: 2160, key: "4k", column: "output_cost_per_video_per_second_4k" },
];

// parseSize mirrors parseImageDimensions: "1920x1080" -> {width, height}. Returns
// null for anything that is not two positive integers around an `x`, which is the
// signal to fall back to the unbanded rate rather than to guess.
export function parseSize(size) {
  if (typeof size !== "string") return null;
  const m = /^\s*(\d+)\s*[xX]\s*(\d+)\s*$/.exec(size);
  if (!m) return null;
  const width = Number(m[1]);
  const height = Number(m[2]);
  if (!(width > 0) || !(height > 0)) return null;
  return { width, height };
}

// videoResolutionBand returns the short edge, which is what the band is keyed on:
// a portrait 1080x1920 clip is the same 1080p price as landscape 1920x1080.
export function videoResolutionBand(size) {
  const parsed = parseSize(size);
  if (!parsed) return 0;
  return Math.min(parsed.width, parsed.height);
}

// bandKeyFor names the band a size resolves to, or null when the size falls
// through to the unbanded rate. Used by the report so a wrong-rate failure says
// WHICH band was expected against which was billed.
export function bandKeyFor(size) {
  const edge = videoResolutionBand(size);
  const band = VIDEO_BANDS.find((b) => b.edge === edge);
  return band ? band.key : null;
}

// Money comparison tolerance. Costs are float64 through Go, JSON and back, and a
// rate x seconds product like 0.7 * 8 is not exact in binary floating point, so
// an equality test would report false failures on correct billing. A tenth of a
// cent is far below any real rate difference we are trying to catch (the bands we
// under-billed differed by 40-130%).
export const COST_EPSILON = 1e-4;

export function costsMatch(a, b, epsilon = COST_EPSILON) {
  if (typeof a !== "number" || typeof b !== "number") return false;
  if (!Number.isFinite(a) || !Number.isFinite(b)) return false;
  return Math.abs(a - b) <= epsilon;
}

// isSettlementRow is the single definition of "this is the cost row, not the
// submission row". video_debug.accounting is set only when a settler priced the
// job, so it distinguishes the two rows even though they share an object type,
// a model and a provider. Do not switch this to "cost is present": the submission
// row can legitimately carry a cost on older data, and an unpriceable settlement
// row legitimately carries none.
export function isSettlementRow(row) {
  return Boolean(row && row.video_debug && row.video_debug.accounting);
}

export function settlementRowsFor(rows, videoId) {
  if (!Array.isArray(rows)) return [];
  return rows.filter(
    (r) => isSettlementRow(r) && r.video_debug.video_id === videoId,
  );
}

// TERMINAL_STATUSES is what stops the poll loop. Anything else means the job is
// still running and the settlement row cannot exist yet.
export const TERMINAL_STATUSES = new Set(["completed", "failed", "cancelled"]);

export function isTerminal(status) {
  return TERMINAL_STATUSES.has(String(status || "").toLowerCase());
}

// expectedCostFor turns a case's `expect.cost` block plus what the settlement row
// actually reported into the dollar figure the row should carry.
//
// It reads seconds/size/output_count from the OBSERVED settlement row rather than
// from the request, because that is the dimension merge the change under test is
// responsible for: a provider that returns 5s for a requested 4s must bill 5. The
// case pins the dimensions separately (expect.seconds, expect.size) so a wrong
// merge fails as its own check rather than silently moving the cost target.
export function expectedCostFor(expect, observed) {
  const spec = (expect && expect.cost) || {};
  const mode = spec.mode || "rate";

  if (mode === "fixed") {
    if (typeof spec.value !== "number") {
      return { ok: false, reason: "expect.cost.mode=fixed needs a numeric expect.cost.value" };
    }
    return { ok: true, value: spec.value, explain: `fixed ${spec.value}` };
  }

  // The provider priced it and Bifrost must pass that through verbatim rather
  // than re-deriving it from a datasheet rate. Runware is the only provider that
  // returns a cost, and it is authoritative over any estimate we could compute.
  if (mode === "provider") {
    if (typeof observed.providerCost !== "number") {
      return { ok: false, reason: "provider did not report a cost to compare against" };
    }
    return {
      ok: true,
      value: observed.providerCost,
      explain: `provider-reported ${observed.providerCost}`,
    };
  }

  if (mode !== "rate") {
    return { ok: false, reason: `unknown expect.cost.mode "${mode}"` };
  }

  if (typeof spec.rate_per_second !== "number") {
    return { ok: false, reason: "expect.cost.mode=rate needs a numeric expect.cost.rate_per_second" };
  }
  const seconds = observed.seconds;
  if (typeof seconds !== "number" || !(seconds > 0)) {
    return { ok: false, reason: "settlement row reported no seconds to multiply the rate by" };
  }
  // max(1, count) mirrors the Go side: a completed job that reports no clips is a
  // failure to be handled as one, never a free request.
  const count = Math.max(1, Number(observed.outputCount) || 0);
  const value = spec.rate_per_second * seconds * count;
  return {
    ok: true,
    value,
    explain: `${spec.rate_per_second}/s x ${seconds}s x ${count}`,
  };
}

function check(name, pass, detail) {
  return { name, pass, detail };
}

// evaluateCase is the whole verdict for one case, as a list of independent checks
// so a report shows exactly which property broke rather than one opaque boolean.
//
//   spec       - the case from fixtures/video-costing-cases.json
//   submission - { requestId, videoId } from the POST
//   terminal   - the last poll response body (may be null if it never settled)
//   rows       - log rows fetched for this video id
export function evaluateCase({ spec, submission, terminal, rows }) {
  const checks = [];
  const expect = spec.expect || {};

  if (!submission || !submission.videoId) {
    checks.push(check("submitted", false, "no video id came back from the submit call"));
    return finalize(spec, checks);
  }
  checks.push(check("submitted", true, `video id ${submission.videoId}`));

  const status = terminal && terminal.status;
  if (!isTerminal(status)) {
    checks.push(
      check("terminal", false, `job never reached a terminal status (last: ${status || "unknown"})`),
    );
    return finalize(spec, checks);
  }
  checks.push(check("terminal", true, `status ${status}`));

  if (expect.status && String(expect.status) !== String(status)) {
    checks.push(check("expected-status", false, `wanted ${expect.status}, got ${status}`));
  } else if (expect.status) {
    checks.push(check("expected-status", true, status));
  }

  const settlements = settlementRowsFor(rows, submission.videoId);

  // Exactly one. Zero means the settler never ran or never found the job row;
  // more than one means the deterministic-id idempotency key failed, which would
  // double-bill and double-report to governance.
  if (settlements.length !== 1) {
    checks.push(
      check(
        "one-settlement-row",
        false,
        `${settlements.length} settlement rows for this video (want exactly 1)`,
      ),
    );
    return finalize(spec, checks);
  }
  checks.push(check("one-settlement-row", true, settlements[0].id));

  const row = settlements[0];
  const accounting = row.video_debug.accounting || {};
  const observed = {
    seconds: accounting.seconds,
    size: accounting.size,
    outputCount: accounting.output_count,
    incomplete: Boolean(accounting.incomplete),
    providerCost: readProviderCost(row, terminal),
  };

  if (expect.seconds != null) {
    checks.push(
      check(
        "seconds",
        observed.seconds === expect.seconds,
        `priced from ${observed.seconds}s, wanted ${expect.seconds}s`,
      ),
    );
  }
  if (expect.size != null) {
    checks.push(
      check("size", observed.size === expect.size, `priced from "${observed.size}", wanted "${expect.size}"`),
    );
  }
  if (expect.output_count != null) {
    checks.push(
      check(
        "output-count",
        observed.outputCount === expect.output_count,
        `priced ${observed.outputCount} clips, wanted ${expect.output_count}`,
      ),
    );
  }
  if (expect.band != null) {
    const actualBand = bandKeyFor(observed.size);
    checks.push(
      check("band", actualBand === expect.band, `size "${observed.size}" resolves to ${actualBand || "unbanded"}, wanted ${expect.band}`),
    );
  }
  if (expect.incomplete != null) {
    checks.push(
      check(
        "incomplete-flag",
        observed.incomplete === Boolean(expect.incomplete),
        `incomplete=${observed.incomplete}, wanted ${expect.incomplete}`,
      ),
    );
  }

  // Unpriceable is a real, expected outcome while the datasheet feed has not
  // published a band yet, so a case can assert it rather than being forced to
  // treat a null cost as a failure.
  if (expect.unpriceable) {
    checks.push(
      check("unpriceable", row.cost == null, `cost is ${row.cost == null ? "null" : row.cost}, wanted null`),
    );
    return finalize(spec, checks);
  }

  if (row.cost == null) {
    checks.push(check("priced", false, "settlement row carries no cost (unpriceable)"));
    return finalize(spec, checks);
  }
  checks.push(check("priced", true, `$${row.cost}`));

  const wanted = expectedCostFor(expect, observed);
  if (!wanted.ok) {
    checks.push(check("cost", false, wanted.reason));
    return finalize(spec, checks);
  }
  checks.push(
    check(
      "cost",
      costsMatch(row.cost, wanted.value),
      `billed $${row.cost}, wanted $${round6(wanted.value)} (${wanted.explain})`,
    ),
  );

  return finalize(spec, checks);
}

// readProviderCost digs out the provider's own cost figure, for
// expect.cost.mode=provider. Only Runware populates it.
//
// The terminal retrieve response is the primary source. /api/logs LIST rows carry
// only scalar columns, never the heavy video_*_output payload blobs, so reading it
// off the row alone found nothing and failed every provider-cost case with
// "provider did not report a cost" even when the provider had reported one.
function readProviderCost(row, terminal) {
  const sources = [terminal, row && row.video_retrieve_output, row && row.video_generation_output];
  for (const src of sources) {
    const cost = src && src.usage && src.usage.cost;
    if (cost == null) continue;
    // usage.cost is a BifrostCost object, not a number — the provider's figure
    // lands on total_cost. Reading it as a number found nothing and failed every
    // provider-cost case with "did not report a cost" while the cost sat right there.
    if (typeof cost === "object" && typeof cost.total_cost === "number") return cost.total_cost;
    if (typeof cost === "number") return cost;
  }
  return undefined;
}

function round6(n) {
  return Math.round(n * 1e6) / 1e6;
}

function finalize(spec, checks) {
  const failed = checks.filter((c) => !c.pass);
  return {
    id: spec.id,
    group: spec.group,
    checklist: spec.checklist,
    status: failed.length === 0 ? "pass" : "fail",
    checks,
    failures: failed.map((c) => `${c.name}: ${c.detail}`),
  };
}

// buildPricingOverride turns a case's expected rate into the pricing-override
// fragment that makes that rate real on the target instance. The bands are not in
// the upstream datasheet feed yet, so without seeding every rate-mode case would
// settle unpriceable and prove nothing.
export function buildPricingOverride(spec) {
  const cost = (spec.expect && spec.expect.cost) || {};
  if (cost.mode !== "rate" || typeof cost.rate_per_second !== "number") return null;
  const band = VIDEO_BANDS.find((b) => b.key === (spec.expect.band || bandKeyFor(spec.expect.size)));
  if (!band) return null;
  const model = spec.body && spec.body.model;
  if (!model) return null;
  // Pricing matches on the RESOLVED model, which carries no provider prefix: a
  // request for "gemini/veo-3.1-generate-preview" is priced as
  // "veo-3.1-generate-preview". Seeding the prefixed form matched nothing, so every
  // banded case silently billed at the unbanded datasheet rate — and the cases whose
  // official rate happens to equal that rate passed anyway, hiding it.
  const pattern = model.includes("/") ? model.slice(model.indexOf("/") + 1) : model;
  return {
    name: `video-costing-harness ${pattern} ${band.key}`,
    scope_kind: "global",
    match_type: "exact",
    pattern,
    // Required by the API, and it is what scopes the override to the request types
    // this rate can price. Retrieve is deliberately absent: polling is never billed.
    request_types: ["video_generation", "video_edit", "video_remix"],
    patch: { [band.column]: cost.rate_per_second },
  };
}

// mergePricingOverridesByModel collapses per-band fragments into ONE override per
// model, carrying every band that model's cases assert.
//
// This is load-bearing, not tidiness. Bifrost resolves a single winning override
// (resolveEntry in framework/modelcatalog/datasheet/overrides.go returns the first
// match; it does not merge), so seeding one override per band meant only the first
// applied and every other band silently fell back to the unbanded datasheet rate —
// which reads exactly like a band-selection bug in Bifrost when it is not one.
export function mergePricingOverridesByModel(specs) {
  const byPattern = new Map();
  for (const spec of specs) {
    const fragment = buildPricingOverride(spec);
    if (!fragment) continue;
    const existing = byPattern.get(fragment.pattern);
    if (!existing) {
      byPattern.set(fragment.pattern, { ...fragment, name: `video-costing-harness ${fragment.pattern}` });
      continue;
    }
    Object.assign(existing.patch, fragment.patch);
  }
  return [...byPattern.values()];
}
