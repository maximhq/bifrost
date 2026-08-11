// Generates the "Cross-Provider Prompt-Cache Matrix" folder consumed by
// augment-provider-harness.mjs.
//
// (createHash is used only to bound prompt_cache_key to OpenAI's 64-char limit; see cacheKeyFor.)
//
// WHAT THIS TESTS THAT THE OTHER TWO CACHE FOLDERS DON'T
// -------------------------------------------------------------------------------------------
// midconv-system-cache-parity.mjs proves one converter (Bedrock) drops one breakpoint, measured
// against a hand-built direct Converse payload. prompt-caching-extension.mjs proves a cache read
// happens at all, single-turn, on five backends. Neither answers the question a gateway actually
// has to answer: does the SAME conversation cache the SAME way no matter which provider is behind
// it? That is the gateway's promise, and it is only testable by sweeping providers x models.
//
// So this folder sweeps 34 (provider, model) cells covering three model families, both the
// Anthropic and OpenAI families on each of bedrock/vertex/azure, and several generations back per
// provider. A Claude cell that caches on one provider and not another is a finding regardless of
// whether the underlying transform is "documented": the client sent the same bytes and got
// different economics.
//
// TWO ARMS
// -------------------------------------------------------------------------------------------
//   control - a plain multi-turn conversation, no mid-conversation system message
//   midconv - the same conversation plus a mid-conversation role:"system" turn
//
// The arms are NOT compared to each other - they carry different content, so they hold different
// cache entries by construction. Each arm is compared to ITSELF across rounds.
//
// TWO BARS, BECAUSE THERE ARE TWO CACHING MECHANISMS
// -------------------------------------------------------------------------------------------
// An earlier revision of this file held every cell to one 0.85 floor. That was wrong, and the
// error is worth recording because it is easy to repeat: a uniform floor assumes a warm,
// byte-identical repeat DETERMINISTICALLY reads back. That holds for explicit cache_control
// breakpoints. It is simply not on offer for implicit caching.
//
// Measured directly against Google, with no gateway in the path, four consecutive byte-identical
// requests produced: gemini-2.5-pro 0%/0%/78%/0%, gemini-2.5-flash 0%/97%/0%/0%,
// gemini-3-flash-preview 0%/52%/52%/52%. The hit rate flips between zero and a fixed per-model
// plateau with no discernible pattern, and the plateau itself (52% / 78% / 97%) is block
// granularity, not a defect. A single round-2 sample of that is a coin flip reported as a verdict.
//
//   explicit (Claude, cache_control) - 2 rounds; round 2 must clear HIT_RATE_FLOOR. Deterministic,
//     so a miss is a real gateway defect: a dropped breakpoint or a perturbed prefix.
//   implicit (OpenAI/Gemini)         - IMPLICIT_ROUNDS rounds; asserts only that caching engaged
//     at least once, and REPORTS the per-round series plus the best. That catches "the gateway
//     broke caching entirely" while refusing to manufacture a verdict out of provider variance.
//
// OpenAI additionally routes cache lookups by prefix hash, so back-to-back requests can land on
// different machines and miss for reasons unrelated to the payload. Cells on the openai provider
// therefore send prompt_cache_key for affinity - the same thing the collection's hand-written
// Round 31 caching rows do.
//
// SIZING: WHY EVERY SEGMENT IS ~5.9K TOKENS AND NOT SMALLER
// -------------------------------------------------------------------------------------------
// The minimum cacheable prompt is per-model and NOT monotonic across generations: 512 tokens on
// Opus 5, 1024 on Opus 4.8 / Sonnet 5 / Sonnet 4.6, 2048 on Opus 4.7, and 4096 on Opus 4.6 /
// Opus 4.5 / Haiku 4.5 (Gemini 2.5 Flash implicit caching needs 2048). A prompt below a model's
// floor is not rejected - it silently produces cache_creation_input_tokens: 0, which is
// indistinguishable from the bug this suite hunts. Segments are therefore sized against the
// WORST floor in the matrix (4096), not the target model's, which is why {{cachePrefix}} (~5.9K
// tokens) is used whole and never sliced. Four segments puts each cell near 24K tokens.
//
// COST/CONCURRENCY: run this folder with PARALLEL=0. The harness forks one newman per provider,
// and these rows match six of them (openai/anthropic/gemini/vertex/bedrock/azure), so a default
// parallel run would execute every request up to six times over.

