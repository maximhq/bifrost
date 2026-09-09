// Direct-vs-Bifrost prompt-cache parity.
//
// The cross-provider cache matrix (crossprovider-cache-matrix.mjs) measures Bifrost alone, which
// means a miss is unattributable: "the provider did not cache this round" and "Bifrost broke
// caching" produce the same number. That is why its implicit bar had to be weakened all the way
// down to "cached at least once across 4 rounds" - with no control arm, anything stricter would
// report provider luck as gateway health.
//
// This folder adds the control. Every cell runs the SAME caching flow twice:
//
//   1. the DIRECT leg, straight at the provider's native API, and
//   2. the BIFROST leg, through the matching native-shape route (/openai, /anthropic, /genai).
//
// Direct runs first, so the comparison reads as "here is what this provider does on its own, and
// here is what it does behind Bifrost". The bar is that Bifrost must cache at least as well as the
// direct call: hitRate(bifrost) >= hitRate(direct). A provider that simply did not cache this
// round drags BOTH legs down together and the assertion still holds, so provider noise cancels
// instead of being reported as a gateway defect.
//
// ---------------------------------------------------------------------------------------------
// Why every leg carries its own salt
//
// Prompt caching is keyed on an exact prefix match at the ACCOUNT/project level, with no isolation
// between rows that merely mean to be separate (see token-parity-matrix.mjs, which hit this same
// wall). Sending both legs the same prefix would make this measure HTTP ordering rather than
// caching: the direct leg's round 1 would warm the provider-side cache that the Bifrost leg then
// reads, and Bifrost would score a perfect hit rate on its very first call while having cached
// nothing itself. Since direct deliberately runs FIRST here, that failure mode would be
// systematic rather than occasional - it would manufacture a permanent green.
//
// So each leg prepends its own salt and starts cold. Each leg then writes on its own round 1 and
// reads its own write on round 2, and the two hit rates are genuinely comparable.

const CACHE_SEG = "{{cachePrefix}}";

// Some models need a bigger prefix than the docs claim before implicit caching engages at all.
// Measured directly against the Gemini API, same account, same body, no gateway involved:
//
//   gemini-2.5-flash  3,940 tok -> caches 3,051 (77%)
//   gemini-2.5-pro    3,939 tok -> 0 cached across six consecutive rounds
//   gemini-2.5-pro    7,848 tok -> caches 4,081 (52%)
//   gemini-2.5-pro   15,672 tok -> caches 12,266 (78%)
//
// So 2.5-pro's effective floor sits between 3.9K and 7.8K tokens, even though
// https://ai.google.dev/gemini-api/docs/caching lists 2048 for BOTH 2.5 Pro and 2.5 Flash. A cell
// under a model's real floor never caches on either leg, which reads as "permanently
// inconclusive" - a cell that can never fail and never means anything. Repeating the prefix is
// what turns it back into a real measurement.
const repeatSeg = (n) => Array.from({ length: n }, () => CACHE_SEG).join(" ");

// Per-run isolation (shared with the other cache folders) plus a per-leg literal. Both are needed:
// pcNonce keeps today's run from reading yesterday's cache, and the leg id keeps the two legs of
// the SAME cell from sharing one.
const saltFor = (cellId, leg) => `[cache-parity ${cellId} ${leg} {{pcNonce}}] `;

const QUESTION = "Reply with ONLY the word ACKNOWLEDGED and nothing else.";
const MAX_TOKENS = 256;

// Bifrost must be at least as good as direct. The epsilon absorbs float and tokenizer rounding
// only - a real regression moves the hit rate by far more than this, and the sign of the
// comparison is what carries the meaning.
const EPSILON = 0.02;

// Rounds per leg: one cold write, one warm read. Unlike the implicit matrix next door this does
// not need four rounds to catch a lucky hit, because the direct leg supplies the baseline that
// makes a single read meaningful.
const ROUNDS = 2;

