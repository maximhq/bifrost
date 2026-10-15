// Unit tests for lib/video-costing.mjs — the verdict logic behind
// run-video-costing.mjs.
//
// These cover the decisions that decide whether a video bills correctly, all of
// which are pure functions of already-fetched data:
//
//   - which resolution band a size resolves to      (videoResolutionBand/bandKeyFor)
//   - what the row SHOULD have cost                 (expectedCostFor)
//   - which log row is the cost row                 (isSettlementRow)
//   - whether a case passed, and which check broke  (evaluateCase)
//
// Run directly: `node video-costing.test.mjs`. No network, no Bifrost, no creds.
import assert from "node:assert";
import test from "node:test";

import {
  bandKeyFor,
  buildPricingOverride,
  costsMatch,
  evaluateCase,
  expectedCostFor,
  isSettlementRow,
  isTerminal,
  parseSize,
  mergePricingOverridesByModel,
  settlementRowsFor,
  videoResolutionBand,
} from "./video-costing.mjs";

test("parseSize accepts WxH and rejects everything else", () => {
  assert.deepEqual(parseSize("1920x1080"), { width: 1920, height: 1080 });
  assert.deepEqual(parseSize(" 1280 X 720 "), { width: 1280, height: 720 });
  assert.equal(parseSize("1080p"), null);
  assert.equal(parseSize("1920*1080"), null);
  assert.equal(parseSize("0x1080"), null);
  assert.equal(parseSize(""), null);
  assert.equal(parseSize(undefined), null);
});

test("the band is the short edge, so portrait and landscape price the same", () => {
  assert.equal(videoResolutionBand("1920x1080"), 1080);
  assert.equal(videoResolutionBand("1080x1920"), 1080);
  assert.equal(bandKeyFor("1080x1920"), "1080p");
});

test("bands match exactly — an off-band size falls through to unbanded", () => {
  assert.equal(bandKeyFor("854x480"), "480p");
  assert.equal(bandKeyFor("1280x720"), "720p");
  assert.equal(bandKeyFor("1024x1024"), "1024p");
  assert.equal(bandKeyFor("3840x2160"), "4k");
  // 640x360 is below every band. It must NOT borrow the 480p rate — that is the
  // silent-overbill the exact match exists to prevent.
  assert.equal(bandKeyFor("640x360"), null);
  // 1440p sits between published bands and has no rate of its own.
  assert.equal(bandKeyFor("2560x1440"), null);
  assert.equal(bandKeyFor("garbage"), null);
});

test("rate mode multiplies rate by the seconds and clip count actually billed", () => {
  const expect = { cost: { mode: "rate", rate_per_second: 0.7 } };
  const got = expectedCostFor(expect, { seconds: 8, outputCount: 1 });
  assert.equal(got.ok, true);
  assert.ok(costsMatch(got.value, 5.6), `wanted 5.6, got ${got.value}`);
});

test("rate mode bills every returned clip, and treats a zero count as one", () => {
  const expect = { cost: { mode: "rate", rate_per_second: 0.1 } };
  assert.ok(costsMatch(expectedCostFor(expect, { seconds: 4, outputCount: 3 }).value, 1.2));
  // A completed job reporting no clips is a failure to be handled as one, never
  // a free request — mirrors max(1, count) on the Go side.
  assert.ok(costsMatch(expectedCostFor(expect, { seconds: 4, outputCount: 0 }).value, 0.4));
});

test("rate mode refuses to invent a cost when the row reported no seconds", () => {
  const expect = { cost: { mode: "rate", rate_per_second: 0.1 } };
  const got = expectedCostFor(expect, { seconds: undefined, outputCount: 1 });
  assert.equal(got.ok, false);
  assert.match(got.reason, /no seconds/);
});

test("provider mode takes the provider's own figure, not a datasheet estimate", () => {
  const expect = { cost: { mode: "provider" } };
  const got = expectedCostFor(expect, { providerCost: 0.0432 });
  assert.equal(got.ok, true);
  assert.equal(got.value, 0.0432);

  const missing = expectedCostFor(expect, {});
  assert.equal(missing.ok, false);
});

test("fixed mode carries an exact figure, including zero for a failed job", () => {
  const got = expectedCostFor({ cost: { mode: "fixed", value: 0 } }, {});
  assert.equal(got.ok, true);
  assert.equal(got.value, 0);
});