import { createHash } from "node:crypto";

const J = (v) => JSON.stringify(v);

// Hit-rate floor for EXPLICIT-breakpoint cells only. A warm byte-identical repeat reads
// essentially the whole prompt; anything that drops a breakpoint or perturbs the bytes lands near
// or below half. 0.85 sits far outside both, so tokenizer noise and the uncached tail can't flip
// it. Deliberately NOT applied to implicit-caching cells - see the header.
const HIT_RATE_FLOOR = 0.85;

// Rounds for implicit-caching cells. Four is enough to distinguish "this provider caches this
// conversation sometimes" from "this conversation never caches": across the direct-baseline runs
// above, every model that cached at all did so within the first four rounds.
const IMPLICIT_ROUNDS = 4;

const SEG = "{{cachePrefix}}";
const REMINDER =
  "Reminder: stay concise, cite the manual by section, and never speculate beyond the provided text.";
const QUESTION = "Reply with ONLY the word ACKNOWLEDGED and nothing else.";
const ACK = "OK.";

// Large enough for a thinking-by-default model (Opus 5 thinks unless told otherwise) to finish or
// hit the cap without erroring. Usage is reported either way, and usage is all this suite reads -
// so no `thinking` field is sent at all, keeping the payload valid across every provider here.
const MAX_TOKENS = 256;

// ...but usage is NOT all the harness reads: the collection-level test asserts every 2xx inference
// response carries non-empty content, and it only forgives an empty body on finish_reason "stop".
// o-series/gpt-5 reasoning models bill reasoning against the same completion budget, so at 256 they
// spend the whole cap thinking about a one-word answer and return finish_reason "length" with
// content:"" - which reads as a caching failure when the cache actually worked (those rounds
// reported cached_tokens 16512). Give just those cells enough headroom to emit the ack after
// reasoning. Kept per-cell rather than raising MAX_TOKENS globally so the other ~30 cells x 2 arms
// x 4 rounds don't pay for it. The value must stay identical across rounds - every round of a cell
// sends byte-identical bytes on purpose, and that is what makes the cache measurement meaningful.
const REASONING_MAX_TOKENS = 4096;

// Cells whose default reasoning effort burns the completion budget before any visible text. Newer
// siblings (gpt-5.6, gpt-5.6-sol) answered fine at 256 and are deliberately not listed - this is a
// remedy for observed truncation, not a blanket policy for the model family.
//
// The Claude entry is the same failure in the adaptive-thinking family. Sonnet 5 rejects
// thinking:{type:"enabled"} with a 400 and is adaptive-only, so it thinks by default and there is
// no budget_tokens knob to turn it down with
// (https://platform.claude.com/docs/en/build-with-claude/extended-thinking). At 256 it spends the
// entire cap thinking about a one-word answer and the turn ends mid-thinking, which returns
// stop_reason "max_tokens" with content:[] - reproduced directly against Bedrock:
//
//   max_tokens 64  -> stop_reason max_tokens, content [], output_tokens 64
//   unconstrained  -> content ["thinking", "text"]
//
// That empty content array reads as a caching failure when the cache actually worked (the failing
// rounds reported cache_creation/cache_read of 29727). Its adaptive-only siblings already in the
// matrix (vertex/claude-opus-4-7, vertex/claude-opus-4-8, bedrock_mantle/anthropic.claude-opus-4-8)
// answered inside 256 and stay unlisted, per the observed-truncation-only rule above.
const REASONING_BUDGET_MODELS = new Set([
  "openai/gpt-5",
  "openai/gpt-5-mini",
  "bedrock/global.anthropic.claude-sonnet-5",
]);

const maxTokensFor = (cell) => (REASONING_BUDGET_MODELS.has(cell.model) ? REASONING_MAX_TOKENS : MAX_TOKENS);