// Gemini's implicit cache does not reliably engage by the first read - the cross-provider matrix
// next door needs four rounds to see it at all, and a two-round cell reports 0 for BOTH legs. That
// is a true statement about parity and a useless one about caching, so Gemini gets the extra
// rounds; every later round reads the same write.
// 6 rounds, not 4: at ~30% engagement per round a 4-round leg yields a usable comparison in only
// about two runs in three, so the extra rounds are what keep most runs from being inconclusive.
const ROUNDS_BY_SHAPE = { gemini: 6 };
const roundsFor = (cell) => ROUNDS_BY_SHAPE[cell.shape] || ROUNDS;

const J = (v) => JSON.stringify(v);

// ---------------------------------------------------------------------------------------------
// Shapes. Each knows how to build a body, address both legs, authenticate the direct leg, and
// read a hit rate out of the provider's own usage accounting.

const SHAPES = {
  // OpenAI chat/completions. Implicit caching, no cache_control to set.
  chat: {
    directUrl: () => "https://api.openai.com/v1/chat/completions",
    bifrostUrl: () => "{{baseUrl}}/openai/v1/chat/completions",
    directHeaders: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "Authorization", value: "Bearer {{openaiKey}}" },
    ],
    body: (cell, salt, model, seg) =>
      JSON.stringify({
        model: model,
        messages: [
          { role: "system", content: salt + seg },
          { role: "user", content: QUESTION },
        ],
        // max_completion_tokens, not max_tokens: the newer chat models (gpt-5.x) reject
        // max_tokens outright with a 400, and the older ones accept either.
        max_completion_tokens: MAX_TOKENS,
      }),
    // prompt_tokens already includes the cached reads, so the denominator is the whole prompt.
    hitRate: `
      var u = (j.usage || {});
      var cached = ((u.prompt_tokens_details || {}).cached_tokens) || 0;
      var total = u.prompt_tokens || 0;
      var hit = total > 0 ? cached / total : 0;`,
  },

  // Anthropic Messages. EXPLICIT caching - the cache_control breakpoint is the thing under test,
  // and it is exactly what Bifrost has to forward unmodified.
  anthropic: {
    directUrl: () => "https://api.anthropic.com/v1/messages",
    bifrostUrl: () => "{{baseUrl}}/anthropic/v1/messages",
    directHeaders: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "x-api-key", value: "{{anthropicKey}}" },
      { key: "anthropic-version", value: "2023-06-01" },
    ],
    body: (cell, salt, model, seg) =>
      JSON.stringify({
        model: model,
        system: [{ type: "text", text: salt + seg, cache_control: { type: "ephemeral" } }],
        messages: [{ role: "user", content: QUESTION }],
        max_tokens: MAX_TOKENS,
      }),
    // Anthropic reports the three buckets separately and input_tokens EXCLUDES both cache buckets,
    // so the denominator has to be rebuilt from all three.
    hitRate: `
      var u = (j.usage || {});
      var read = u.cache_read_input_tokens || 0;
      var write = u.cache_creation_input_tokens || 0;
      var uncached = u.input_tokens || 0;
      var total = read + write + uncached;
      var hit = total > 0 ? read / total : 0;`,
  },

  // Gemini generateContent. Implicit caching, and the cell class whose Bifrost-side numbers look
  // erratic today - the reason this folder exists.
  gemini: {
    directUrl: (cell) =>
      `https://generativelanguage.googleapis.com/v1beta/models/${cell.directModel}:generateContent`,
    bifrostUrl: (cell) => `{{baseUrl}}/genai/v1beta/models/${cell.directModel}:generateContent`,
    directHeaders: () => [
      { key: "Content-Type", value: "application/json" },
      { key: "x-goog-api-key", value: "{{genaiKey}}" },
    ],
    body: (cell, salt, model, seg) =>
      JSON.stringify({
        systemInstruction: { parts: [{ text: salt + seg }] },
        contents: [{ role: "user", parts: [{ text: QUESTION }] }],
        generationConfig: { maxOutputTokens: MAX_TOKENS },
      }),
    hitRate: `
      var u = (j.usageMetadata || {});
      var cached = u.cachedContentTokenCount || 0;
      var total = u.promptTokenCount || 0;
      var hit = total > 0 ? cached / total : 0;`,
  },
};