test("the settlement row is the one carrying accounting, not merely a cost", () => {
  // The submission row shares object, model and provider with the cost row and
  // can carry a cost of its own, so cost is not a usable discriminator.
  const submission = { id: "a", cost: 2.4, video_debug: { video_id: "v1", status: "queued" } };
  const settlement = {
    id: "b",
    cost: 5.6,
    video_debug: { video_id: "v1", status: "completed", accounting: { seconds: 8 } },
  };
  assert.equal(isSettlementRow(submission), false);
  assert.equal(isSettlementRow(settlement), true);
  assert.deepEqual(settlementRowsFor([submission, settlement], "v1"), [settlement]);
  assert.deepEqual(settlementRowsFor([submission, settlement], "other"), []);
});

test("isTerminal stops the poll loop only on a final status", () => {
  assert.equal(isTerminal("completed"), true);
  assert.equal(isTerminal("failed"), true);
  assert.equal(isTerminal("cancelled"), true);
  assert.equal(isTerminal("in_progress"), false);
  assert.equal(isTerminal("queued"), false);
  assert.equal(isTerminal(undefined), false);
});

const SPEC = {
  id: "demo",
  group: "OpenAI",
  body: { model: "sora-2-pro" },
  expect: {
    seconds: 8,
    size: "1920x1080",
    band: "1080p",
    output_count: 1,
    cost: { mode: "rate", rate_per_second: 0.7 },
  },
};

function rowFor(overrides = {}) {
  return {
    id: "log-1",
    cost: 5.6,
    video_debug: {
      video_id: "v1",
      status: "completed",
      accounting: { seconds: 8, size: "1920x1080", output_count: 1 },
    },
    ...overrides,
  };
}

test("a correctly billed job passes every check", () => {
  const v = evaluateCase({
    spec: SPEC,
    submission: { videoId: "v1" },
    terminal: { status: "completed" },
    rows: [rowFor()],
  });
  assert.equal(v.status, "pass", JSON.stringify(v.failures));
});

test("the under-bill this whole change exists to catch is reported as a cost failure", () => {
  // sora-2-pro at 1080p billed at the unbanded $0.30 instead of the real $0.70.
  const v = evaluateCase({
    spec: SPEC,
    submission: { videoId: "v1" },
    terminal: { status: "completed" },
    rows: [rowFor({ cost: 2.4 })],
  });
  assert.equal(v.status, "fail");
  assert.equal(v.failures.length, 1);
  assert.match(v.failures[0], /^cost: billed \$2\.4, wanted \$5\.6/);
});

test("two settlement rows fail as a double-bill, not as a cost mismatch", () => {
  const v = evaluateCase({
    spec: SPEC,
    submission: { videoId: "v1" },
    terminal: { status: "completed" },
    rows: [rowFor(), rowFor({ id: "log-2" })],
  });
  assert.equal(v.status, "fail");
  assert.match(v.failures[0], /2 settlement rows/);
});

test("a job that never settles fails before any cost check runs", () => {
  const v = evaluateCase({
    spec: SPEC,
    submission: { videoId: "v1" },
    terminal: { status: "completed" },
    rows: [],
  });
  assert.equal(v.status, "fail");
  assert.match(v.failures[0], /0 settlement rows/);
});

test("a job stuck in progress fails as non-terminal, not as unbilled", () => {
  const v = evaluateCase({
    spec: SPEC,
    submission: { videoId: "v1" },
    terminal: { status: "in_progress" },
    rows: [],
  });
  assert.equal(v.status, "fail");
  assert.match(v.failures[0], /never reached a terminal status/);
});

test("a failed generation is expected to settle at exactly zero", () => {
  const spec = {
    id: "failed",
    body: { model: "sora-2" },
    expect: { status: "failed", cost: { mode: "fixed", value: 0 } },
  };
  const row = {
    id: "log-f",
    cost: 0,
    video_debug: { video_id: "v1", status: "failed", accounting: { output_count: 0 } },
  };
  const v = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "failed" },
    rows: [row],
  });
  assert.equal(v.status, "pass", JSON.stringify(v.failures));

  // Still billed despite failing is the regression that started this work.
  const stillBilled = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "failed" },
    rows: [{ ...row, cost: 2.4 }],
  });
  assert.equal(stillBilled.status, "fail");
  assert.match(stillBilled.failures[0], /billed \$2\.4, wanted \$0/);
});

test("unpriceable is assertable, so a missing feed rate is not a false failure", () => {
  const spec = { id: "u", body: { model: "m" }, expect: { unpriceable: true } };
  const row = {
    id: "log-u",
    cost: null,
    video_debug: { video_id: "v1", status: "completed", accounting: { incomplete: true } },
  };
  const v = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed" },
    rows: [row],
  });
  assert.equal(v.status, "pass", JSON.stringify(v.failures));
});