// OpenAI rejects prompt_cache_key longer than 64 chars at runtime
// ("string_above_max_length ... got a string with length 65"). Note the published spec does NOT
// declare that bound on the chat/responses request schema - only on CompactResponseMethodPublicBody
// - so this is enforced-but-undocumented and can't be caught by schema validation.
//
// cellId has no length bound: it embeds cell.model, which already carries its own provider prefix,
// so the provider lands in the key twice ("bedrock__gpt__bedrock-openai-gpt-5-6-sol__control" = 65).
// Dropping the redundant provider would fit today at 63 of 64, which is one longer model name away
// from breaking again - so bound it instead. The readable form is kept whenever it fits, and a
// sha256 suffix takes over when it doesn't, which keeps distinct cells distinct.
//
// Deterministic on purpose: the generated collection is diffed, and every round of a cell must send
// byte-identical bytes for the cache measurement to mean anything.
const CACHE_KEY_MAX = 64;
const cacheKeyFor = (cellId) => {
  const full = `bf-cache-matrix-${cellId}`;
  if (full.length <= CACHE_KEY_MAX) return full;
  const digest = createHash("sha256").update(cellId).digest("hex").slice(0, 12);
  return `${full.slice(0, CACHE_KEY_MAX - digest.length - 1)}-${digest}`;
};

// Per-cell cold cache. Providers match cached prefixes account-wide, so without a salt the first
// cell to run writes a prefix every later cell reads, and the matrix measures request ordering
// instead of the gateway. {{pcNonce}} (set once per run by the collection-level pre-request
// script) additionally keeps a re-run inside the TTL from inheriting the previous run's cache.
const salt = (cellId) => `[cache-matrix cell ${cellId} run {{pcNonce}}]\n\n${SEG}`;