// ---------------------------------------------------------------------------------------------
// Cells. Deliberately narrow: two models per shape, chosen to cover both caching mechanisms and
// the provider whose numbers are currently in question. A cell costs two legs x its shape's round
// count in live calls against a ~5.9K-token prefix - four for a two-round shape, twelve for the
// six-round gemini shape - so this is a diagnostic instrument, not a sweep: widen it when a
// specific question needs it.
const CELLS = [
  { id: "openai-gpt-4o-mini", provider: "openai", shape: "chat", directModel: "gpt-4o-mini", mechanism: "implicit" },
  { id: "openai-gpt-5-6", provider: "openai", shape: "chat", directModel: "gpt-5.6", bifrostModel: "openai/gpt-5.6", mechanism: "implicit" },

  { id: "anthropic-sonnet-5", provider: "anthropic", shape: "anthropic", directModel: "claude-sonnet-5", mechanism: "explicit" },
  { id: "anthropic-haiku-4-5", provider: "anthropic", shape: "anthropic", directModel: "claude-haiku-4-5", mechanism: "explicit" },

  { id: "gemini-2-5-flash", provider: "gemini", shape: "gemini", directModel: "gemini-2.5-flash", mechanism: "implicit" },
  { id: "gemini-2-5-pro", provider: "gemini", shape: "gemini", directModel: "gemini-2.5-pro", mechanism: "implicit", prefixRepeat: 2 },
];

const seriesVarFor = (cellId, leg) => `dcp_${cellId.replace(/[^a-zA-Z0-9]+/g, "_")}_${leg}_series`;
const varFor = (cellId, leg) => `dcp_${cellId.replace(/[^a-zA-Z0-9]+/g, "_")}_${leg}_hit`;

// Round 1 writes the cache. Its hit rate is expected to be ~0 for BOTH legs (nothing to read yet),
// so it is recorded for the report but never asserted on - a provider that happens to serve a hit
// here is not a fault.
function roundOneScript(cell, leg, round) {
  const shape = SHAPES[cell.shape];
  const label = `${cell.provider}/${cell.directModel}`;
  return `
${
  round === 1
    ? `// Round 1 starts the cell fresh, BEFORE the error guard below. Collection variables outlive a
// single iteration, and both of this cell's variables carry a previous iteration's answer:
// the series, which a later "best" could be satisfied from, and the best-hit itself, which is
// the baseline the bifrost leg compares against. Resetting after the guard would leave both
// live whenever the write round errored - exactly the run where the stale baseline is most
// likely to be wrong.
pm.collectionVariables.unset(${J(varFor(cell.id, leg))});
pm.collectionVariables.set(${J(seriesVarFor(cell.id, leg))}, JSON.stringify([]));
`
    : ""
}if (pm.response.code >= 400) {
  // A write round that errors must fail its own row. Returning silently would leave the
  // read round as the only symptom, and a read round with nothing to read is reported
  // INCONCLUSIVE rather than failed - so a cell whose writes always error would stay
  // green indefinitely. The reset above still runs first, so the previous iteration's
  // baseline is cleared even on this path.
  pm.test(${J(`Direct-cache-parity [${label}] ${leg} round ${round} responded`)}, function () {
    pm.expect(pm.response.code, 'request failed: ' + pm.response.text()).to.be.below(400);
  });
  return;
}
var j = pm.response.json();
${shape.hitRate}
// Every round appends the same way: round 1 emptied the series above, so it needs no
// special case here.
var series = [];
try { series = JSON.parse(pm.collectionVariables.get(${J(seriesVarFor(cell.id, leg))}) || '[]'); } catch (e) { series = []; }
series.push(hit);
pm.collectionVariables.set(${J(seriesVarFor(cell.id, leg))}, JSON.stringify(series));
console.log('[direct-cache-parity] ${cell.id} ${leg} round ${round} hit=' + (hit * 100).toFixed(1) + '%');
`.trim();
}