test("a wrong merged dimension fails on its own check, separately from cost", () => {
  // Provider returned 5s for a requested 4s and we priced the request instead.
  const spec = {
    id: "merge",
    body: { model: "m" },
    expect: { seconds: 5, cost: { mode: "rate", rate_per_second: 0.1 } },
  };
  const v = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed" },
    rows: [
      {
        id: "log-m",
        cost: 0.4,
        video_debug: { video_id: "v1", status: "completed", accounting: { seconds: 4, output_count: 1 } },
      },
    ],
  });
  assert.equal(v.status, "fail");
  assert.ok(v.failures.some((f) => /^seconds: priced from 4s, wanted 5s/.test(f)));
});

test("buildPricingOverride seeds the band column the case asserts", () => {
  const o = buildPricingOverride(SPEC);
  assert.equal(o.pattern, "sora-2-pro");
  assert.equal(o.match_type, "exact");
  assert.deepEqual(o.patch, { output_cost_per_video_per_second_1080p: 0.7 });
  // The API rejects an override with no request_types, and every seed silently
  // 400'd until this was sent — leaving the banded cases on the unbanded rate.
  assert.deepEqual(o.request_types, ["video_generation", "video_edit", "video_remix"]);
});

test("buildPricingOverride declines cases with no datasheet rate to seed", () => {
  assert.equal(buildPricingOverride({ expect: { cost: { mode: "provider" } }, body: { model: "m" } }), null);
  assert.equal(buildPricingOverride({ expect: { cost: { mode: "fixed", value: 0 } }, body: { model: "m" } }), null);
  // A size with no band has no column to write the rate into.
  assert.equal(
    buildPricingOverride({ expect: { size: "640x360", cost: { mode: "rate", rate_per_second: 1 } }, body: { model: "m" } }),
    null,
  );
});

test("the provider's cost is read off the terminal retrieve, not just the log row", () => {
  // /api/logs LIST rows carry only scalar columns — never the video_*_output
  // payload blobs — so a provider-cost case that looked only at the row failed
  // with "did not report a cost" even when the provider had reported one.
  const spec = { id: "p", body: { model: "m" }, expect: { cost: { mode: "provider" } } };
  const row = {
    id: "log-p",
    cost: 0.0432,
    video_debug: { video_id: "v1", status: "completed", accounting: { seconds: 5, output_count: 1 } },
  };
  const v = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed", usage: { cost: { total_cost: 0.0432 } } },
    rows: [row],
  });
  assert.equal(v.status, "pass", JSON.stringify(v.failures));

  // Bifrost billing something other than what the provider charged is the failure
  // this mode exists to catch.
  const drifted = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed", usage: { cost: { total_cost: 0.0432 } } },
    rows: [{ ...row, cost: 0.05 }],
  });
  assert.equal(drifted.status, "fail");
  assert.match(drifted.failures[0], /billed \$0\.05, wanted \$0\.0432/);
});

test("the payload blob on a single-log fetch still works as a fallback source", () => {
  const spec = { id: "p", body: { model: "m" }, expect: { cost: { mode: "provider" } } };
  const v = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed" },
    rows: [
      {
        id: "log-p",
        cost: 0.02,
        video_retrieve_output: { usage: { cost: { total_cost: 0.02 } } },
        video_debug: { video_id: "v1", status: "completed", accounting: { output_count: 1 } },
      },
    ],
  });
  assert.equal(v.status, "pass", JSON.stringify(v.failures));
});

test("every verdict carries a checks array, including the early-exit paths", () => {
  // run-video-costing.mjs's compare_cost_with cross-check pushes into
  // verdict.checks. A path that returned no array crashed the whole run at the
  // very end — after it had already spent money on real generations.
  const spec = { id: "e", body: { model: "m" }, expect: { cost: { mode: "rate", rate_per_second: 1 } } };
  for (const [label, input] of [
    ["no submission", { spec, submission: null, terminal: null, rows: [] }],
    ["never terminal", { spec, submission: { videoId: "v1" }, terminal: { status: "queued" }, rows: [] }],
    ["no settlement", { spec, submission: { videoId: "v1" }, terminal: { status: "completed" }, rows: [] }],
  ]) {
    const v = evaluateCase(input);
    assert.ok(Array.isArray(v.checks), `${label}: checks must be an array`);
    assert.ok(Array.isArray(v.failures), `${label}: failures must be an array`);
  }
});