// ---------------------------------------------------------------------------------------------
// The matrix. `shape` selects the wire format and therefore the usage extractor:
//   "anthropic" -> POST /anthropic/v1/messages, native cache_read/cache_creation/input fields
//   "chat"      -> POST /v1/chat/completions,  normalized prompt_tokens_details.cached_tokens
// Claude cells use the Anthropic shape because that is Claude Code's real entry point and the
// only shape that carries cache_control; everything else uses the unified chat route.
//
// Model IDs are exactly those probed reachable against this account - notably
// bedrock/openai.gpt-oss-120b-1:0 is NOT here (404s; the collection's bedrockOpenaiModel default
// is stale) and bedrock/openai.gpt-5.6-sol stands in for the OpenAI-family-on-Bedrock cell.
// ---------------------------------------------------------------------------------------------
const CELLS = [
  // --- Anthropic API, Claude family: latest through several generations back -----------------
  { provider: "anthropic", family: "claude", model: "anthropic/claude-opus-5", shape: "anthropic", mechanism: "explicit" },
  { provider: "anthropic", family: "claude", model: "anthropic/claude-opus-4-8", shape: "anthropic", mechanism: "explicit" },
  { provider: "anthropic", family: "claude", model: "anthropic/claude-opus-4-7", shape: "anthropic", mechanism: "explicit" },
  { provider: "anthropic", family: "claude", model: "anthropic/claude-sonnet-5", shape: "anthropic", mechanism: "explicit" },
  { provider: "anthropic", family: "claude", model: "anthropic/claude-sonnet-4-6", shape: "anthropic", mechanism: "explicit" },
  { provider: "anthropic", family: "claude", model: "anthropic/claude-haiku-4-5", shape: "anthropic", mechanism: "explicit" },

  // --- Bedrock, Claude family: the converter this suite's sibling folder fixed ---------------
  { provider: "bedrock", family: "claude", model: "bedrock/global.anthropic.claude-opus-4-8", shape: "anthropic", mechanism: "explicit" },
  { provider: "bedrock", family: "claude", model: "bedrock/global.anthropic.claude-opus-4-7", shape: "anthropic", mechanism: "explicit" },
  { provider: "bedrock", family: "claude", model: "bedrock/global.anthropic.claude-opus-4-5-20251101-v1:0", shape: "anthropic", mechanism: "explicit" },
  { provider: "bedrock", family: "claude", model: "bedrock/global.anthropic.claude-sonnet-5", shape: "anthropic", mechanism: "explicit" },
  { provider: "bedrock", family: "claude", model: "bedrock/global.anthropic.claude-sonnet-4-6", shape: "anthropic", mechanism: "explicit" },
  { provider: "bedrock", family: "claude", model: "bedrock/global.anthropic.claude-haiku-4-5-20251001-v1:0", shape: "anthropic", mechanism: "explicit" },

  // --- Vertex, Claude family: hoists mid-conversation system instead of inlining -------------
  { provider: "vertex", family: "claude", model: "vertex/claude-opus-4-8", shape: "anthropic", mechanism: "explicit" },
  { provider: "vertex", family: "claude", model: "vertex/claude-opus-4-7", shape: "anthropic", mechanism: "explicit" },
  { provider: "vertex", family: "claude", model: "vertex/claude-sonnet-4-6", shape: "anthropic", mechanism: "explicit" },

  // --- Azure (Microsoft Foundry), Claude family ----------------------------------------------
  { provider: "azure", family: "claude", model: "azure/claude-haiku-4-5", shape: "anthropic", mechanism: "explicit" },

  // --- Bedrock Mantle, both families ----------------------------------------------------------
  { provider: "bedrock_mantle", family: "claude", model: "bedrock_mantle/anthropic.claude-opus-4-8", shape: "anthropic", mechanism: "explicit" },
  { provider: "bedrock_mantle", family: "gpt", model: "bedrock_mantle/openai.gpt-5.6-sol", shape: "chat", mechanism: "implicit" },

  // --- OpenAI API, GPT family: latest through several generations back -----------------------
  { provider: "openai", family: "gpt", model: "openai/gpt-5.6", shape: "chat", mechanism: "implicit" },
  { provider: "openai", family: "gpt", model: "openai/gpt-5.6-sol", shape: "chat", mechanism: "implicit" },
  { provider: "openai", family: "gpt", model: "openai/gpt-5", shape: "chat", mechanism: "implicit" },
  { provider: "openai", family: "gpt", model: "openai/gpt-5-mini", shape: "chat", mechanism: "implicit" },
  { provider: "openai", family: "gpt", model: "openai/gpt-4.1", shape: "chat", mechanism: "implicit" },
  { provider: "openai", family: "gpt", model: "openai/gpt-4o-mini", shape: "chat", mechanism: "implicit" },

  // --- Bedrock and Azure, OpenAI family -------------------------------------------------------
  // Bedrock strips cachePoint for non-Anthropic/Nova models (BedrockModelSupportsCachePoints),
  // so this cell can only exercise whatever implicit caching Bedrock does for gpt-5.6-sol.
  { provider: "bedrock", family: "gpt", model: "bedrock/openai.gpt-5.6-sol", shape: "chat", mechanism: "implicit" },
  { provider: "azure", family: "gpt", model: "azure/gpt-4o", shape: "chat", mechanism: "implicit" },
  { provider: "azure", family: "gpt", model: "azure/gpt-4o-mini", shape: "chat", mechanism: "implicit" },

  // --- Gemini API and Gemini-on-Vertex --------------------------------------------------------
  { provider: "gemini", family: "gemini", model: "gemini/gemini-3.1-pro-preview", shape: "chat", mechanism: "implicit" },
  { provider: "gemini", family: "gemini", model: "gemini/gemini-3-flash-preview", shape: "chat", mechanism: "implicit" },
  { provider: "gemini", family: "gemini", model: "gemini/gemini-2.5-pro", shape: "chat", mechanism: "implicit" },
  { provider: "gemini", family: "gemini", model: "gemini/gemini-2.5-flash", shape: "chat", mechanism: "implicit" },
  { provider: "gemini", family: "gemini", model: "gemini/gemini-2.5-flash-lite", shape: "chat", mechanism: "implicit" },
  { provider: "vertex", family: "gemini", model: "vertex/gemini-2.5-pro", shape: "chat", mechanism: "implicit" },
  { provider: "vertex", family: "gemini", model: "vertex/gemini-2.5-flash", shape: "chat", mechanism: "implicit" },
];