// The read round reads the writes from the earlier rounds of the SAME leg. It is round 2 for a
// two-round shape and round 6 for gemini (see ROUNDS_BY_SHAPE), so the round number is passed in
// rather than assumed - a failure reported as "round 2" on a six-round cell names a call that
// passed.
function roundTwoScript(cell, leg, round) {
  const shape = SHAPES[cell.shape];
  const label = `${cell.provider}/${cell.directModel}`;

  const common = `
if (pm.response.code >= 400) {
  pm.test(${J(`Direct-cache-parity [${label}] ${leg} round ${round} responded`)}, function () {
    pm.expect(pm.response.code, 'request failed: ' + pm.response.text()).to.be.below(400);
  });
  return;
}
var j = pm.response.json();
${shape.hitRate}
var series = [];
try { series = JSON.parse(pm.collectionVariables.get(${J(seriesVarFor(cell.id, leg))}) || '[]'); } catch (e) { series = []; }
series.push(hit);
pm.collectionVariables.set(${J(seriesVarFor(cell.id, leg))}, JSON.stringify(series));

// Compare BEST-of-rounds, not the final round. Implicit caching is non-deterministic per round
// at the provider - byte-identical repeats alternate between 0% and a fixed plateau. Comparing
// one leg's last round against the other's therefore reports which leg got luckier on its final
// call: three consecutive runs of the same cell gave direct 0.772 / bifrost 0.000, then
// 0.772 / 0.771, then 0.000 / 0.771 - the first and third would have failed in OPPOSITE
// directions with no gateway change between them. Best-of-rounds asks the question that
// actually matters: did Bifrost ever reach what the provider ever reached.
var best = series.reduce(function (a, b) { return b > a ? b : a; }, 0);
pm.collectionVariables.set(${J(varFor(cell.id, leg))}, String(best));
`.trim();

  if (leg === "direct") {
    // The direct leg only records. It is the baseline, so it is never itself a failure - a
    // provider that declined to cache is information, not a defect.
    return `${common}
console.log('[direct-cache-parity] ${cell.id} direct round ${round} (read) hit=' + (hit * 100).toFixed(1) + '%');`;
  }

  return `${common}
var directRaw = pm.collectionVariables.get(${J(varFor(cell.id, "direct"))});
console.log('DIRECT_CACHE_PARITY_REPORT', JSON.stringify({
  cell: ${J(cell.id)}, provider: ${J(cell.provider)}, model: ${J(cell.directModel)},
  mechanism: ${J(cell.mechanism)},
  direct: directRaw === undefined || directRaw === '' ? null : Number(directRaw),
  bifrost: best, series: series, epsilon: ${EPSILON}
}));

// Skip rather than fail when the direct leg never recorded: without a baseline there is nothing to
// compare against, and asserting anyway would either invent a pass or blame Bifrost for a missing
// control. A direct leg that errored already failed its own "round ${round} responded" assertion.
if (directRaw === undefined || directRaw === '') { return; }
var direct = Number(directRaw);

// A direct leg that cached nothing gives no baseline to compare against: 0 >= 0 holds no matter
// what Bifrost did, so asserting on it would report a permanent pass for a cell that demonstrates
// no caching at all. Recorded as inconclusive instead - visible in the report, never a false green.
// Assert only when BOTH legs actually engaged the cache.
//
// Measured over 20 consecutive runs of gemini-2.5-flash, implicit caching engages about 30% of
// the time PER ROUND, independently on each leg: direct engaged in 15/20 runs, Bifrost in 14/20,
// and in all 10 runs where both engaged the hit rates were identical (0.772 vs 0.771 - the 0.001
// is the differing salt length). Comparing a run where direct happened to engage against one
// where Bifrost happened not to would have failed 5 of those 20 runs with no gateway difference
// whatsoever: a 25% false-failure rate measuring nothing but which leg won a coin flip.
//
// A run where only one leg engaged carries no comparison, so it is reported and skipped. That is
// deliberately NOT silent about a real regression: a cell whose Bifrost leg stops engaging
// entirely goes permanently inconclusive while direct keeps engaging, and the printed engagement
// line is what surfaces it. Inconclusive is a state to investigate, never a pass.
if (best <= 0 && direct > 0) {
  console.log('[direct-cache-parity] ${cell.id} INCONCLUSIVE: direct engaged (' + (direct * 100).toFixed(1) +
    '%) but Bifrost did not this run. Implicit caching is stochastic per round - investigate only if ' +
    'this cell stays inconclusive across runs while direct keeps engaging.');
  return;
}

if (direct <= 0) {
  console.log('[direct-cache-parity] ${cell.id} INCONCLUSIVE: direct leg cached nothing (' +
    (best * 100).toFixed(1) + '% on the bifrost leg), so there is no baseline to compare against.');
  return;
}

pm.test(${J(`Direct-cache-parity [${label}] Bifrost caches at least as well as the direct call`)}, function () {
  pm.expect(best, 'Bifrost best ' + (best * 100).toFixed(1) + '% vs direct best ' + (direct * 100).toFixed(1) +
    '% across all rounds. Both legs are separately salted, so each started cold and wrote its ' +
    'own cache - a shortfall here is Bifrost losing a cache the provider granted the direct call, not ' +
    'provider variance.').to.be.at.least(direct - ${EPSILON});
});`;
}