test("usage.cost is a BifrostCost object, not a number", () => {
  // Reading it as a number found nothing and reported "provider did not report a
  // cost" on four cases whose cost was sitting right there in the response.
  const spec = { id: "shape", body: { model: "m" }, expect: { cost: { mode: "provider" } } };
  const row = {
    id: "log-s",
    cost: 0.42,
    video_debug: { video_id: "v1", status: "completed", accounting: { seconds: 5, output_count: 1 } },
  };
  const v = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed", usage: { cost: { total_cost: 0.42, output_cost: 0.42 } } },
    rows: [row],
  });
  assert.equal(v.status, "pass", JSON.stringify(v.failures));

  // A bare number still works, so a provider that reports one is not broken by the fix.
  const bare = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed", usage: { cost: 0.42 } },
    rows: [row],
  });
  assert.equal(bare.status, "pass", JSON.stringify(bare.failures));

  // An empty usage block must still report the shortfall rather than invent a figure.
  const none = evaluateCase({
    spec,
    submission: { videoId: "v1" },
    terminal: { status: "completed", usage: {} },
    rows: [row],
  });
  assert.equal(none.status, "fail");
  assert.match(none.failures[0], /did not report a cost/);
});

test("all of a model's bands merge into ONE override", () => {
  // Bifrost resolves a single winning override rather than merging them, so one
  // override per band meant only the first applied and every other band fell back
  // to the unbanded datasheet rate — indistinguishable from a band-selection bug.
  const specs = [
    { body: { model: "sora-2-pro" }, expect: { band: "720p", cost: { mode: "rate", rate_per_second: 0.3 } } },
    { body: { model: "sora-2-pro" }, expect: { band: "1024p", cost: { mode: "rate", rate_per_second: 0.5 } } },
    { body: { model: "sora-2" }, expect: { band: "720p", cost: { mode: "rate", rate_per_second: 0.1 } } },
  ];
  const merged = mergePricingOverridesByModel(specs);

  assert.equal(merged.length, 2, "one override per model, not per band");
  const pro = merged.find((o) => o.pattern === "sora-2-pro");
  assert.deepEqual(pro.patch, {
    output_cost_per_video_per_second_720p: 0.3,
    output_cost_per_video_per_second_1024p: 0.5,
  });
  assert.deepEqual(pro.request_types, ["video_generation", "video_edit", "video_remix"]);
  assert.deepEqual(merged.find((o) => o.pattern === "sora-2").patch, {
    output_cost_per_video_per_second_720p: 0.1,
  });
});

test("cases with no seedable rate contribute no override", () => {
  assert.deepEqual(
    mergePricingOverridesByModel([
      { body: { model: "kling" }, expect: { cost: { mode: "provider" } } },
      { body: { model: "sora-2" }, expect: { cost: { mode: "fixed", value: 0 } } },
    ]),
    [],
  );
});

test("the override pattern drops the provider prefix", () => {
  // Pricing matches the resolved model name. Seeding "gemini/veo-..." matched
  // nothing, so every band fell back to the unbanded datasheet rate — and the bands
  // whose official rate equals that fallback still passed, masking the whole thing.
  const o = buildPricingOverride({
    body: { model: "gemini/veo-3.1-fast-generate-preview" },
    expect: { band: "4k", cost: { mode: "rate", rate_per_second: 0.3 } },
  });
  assert.equal(o.pattern, "veo-3.1-fast-generate-preview");
  assert.deepEqual(o.patch, { output_cost_per_video_per_second_4k: 0.3 });

  // An unprefixed model is left exactly as-is.
  assert.equal(buildPricingOverride(SPEC).pattern, "sora-2-pro");

  // Merging keys off the stripped pattern, so both bands land on one override.
  const merged = mergePricingOverridesByModel([
    { body: { model: "gemini/veo-3.1-generate-preview" }, expect: { band: "720p", cost: { mode: "rate", rate_per_second: 0.4 } } },
    { body: { model: "gemini/veo-3.1-generate-preview" }, expect: { band: "4k", cost: { mode: "rate", rate_per_second: 0.6 } } },
  ]);
  assert.equal(merged.length, 1);
  assert.equal(merged[0].pattern, "veo-3.1-generate-preview");
  assert.deepEqual(merged[0].patch, {
    output_cost_per_video_per_second_720p: 0.4,
    output_cost_per_video_per_second_4k: 0.6,
  });
});