const ARMS = [
  { key: "control", midconv: false },
  { key: "midconv", midconv: true },
];

// ---------------------------------------------------------------------------------------------
// Body builders.
// ---------------------------------------------------------------------------------------------

// Anthropic Messages shape. cache_control is only meaningful for the Claude family, which is the
// only family routed here.
function anthropicBody(cell, arm, cellId) {
  const cc = { type: "ephemeral" };
  const messages = [
    { role: "user", content: [{ type: "text", text: `${SEG}\n\nDocument A. Reply ${ACK}` }] },
    { role: "assistant", content: [{ type: "text", text: ACK }] },
    { role: "user", content: [{ type: "text", text: `${SEG}\n\nDocument B. Reply ${ACK}` }] },
    { role: "assistant", content: [{ type: "text", text: ACK }] },
  ];

  if (arm.midconv) {
    // The third breakpoint rides the mid-conversation system turn - the shape that loses its
    // anchor on a converter that inlines without carrying cache_control through.
    //
    // Placement matters, and Anthropic's rule has TWO clauses: the system turn must FOLLOW a
    // user message (or an assistant message ending in server-tool use), AND be either last or
    // followed by an assistant turn. The tempting [..., assistant, system, user] shape violates
    // both and is rejected with "messages.N: role 'system' must follow a 'user' message" on the
    // models that forward it natively (Opus 4.8+/Opus 5) - while models that don't support it
    // get hoisted instead and never surface the error, which makes the failure look
    // model-specific when it is really placement-specific. Trailing the user turn satisfies
    // both clauses on every provider in this matrix.
    messages.push({ role: "user", content: [{ type: "text", text: QUESTION }] });
    messages.push({ role: "system", content: [{ type: "text", text: REMINDER, cache_control: cc }] });
  } else {
    messages.push({
      role: "user",
      content: [{ type: "text", text: REMINDER, cache_control: cc }, { type: "text", text: QUESTION }],
    });
  }

  return {
    model: cell.model,
    max_tokens: maxTokensFor(cell),
    system: [
      { type: "text", text: salt(cellId), cache_control: cc },
      { type: "text", text: SEG, cache_control: cc },
    ],
    messages,
  };
}

// Unified chat shape for the OpenAI and Gemini families. No cache_control: neither family has the
// concept (OpenAI 5.6+ uses prompt_cache_breakpoint, older models and Gemini cache implicitly over
// a stable prefix). The measurement is unchanged - a byte-identical repeat must read back what it
// wrote - so what this arm actually detects for these families is the gateway failing to render
// the same conversation to the same bytes twice.
function chatBody(cell, arm, cellId) {
  const messages = [
    { role: "system", content: salt(cellId) },
    { role: "system", content: SEG },
    { role: "user", content: `${SEG}\n\nDocument A. Reply ${ACK}` },
    { role: "assistant", content: ACK },
    { role: "user", content: `${SEG}\n\nDocument B. Reply ${ACK}` },
    { role: "assistant", content: ACK },
  ];

  if (arm.midconv) {
    messages.push({ role: "system", content: REMINDER });
    messages.push({ role: "user", content: QUESTION });
  } else {
    messages.push({ role: "user", content: `${REMINDER}\n\n${QUESTION}` });
  }

  const body = { model: cell.model, max_tokens: maxTokensFor(cell), messages };
  // OpenAI routes cache lookups by prefix hash; without an affinity key, consecutive identical
  // requests can land on different machines and miss for reasons that have nothing to do with the
  // payload. This is not hypothetical: azure/gpt-4o-mini measured 0% on both arms without the key
  // and 99% on every warm round with it, and openai/gpt-5-mini went 0% -> 99% the same way. Sent
  // on every OpenAI-family cell; acceptance was probed on all four surfaces (openai, azure,
  // bedrock, bedrock_mantle) rather than assumed, since an unsupported parameter is a 400 and a
  // 400 reads as a caching failure. Gemini has no equivalent and must not receive it.
  if (cell.family === "gpt") body.prompt_cache_key = cacheKeyFor(cellId);
  return body;
}