// A short settle so round 1's write has landed before round 2 reads it. Same reasoning, and same
// duration, as the cross-provider matrix next door.
const SETTLE = `var __t = Date.now(); while (Date.now() - __t < 1200) { /* let the round-1 write land */ }`;

function requestFor(cell, leg, body) {
  const shape = SHAPES[cell.shape];
  const url = leg === "direct" ? shape.directUrl(cell) : shape.bifrostUrl(cell);
  return {
    method: "POST",
    header:
      leg === "direct"
        ? shape.directHeaders()
        : [{ key: "Content-Type", value: "application/json" }],
    body: { mode: "raw", raw: body },
    // A plain string URL: newman resolves {{vars}} in it and it works for the absolute
    // provider endpoints too. A {raw}-only object does NOT resolve - the request errors
    // before it is ever sent.
    url: url,
  };
}

export function buildDirectCacheParityItems() {
  const items = [];

  for (const cell of CELLS) {
    const shape = SHAPES[cell.shape];

    // Direct first, deliberately: the baseline is established before Bifrost is measured against
    // it. Safe only because the legs are separately salted - see the header.
    for (const leg of ["direct", "bifrost"]) {
      // The direct leg must use the provider's own bare model id. The Bifrost leg uses
      // bifrostModel when the gateway cannot auto-resolve the bare name - an unregistered
      // model returns "could not auto resolve a provider for the request" rather than routing.
      const model = leg === "direct" ? cell.directModel : (cell.bifrostModel || cell.directModel);
      const body = shape.body(cell, saltFor(cell.id, leg), model, repeatSeg(cell.prefixRepeat || 1));

      const rounds = roundsFor(cell);
      for (let round = 1; round <= rounds; round++) {
        const isRead = round === rounds;
        items.push({
          name: `Direct-cache-parity: ${cell.provider}/${cell.directModel} ${leg} round ${round}${isRead ? " (read)" : " (write)"}`,
          event: [
            ...(isRead ? [{ listen: "prerequest", script: { type: "text/javascript", exec: [SETTLE] } }] : []),
            {
              listen: "test",
              script: {
                type: "text/javascript",
                exec: (isRead ? roundTwoScript(cell, leg, round) : roundOneScript(cell, leg, round)).split("\n"),
              },
            },
          ],
          request: requestFor(cell, leg, body),
        });
      }
    }
  }

  return items;
}

export function buildDirectCacheParityFolder() {
  return {
    name: "Cross-Cut Round 36: Direct-vs-Bifrost Prompt-Cache Parity (generated)",
    description:
      "Prompt-cache parity against the provider's own API. Each cell runs the same caching flow " +
      "twice - once straight at the provider (direct leg) and once through Bifrost's matching " +
      "native route (/openai, /anthropic, /genai) - and requires Bifrost to cache at least as " +
      "well as the direct call. Direct runs first so the baseline exists before Bifrost is " +
      "measured against it. Each leg is separately salted so both start cold; sharing a prefix " +
      "would let the direct leg warm Bifrost's cache and manufacture a permanent pass. This is " +
      "the control arm the cross-provider cache matrix lacks: there, a miss cannot be attributed " +
      "to the gateway or the provider, which is why its implicit bar is only 'cached at least " +
      "once'. Here a provider that declined to cache drags both legs down together and the " +
      "assertion still holds.",
    item: buildDirectCacheParityItems(),
  };
}