// ---------------------------------------------------------------------------------------------
// Usage extraction. The two shapes disagree about what "input tokens" counts, and getting it
// wrong is exactly how a cache defect hides: Anthropic reports uncached input EXCLUDING cached
// reads, while the OpenAI-normalized shape reports a TOTAL with cached reads folded in. Both are
// normalized to {read, write, uncached} so one hit-rate formula means the same thing everywhere.
// ---------------------------------------------------------------------------------------------
const EXTRACT = {
  anthropic: `
var j = pm.response.json();
var u = j.usage || {};
var read = u.cache_read_input_tokens || 0;
var write = u.cache_creation_input_tokens || 0;
var uncached = u.input_tokens || 0;`.trim(),

  chat: `
var j = pm.response.json();
var u = j.usage || {};
var d = u.prompt_tokens_details || u.input_tokens_details || {};
var read = d.cached_tokens || 0;
var write = d.cached_write_tokens || d.cache_write_tokens || 0;
// prompt_tokens is the TOTAL with cached reads included; subtract them back out so "uncached"
// means the same thing it does on the Anthropic shape. Writes are billed separately and are
// already excluded from the total, so they are not subtracted.
var uncached = Math.max(0, (u.prompt_tokens || u.input_tokens || 0) - read);`.trim(),
};

const HIT_RATE = `
var billed = read + write + uncached;
var hitRate = billed > 0 ? read / billed : 0;
var detail = 'read=' + read + ' write=' + write + ' uncached=' + uncached +
  ' hitRate=' + (hitRate * 100).toFixed(1) + '%';`.trim();

// ---------------------------------------------------------------------------------------------
// Item construction.
// ---------------------------------------------------------------------------------------------
function requestFor(cell, body) {
  const raw = JSON.stringify(body, null, 2);
  const path = cell.shape === "anthropic" ? ["anthropic", "v1", "messages"] : ["v1", "chat", "completions"];
  return {
    method: "POST",
    header: [{ key: "Content-Type", value: "application/json" }],
    body: { mode: "raw", raw },
    url: { raw: `{{baseUrl}}/${path.join("/")}`, host: ["{{baseUrl}}"], path },
  };
}

const buildBody = (cell, arm, cellId) =>
  cell.shape === "anthropic" ? anthropicBody(cell, arm, cellId) : chatBody(cell, arm, cellId);

// Round 1 warms a cold, per-cell-salted prefix, so it has nothing to read. Recorded (the write
// count is what proves the prefix was cacheable at all) but asserted only for success.
function round1Script(cell, cellId) {
  return `
${EXTRACT[cell.shape]}
${HIT_RATE}
pm.test(${J(`Cache matrix [${cellId}] round 1 (write) succeeds`)}, function () {
  pm.expect(pm.response.code, 'request failed: ' + pm.response.text()).to.be.below(400);
});
if (pm.response.code < 400) {
  console.log('[cache-matrix] ' + ${J(cellId)} + ' round1(write) ' + detail);
}`.trim();
}

// Terminal round for an EXPLICIT-breakpoint cell: deterministic, so assert the floor.
function round2Script(cell, arm, cellId) {
  const label = `${cell.model} / ${arm.key}`;
  return `
${EXTRACT[cell.shape]}
${HIT_RATE}
pm.test(${J(`Cache matrix [${label}] round 2 (read) succeeds`)}, function () {
  pm.expect(pm.response.code, 'request failed: ' + pm.response.text()).to.be.below(400);
});
if (pm.response.code < 400) {
  console.log('CACHE_MATRIX_REPORT', JSON.stringify({
    cell: ${J(cellId)},
    provider: ${J(cell.provider)},
    family: ${J(cell.family)},
    model: ${J(cell.model)},
    shape: ${J(cell.shape)},
    mechanism: ${J(cell.mechanism)},
    arm: ${J(arm.key)},
    read: read, write: write, uncached: uncached, hitRate: hitRate,
    // Carried so render-cache-parity-report.mjs scores this cell against the same floor the
    // assertion below uses, rather than duplicating the constant and drifting from it.
    hitRateFloor: ${HIT_RATE_FLOOR}
  }));
  pm.test(${J(`Cache matrix [${label}] reads the whole warm prefix`)}, function () {
    pm.expect(hitRate, detail +
      ' - byte-identical bytes were sent in round 1, so the warm prefix should have been read back. ' +
      'An explicit cache_control breakpoint is deterministic, so a miss here is a dropped ' +
      'breakpoint or a perturbed prefix, not provider variance.').to.be.at.least(${HIT_RATE_FLOOR});
  });
}`.trim();
}

// One round of an IMPLICIT-caching cell. Every round appends its hit rate to a collection
// variable; the final round asserts on the accumulated series and reports it. Asserting per-round
// would fail on providers that legitimately miss some rounds (see the header's direct-baseline
// measurements), which is exactly the mistake this shape exists to avoid.
function implicitRoundScript(cell, arm, cellId, round, isLast) {
  const label = `${cell.model} / ${arm.key}`;
  const seriesVar = `cm_series_${cellId}`;
  return `
${EXTRACT[cell.shape]}
${HIT_RATE}
pm.test(${J(`Cache matrix [${label}] round ${round} succeeds`)}, function () {
  pm.expect(pm.response.code, 'request failed: ' + pm.response.text()).to.be.below(400);
});
if (pm.response.code < 400) {
  var series = [];
${
  round === 1
    ? `  // Round 1 starts a fresh series. Collection variables persist for the whole newman
  // process, so without this reset a second iteration would append to the first one's series and
  // the final round's "best" could be satisfied by a hit recorded in an earlier iteration —
  // reporting a pass for a run in which caching never engaged. That is precisely the false green
  // this assertion exists to prevent.`
    : `  try { series = JSON.parse(pm.collectionVariables.get(${J(seriesVar)}) || '[]'); } catch (e) { series = []; }`
}
  series.push(hitRate);
  pm.collectionVariables.set(${J(seriesVar)}, JSON.stringify(series));
${
  isLast
    ? `  var best = series.reduce(function (a, b) { return b > a ? b : a; }, 0);
  var pcts = series.map(function (h) { return (h * 100).toFixed(0) + '%'; }).join(' ');
  console.log('CACHE_MATRIX_REPORT', JSON.stringify({
    cell: ${J(cellId)},
    provider: ${J(cell.provider)},
    family: ${J(cell.family)},
    model: ${J(cell.model)},
    shape: ${J(cell.shape)},
    mechanism: ${J(cell.mechanism)},
    arm: ${J(arm.key)},
    read: read, write: write, uncached: uncached,
    // Implicit cells assert only "cached at least once", so the renderer scores hitRate > 0 for
    // them; hitRateFloor is carried anyway so the report can show the bar that was applied.
    hitRate: best, series: series, hitRateFloor: 0
  }));
  // Deliberately only "caching engaged at least once". Implicit caching is best-effort and
  // non-deterministic at the provider — measured directly against Google, byte-identical repeats
  // alternate between 0% and a fixed per-model plateau. A higher bar here would report provider
  // luck as gateway health. Zero across every round is still meaningful: it means this
  // conversation never cached at all, which a gateway defect would produce.
  pm.test(${J(`Cache matrix [${label}] caches at least once across ${IMPLICIT_ROUNDS} rounds`)}, function () {
    pm.expect(best, 'per-round hit rates: ' + pcts + ' (last round ' + detail + ')').to.be.above(0);
  });`
    : `  console.log('[cache-matrix] ' + ${J(cellId)} + ' round ${round} ' + detail);`
}
}`.trim();
}

// Cache writes are readable by the next request on these providers, but the harness fires cells
// back to back with no other latency in between; a short settle keeps a round-2 miss from being
// blamed on the gateway when it was really propagation.
const SETTLE = `var __t = Date.now(); while (Date.now() - __t < 1200) { /* settle: let the round-1 cache write land */ }`;

export function buildCrossProviderCacheMatrixItems() {
  const items = [];

  for (const cell of CELLS) {
    for (const arm of ARMS) {
      const cellId = `${cell.provider}__${cell.family}__${cell.model.replace(/[^a-zA-Z0-9]+/g, "-")}__${arm.key}`;
      const body = buildBody(cell, arm, cellId);
      const shortName = `${cell.provider}/${cell.model.split("/").pop()} / ${arm.key}`;

      // Every round below sends byte-identical bytes on purpose: nothing about the request
      // changes, so any difference in what got cached is attributable to the gateway (explicit)
      // or to provider variance (implicit).
      if (cell.mechanism === "explicit") {
        items.push({
          name: `Cache matrix: ${shortName} round 1 (write)`,
          event: [
            { listen: "test", script: { type: "text/javascript", exec: round1Script(cell, cellId).split("\n") } },
          ],
          request: requestFor(cell, body),
        });
        items.push({
          name: `Cache matrix: ${shortName} round 2 (read)`,
          event: [
            { listen: "prerequest", script: { type: "text/javascript", exec: [SETTLE] } },
            { listen: "test", script: { type: "text/javascript", exec: round2Script(cell, arm, cellId).split("\n") } },
          ],
          request: requestFor(cell, body),
        });
      } else {
        for (let round = 1; round <= IMPLICIT_ROUNDS; round++) {
          const isLast = round === IMPLICIT_ROUNDS;
          items.push({
            name: `Cache matrix: ${shortName} round ${round}${isLast ? " (verdict)" : ""}`,
            event: [
              ...(round > 1
                ? [{ listen: "prerequest", script: { type: "text/javascript", exec: [SETTLE] } }]
                : []),
              {
                listen: "test",
                script: {
                  type: "text/javascript",
                  exec: implicitRoundScript(cell, arm, cellId, round, isLast).split("\n"),
                },
              },
            ],
            request: requestFor(cell, body),
          });
        }
      }
    }
  }

  return items;
}

export function buildCrossProviderCacheMatrixFolder() {
  const items = buildCrossProviderCacheMatrixItems();
  const providers = [...new Set(CELLS.map((c) => c.provider))].sort().join(", ");
  return {
    name: "Cross-Cut Round 35: Cross-Provider Prompt-Cache Matrix (generated)",
    description:
      `Generated at harness runtime. ${CELLS.length} (provider, model) cells across ${providers}, covering the Claude, OpenAI, and Gemini model families - including both the Anthropic and OpenAI families on each of bedrock, vertex, and azure, and several generations back per provider. ` +
      "Each cell runs a ~24K-token multi-turn conversation in two arms (control, and one carrying a mid-conversation role:\"system\" turn), sending byte-identical bytes every round. " +
      `Two bars, because there are two caching mechanisms: EXPLICIT cells (Claude, cache_control) run 2 rounds and must clear read / (read + write + uncached) >= ${HIT_RATE_FLOOR} on round 2 - deterministic, so a miss is a real defect. IMPLICIT cells (OpenAI/Gemini) run ${IMPLICIT_ROUNDS} rounds and assert only that caching engaged at least once, reporting the per-round series. ` +
      "That split exists because implicit caching is non-deterministic at the provider: measured directly against Google with no gateway in the path, byte-identical repeats alternate between 0% and a fixed per-model plateau (52% / 78% / 97% depending on model). A single-sample floor there reports provider luck as gateway health. " +
      "The hit rate is deliberately NOT read / prompt_tokens, which counts a cache write as a miss. Arms are compared to themselves across rounds, never to each other (they hold different cache entries by construction). " +
      "Cells on the openai provider send prompt_cache_key, because OpenAI routes cache lookups by prefix hash and back-to-back requests can otherwise miss on routing rather than payload. " +
      "Segments use {{cachePrefix}} whole (~5.9K tokens) because the minimum cacheable prompt is per-model and non-monotonic (512 on Opus 5, 4096 on Opus 4.6/4.5/Haiku 4.5) - a prompt under a model's floor silently fails to cache with no error. " +
      "Results are emitted as CACHE_MATRIX_REPORT console lines. RUN WITH PARALLEL=0: these rows match six provider forks, so a default parallel run executes every request up to six times.",
    item: items,
  };
}
