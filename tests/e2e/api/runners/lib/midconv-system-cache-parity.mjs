// Generates the "Mid-Conversation System Cache-Anchor Parity" folder consumed by
// augment-provider-harness.mjs.
//
// WHY THIS EXISTS (and why the neighbouring caching rows can't see what it tests)
// -------------------------------------------------------------------------------------------
// prompt-caching-extension.mjs already proves a cache READ happens at all: same {{cachePrefix}}
// system block, three single-turn requests, assert cached_tokens > 0. That shape structurally
// cannot detect a lost breakpoint in the MESSAGES array, because it has no conversation body -
// the two system breakpoints alone account for the whole prompt, so a request that caches only
// the system floor still reports a ~100% hit and passes.
//
// The traffic shape that breaks is Claude Code's: two cache_control breakpoints in `system`,
// plus a third riding a mid-conversation role:"system" turn (a <system-reminder>), on top of a
// LARGE conversation body. On Bedrock, ConvertBifrostMessagesToBedrockMessages inlines that
// mid-conversation system message as a user turn (Bedrock has no message-level system role) via
// convertBifrostSystemReminderToBedrockUserMessage. That inlining originally built Text blocks
// only and never read block.CacheControl: three breakpoints in, two cachePoints out, both
// survivors sitting in `system`, so the cacheable prefix was pinned at the system floor and the
// entire conversation body was re-read uncached every turn. The converter now carries the
// breakpoint through; these cells exist to catch a regression back to that state.
//
// The measurement therefore needs (a) a conversation body big enough that the system floor is
// only ~half the prompt, so "cached the system only" and "cached everything" are far apart in
// token terms, and (b) an otherwise-identical control arm, so the ONLY variable is which role
// the third breakpoint rides on.
//
// HOW IT MEASURES
// -------------------------------------------------------------------------------------------
// Each case runs two rounds - round 1 warms the prefix (write, informational), round 2 sends the
// byte-identical request and asserts on the hit rate. Hit rate is read / (read + write +
// uncached), NOT read / prompt_tokens: the latter counts a cache WRITE as a miss, which is the
// exact metric-definition artifact the vendor report calls out as having masked this defect from
// both sides for two weeks.
//
// Two arms carry the experiment:
//   control  - third breakpoint rides a role:"user" block  -> Bifrost preserves it (3 cachePoints)
//   midconv  - third breakpoint rides a role:"system" turn -> Bifrost preserves it (3 cachePoints)
//              Before the fix this arm emitted only 2: the inlining discarded the block's
//              cache_control, leaving both survivors in `system`.
// and three legs run each arm:
//   direct           - Bedrock Converse over SigV4, payload hand-built with all three cachePoint
//                      elements explicitly placed. This is GROUND TRUTH: it is what Bifrost would
//                      emit if the breakpoint survived inlining, so a direct/bifrost split on the
//                      midconv arm isolates the converter as the only remaining variable.
//   bifrost_messages - POST /anthropic/v1/messages (Claude Code's actual entry point)
//   bifrost_responses- POST /openai/v1/responses   (second reported repro path)
// Expected now that the converter carries the breakpoint through: every cell passes, both arms,
// all three legs. Before the fix the two bifrost/midconv cells failed at roughly half the hit rate
// of their control twin (measured 49.8% vs 99.9% on a warm ~19K prompt) — that gap is what these
// cells were built to detect, and a return to it is what a failure here now means.
//
// Four single-leg variants pin the surrounding behaviour so a future fix can't over-correct:
// role:"developer" (same dispatch branch), a ContentStr-form system message (carries no per-block
// cache_control, so it must NOT grow a breakpoint), a reminder placed last, and two reminders
// each carrying cache_control (the cache-checkpoint budget - Bedrock caps Claude at 4).
//
// Credentials/variables are the ones this harness already wires from Infisical (Makefile:2097):
// {{bedrockDirectAccessKeyId}} / {{bedrockDirectSecretAccessKey}} / {{bedrockDirectRegion}} for
// the SigV4 leg, {{cachePrefix}} for the bulk text, {{pcNonce}} for per-run cache isolation.

const J = (v) => JSON.stringify(v);

// Haiku 4.5 rather than {{bedrockModel}} (Opus 4.7). The inlining branch is gated on
// schemas.IsAnthropicModelFamily, NOT on the Opus-4.8+ SupportsMidConversationSystem predicate
// the vendor report frames this around, so every Claude model on Bedrock takes it and the
// cheapest one reproduces the defect identically. Same model id the existing Round 16 bedrock
// caching rows use, so model access is already proven for this account.
const BEDROCK_MODEL = "global.anthropic.claude-haiku-4-5-20251001-v1:0";
const BIFROST_MODEL = `bedrock/${BEDROCK_MODEL}`;

// Hit-rate floor for a warm, byte-identical repeat. A correctly-anchored request reads
// essentially the whole prompt; the failure mode this hunts halves it. 0.85 sits far outside
// both, so neither tokenizer boundary noise nor the handful of uncached tail tokens can flip it.
const HIT_RATE_FLOOR = 0.85;

// Haiku 4.5's minimum cacheable prompt is 4096 tokens (AWS prompt-caching limits table; the
// ladder is NOT monotonic across generations - Opus 5 is 512 and Opus 4.7 is 2048). A breakpoint
// below a model's floor is silently ignored rather than rejected, which would look exactly like
// the bug this file hunts. {{cachePrefix}} is ~5.9K tokens and every segment below carries one,
// so every breakpoint clears the worst floor in play. Four segments puts the prompt near 24K: system ~12K, body ~12K, i.e. the
// ~50/50 split that makes "system floor only" unmistakable next to "whole prefix".
const SEG = "{{cachePrefix}}";

const REMINDER =
  "Reminder: stay concise, cite the manual by section, and never speculate beyond the provided text.";
const QUESTION = "Reply with ONLY the word ACKNOWLEDGED and nothing else.";
const ACK = "OK.";

// The envelope convertBifrostSystemReminderToBedrockUserMessage wraps inlined reminders in.
// The direct leg has to reproduce it byte-for-byte, since its whole job is to stand in for what
// Bifrost should have emitted.
const wrapReminder = (text) => `<system-reminder>\n${text}\n</system-reminder>\n`;

// Per-cell cache isolation. Bedrock matches the longest cached prefix at or before each
// breakpoint, account-wide - it has no notion of "these two harness rows are logically separate".
// Without a salt the first cell to reach Bedrock writes the prefix and every later cell reads
// it, so the direct leg would warm the cache that the bifrost leg then hits and the defect would
// vanish into request ordering. Salting the FIRST system block with the cell id gives each cell
// its own cold cache; {{pcNonce}} (set once per run by the collection-level pre-request script)
// keeps a re-run inside the 5-minute TTL from inheriting the previous run's cache.
const salt = (cellId) => `[cache-anchor cell ${cellId} run {{pcNonce}}]\n\n${SEG}`;

// ---------------------------------------------------------------------------------------------
// Case matrix. `tail` describes what carries the third breakpoint; every other part of the
// conversation is identical across cases so the tail is the only variable.
// ---------------------------------------------------------------------------------------------
const CASES = [
  {
    key: "control",
    label: "control - third breakpoint on a role:user block",
    tail: "user",
    legs: ["direct", "bifrost_messages", "bifrost_responses"],
    expectHit: true,
    note: "Baseline. Bifrost preserves a cache_control that rides a user block, so all three legs should read the whole prefix. If THIS fails, the harness or the account is the problem, not the converter.",
  },
  {
    key: "midconv",
    label: "defect - third breakpoint on a mid-conversation role:system turn",
    tail: "system",
    legs: ["direct", "bifrost_messages", "bifrost_responses"],
    expectHit: true,
    note: "Claude Code's shape. The direct leg sends the hand-inlined equivalent WITH its cachePoint and should match the control; the two Bifrost legs drop the breakpoint in convertBifrostSystemReminderToBedrockUserMessage and should land near half.",
  },
  {
    key: "midconv_developer",
    label: "variant - role:developer instead of role:system",
    tail: "developer",
    legs: ["bifrost_messages"],
    expectHit: true,
    note: "role:developer takes the same dispatch branch (responses.go checks both roles), so it must behave identically to the midconv arm - before and after any fix.",
  },
  {
    key: "midconv_last",
    label: "variant - reminder is the final message",
    tail: "system_last",
    legs: ["bifrost_messages"],
    expectHit: true,
    note: "Same drop, without a trailing user turn to merge into. Separates 'the breakpoint is lost' from 'the breakpoint is misplaced by the consecutive-same-role merge in responses.go'.",
  },
  {
    key: "midconv_contentstr",
    label: "guard - mid-conversation system sent as a plain string",
    tail: "system_str",
    legs: ["bifrost_messages"],
    expectHit: false,
    note: "The string form carries no per-block cache_control, so there is no third breakpoint to preserve and the system floor IS the correct answer here. Reported, never asserted high - this is the row that catches a fix which invents a breakpoint the client never sent.",
  },
  {
    key: "midconv_double",
    label: "guard - two mid-conversation reminders, each with cache_control",
    tail: "system_double",
    legs: ["bifrost_messages"],
    expectHit: true,
    note: "Bedrock caps Claude at 4 cache checkpoints (BedrockMaxCachePoints). 2 system + 2 reminders is exactly at the cap, so this row asserts the request is accepted rather than 400ing on checkpoint count. Requests that would exceed the cap are now trimmed by clampBedrockCachePoints, which drops the earliest markers; this cell sits at the boundary where nothing should be trimmed at all.",
  },
];

const LEG_LABEL = {
  direct: "direct Bedrock Converse (SigV4)",
  bifrost_messages: "Bifrost /anthropic/v1/messages",
  bifrost_responses: "Bifrost /openai/v1/responses",
};

// ---------------------------------------------------------------------------------------------
// Body builders - one per leg, all driven off the same case `tail` descriptor.
// ---------------------------------------------------------------------------------------------

// Anthropic Messages wire shape (the /anthropic/v1/messages leg).
function anthropicBody(kase, cellId) {
  const cc = { type: "ephemeral" };
  const messages = [
    { role: "user", content: [{ type: "text", text: `${SEG}\n\nDocument A. Reply ${ACK}` }] },
    { role: "assistant", content: [{ type: "text", text: ACK }] },
    { role: "user", content: [{ type: "text", text: `${SEG}\n\nDocument B. Reply ${ACK}` }] },
    { role: "assistant", content: [{ type: "text", text: ACK }] },
  ];

  switch (kase.tail) {
    case "user":
      messages.push({
        role: "user",
        content: [
          { type: "text", text: REMINDER, cache_control: cc },
          { type: "text", text: QUESTION },
        ],
      });
      break;
    case "system":
    case "developer":
      messages.push({
        role: kase.tail === "developer" ? "developer" : "system",
        content: [{ type: "text", text: REMINDER, cache_control: cc }],
      });
      messages.push({ role: "user", content: [{ type: "text", text: QUESTION }] });
      break;
    case "system_last":
      messages.push({ role: "user", content: [{ type: "text", text: QUESTION }] });
      messages.push({ role: "system", content: [{ type: "text", text: REMINDER, cache_control: cc }] });
      break;
    case "system_str":
      // Deliberately the string form: no content blocks, therefore no place to hang
      // cache_control. Only two breakpoints legitimately reach the provider here.
      messages.push({ role: "system", content: REMINDER });
      messages.push({ role: "user", content: [{ type: "text", text: QUESTION }] });
      break;
    case "system_double":
      messages.push({ role: "system", content: [{ type: "text", text: `${REMINDER} (first)`, cache_control: cc }] });
      messages.push({ role: "user", content: [{ type: "text", text: "Understood." }] });
      messages.push({ role: "system", content: [{ type: "text", text: `${REMINDER} (second)`, cache_control: cc }] });
      messages.push({ role: "user", content: [{ type: "text", text: QUESTION }] });
      break;
    default:
      throw new Error(`unknown tail: ${kase.tail}`);
  }

  return {
    model: BIFROST_MODEL,
    max_tokens: 32,
    temperature: 0,
    system: [
      { type: "text", text: salt(cellId), cache_control: cc },
      { type: "text", text: SEG, cache_control: cc },
    ],
    messages,
  };
}

// OpenAI Responses wire shape (the /openai/v1/responses leg). Same conversation, expressed as
// input items - cache_control is accepted on Responses content blocks (schemas/responses.go:1540,
// "Not in OpenAI's schemas, but sent by a few providers"), which is what makes this leg a
// genuine second path into the same Bedrock converter rather than a re-run of the first.
function responsesBody(kase, cellId) {
  const cc = { type: "ephemeral" };
  const txt = (text, cacheControl) => ({
    type: "input_text",
    text,
    ...(cacheControl ? { cache_control: cc } : {}),
  });

  const input = [
    { role: "system", content: [txt(salt(cellId), true)] },
    { role: "system", content: [txt(SEG, true)] },
    { role: "user", content: [txt(`${SEG}\n\nDocument A. Reply ${ACK}`)] },
    { role: "assistant", content: [{ type: "output_text", text: ACK }] },
    { role: "user", content: [txt(`${SEG}\n\nDocument B. Reply ${ACK}`)] },
    { role: "assistant", content: [{ type: "output_text", text: ACK }] },
  ];

  if (kase.tail === "user") {
    input.push({ role: "user", content: [txt(REMINDER, true), txt(QUESTION)] });
  } else {
    input.push({ role: "system", content: [txt(REMINDER, true)] });
    input.push({ role: "user", content: [txt(QUESTION)] });
  }

  return { model: BIFROST_MODEL, max_output_tokens: 32, temperature: 0, input };
}

// Bedrock Converse wire shape (the direct SigV4 leg). Every cachePoint is placed by hand, in the
// position Bifrost's own converter uses elsewhere: a cachePoint element FOLLOWS the block it
// anchors, because per Converse semantics the element terminates the cacheable prefix rather
// than opening one (same ordering as responses.go:3305-3309 for tool results).
function bedrockDirectBody(kase, cellId) {
  const cp = { cachePoint: { type: "default" } };
  const messages = [
    { role: "user", content: [{ text: `${SEG}\n\nDocument A. Reply ${ACK}` }] },
    { role: "assistant", content: [{ text: ACK }] },
    { role: "user", content: [{ text: `${SEG}\n\nDocument B. Reply ${ACK}` }] },
    { role: "assistant", content: [{ text: ACK }] },
  ];

  if (kase.tail === "user") {
    // Passthrough shape: Bifrost forwards a user block's cache_control untouched.
    messages.push({ role: "user", content: [{ text: REMINDER }, cp, { text: QUESTION }] });
  } else {
    // The hand-inlined equivalent of the midconv arm, and the whole point of this leg: reminder
    // rendered as a user turn inside the <system-reminder> envelope, its cachePoint intact, then
    // the trailing question folded into the same message - which is what Bifrost's own
    // consecutive-same-role merge (responses.go:3796-3810) produces for this input.
    messages.push({
      role: "user",
      content: [{ text: wrapReminder(REMINDER) }, cp, { text: QUESTION }],
    });
  }

  return {
    system: [{ text: salt(cellId) }, cp, { text: SEG }, cp],
    messages,
    inferenceConfig: { maxTokens: 32, temperature: 0 },
  };
}

// ---------------------------------------------------------------------------------------------
// Per-leg usage extraction. The three wire shapes disagree about what `input tokens` means, and
// getting this wrong is precisely how the defect stayed hidden in production dashboards:
//   Anthropic/Bedrock: input_tokens EXCLUDES cached reads and writes (they're separate counters).
//   OpenAI Responses:  input_tokens INCLUDES cached reads (cached_tokens is a subset of it).
// Both are normalised here to {read, write, uncached} so the hit rate means the same thing in
// every cell of the table.
// ---------------------------------------------------------------------------------------------
const EXTRACT = {
  direct: `
var j = pm.response.json();
var u = j.usage || {};
var read = u.cacheReadInputTokens || 0;
var write = u.cacheWriteInputTokens || 0;
var uncached = u.inputTokens || 0;`.trim(),

  bifrost_messages: `
var j = pm.response.json();
var u = j.usage || {};
var read = u.cache_read_input_tokens || 0;
var write = u.cache_creation_input_tokens || 0;
var uncached = u.input_tokens || 0;`.trim(),

  bifrost_responses: `
var j = pm.response.json();
var u = j.usage || {};
var d = u.input_tokens_details || {};
var read = d.cached_tokens || 0;
var write = d.cached_write_tokens || 0;
// Responses reports a TOTAL input_tokens with cached reads folded in; subtract them back out so
// "uncached" means the same thing it does on the other two legs. Writes are billed separately
// and are already excluded from the total, so they are not subtracted.
var uncached = Math.max(0, (u.input_tokens || 0) - read);`.trim(),
};

const HIT_RATE = `
var billed = read + write + uncached;
var hitRate = billed > 0 ? read / billed : 0;
var pct = (hitRate * 100).toFixed(1) + '%';
var detail = 'read=' + read + ' write=' + write + ' uncached=' + uncached + ' hitRate=' + pct;`.trim();

// ---------------------------------------------------------------------------------------------
// Item construction.
// ---------------------------------------------------------------------------------------------
function legRequest(leg, body) {
  const raw = JSON.stringify(body, null, 2);
  const common = { method: "POST", body: { mode: "raw", raw } };

  if (leg === "direct") {
    return {
      ...common,
      header: [{ key: "Content-Type", value: "application/json" }],
      auth: {
        type: "awsv4",
        awsv4: [
          { key: "accessKey", value: "{{bedrockDirectAccessKeyId}}", type: "string" },
          { key: "secretKey", value: "{{bedrockDirectSecretAccessKey}}", type: "string" },
          { key: "region", value: "{{bedrockDirectRegion}}", type: "string" },
          { key: "service", value: "bedrock", type: "string" },
        ],
      },
      url: {
        raw: `https://bedrock-runtime.{{bedrockDirectRegion}}.amazonaws.com/model/${BEDROCK_MODEL}/converse`,
        protocol: "https",
        host: ["bedrock-runtime", "{{bedrockDirectRegion}}", "amazonaws", "com"],
        path: ["model", BEDROCK_MODEL, "converse"],
      },
    };
  }

  const path = leg === "bifrost_messages" ? ["anthropic", "v1", "messages"] : ["openai", "v1", "responses"];
  return {
    ...common,
    header: [{ key: "Content-Type", value: "application/json" }],
    url: { raw: `{{baseUrl}}/${path.join("/")}`, host: ["{{baseUrl}}"], path },
  };
}

function buildBody(leg, kase, cellId) {
  if (leg === "direct") return bedrockDirectBody(kase, cellId);
  if (leg === "bifrost_responses") return responsesBody(kase, cellId);
  return anthropicBody(kase, cellId);
}

// Round 1 is a cache WRITE against a cold, per-cell-salted prefix, so it has nothing to read.
// It is recorded (the write-token count is what proves the breakpoints were accepted at all)
// but never asserted on beyond "the request worked".
function writeRoundScript(kase, leg, cellId) {
  return `
${EXTRACT[leg]}
${HIT_RATE}
pm.test(${J(`Cache anchor [${cellId}] round 1 (write) succeeds`)}, function () {
  pm.expect(pm.response.code, 'request failed: ' + pm.response.text()).to.be.below(400);
});
if (pm.response.code < 400) {
  pm.collectionVariables.set(${J(`ca_${cellId}_write`)}, JSON.stringify({ read: read, write: write, uncached: uncached }));
  console.log('[cache-anchor] ' + ${J(cellId)} + ' round1(write) ' + detail);
}`.trim();
}

function readRoundScript(kase, leg, cellId) {
  const cellLabel = `${kase.key} / ${LEG_LABEL[leg]}`;
  // The failure message has to carry the whole argument, because whoever reads it is looking at
  // one red line in a newman log, not at this file.
  const failMsg =
    kase.key === "midconv" || kase.key === "midconv_developer" || kase.key === "midconv_last"
      ? `' - the third cache_control rode a mid-conversation role:${kase.tail.startsWith("developer") ? "developer" : "system"} turn. ` +
        `Compare the control cell (same content, breakpoint on a role:user block) and the direct leg (same content, cachePoint placed by hand): ` +
        `if those read the whole prefix and this one reads only the system floor, the breakpoint was dropped in ` +
        `convertBifrostSystemReminderToBedrockUserMessage (core/providers/bedrock/responses.go), which builds Text blocks and never reads block.CacheControl.'`
      : `' - identical bytes were sent in round 1, so the warm prefix should have been read back.'`;

  return `
${EXTRACT[leg]}
${HIT_RATE}
pm.test(${J(`Cache anchor [${cellLabel}] round 2 (read) succeeds`)}, function () {
  pm.expect(pm.response.code, 'request failed: ' + pm.response.text()).to.be.below(400);
});
if (pm.response.code < 400) {
  console.log('CACHE_ANCHOR_REPORT', JSON.stringify({
    cell: ${J(cellId)},
    caseKey: ${J(kase.key)},
    caseLabel: ${J(kase.label)},
    leg: ${J(leg)},
    legLabel: ${J(LEG_LABEL[leg])},
    model: ${J(leg === "direct" ? BEDROCK_MODEL : BIFROST_MODEL)},
    read: read,
    write: write,
    uncached: uncached,
    hitRate: hitRate,
    // Carried so render-cache-parity-report.mjs can reproduce the verdict this cell actually
    // asserts below, instead of assuming every cell wants a high hit rate - midconv_contentstr
    // deliberately expects only the system floor, and a report that flagged it red would be
    // reporting correct behaviour as a defect.
    expectHit: ${kase.expectHit ? "true" : "false"},
    hitRateFloor: ${HIT_RATE_FLOOR}
  }));
${
  kase.expectHit
    ? `  pm.test(${J(`Cache anchor [${cellLabel}] reads the whole warm prefix`)}, function () {
    pm.expect(hitRate, detail + ${failMsg}).to.be.at.least(${HIT_RATE_FLOOR});
  });`
    : `  // Reported only - see the CASES entry for why a high hit rate is NOT the expected answer here.
  pm.test(${J(`Cache anchor [${cellLabel}] still caches the system floor`)}, function () {
    pm.expect(read, detail + ' - the two system breakpoints alone should still produce a read').to.be.above(0);
  });`
}
}`.trim();
}

// A cache write is visible to the very next request on Anthropic-family models, but the harness
// runs cells back-to-back with no other latency between them; a short settle keeps a round-2
// miss from being blamed on the converter when it was really propagation. Same busy-wait shape
// the collection's existing Round 31 caching rows use.
const SETTLE = `var __t = Date.now(); while (Date.now() - __t < 1200) { /* settle: let the round-1 cache write land */ }`;

export function buildMidConvSystemCacheParityItems() {
  const items = [];

  for (const kase of CASES) {
    for (const leg of kase.legs) {
      const cellId = `${kase.key}__${leg}`;
      const body = buildBody(leg, kase, cellId);
      const request = legRequest(leg, body);

      items.push({
        name: `Cache anchor: ${kase.key} / ${leg} round 1 (write)`,
        event: [
          {
            listen: "test",
            script: { type: "text/javascript", exec: writeRoundScript(kase, leg, cellId).split("\n") },
          },
        ],
        request,
      });

      items.push({
        name: `Cache anchor: ${kase.key} / ${leg} round 2 (read)`,
        event: [
          { listen: "prerequest", script: { type: "text/javascript", exec: [SETTLE] } },
          {
            listen: "test",
            script: { type: "text/javascript", exec: readRoundScript(kase, leg, cellId).split("\n") },
          },
        ],
        // Byte-identical to round 1 - the point is that nothing about the request changed, so any
        // difference in what got cached is attributable to the gateway, not to the payload.
        request: legRequest(leg, body),
      });
    }
  }

  return items;
}

export function buildMidConvSystemCacheParityFolder() {
  const items = buildMidConvSystemCacheParityItems();
  return {
    name: "Cross-Cut Round 34: Mid-Conversation System Cache-Anchor Parity (generated)",
    description:
      "Generated at harness runtime. Multi-turn (~24K prompt: ~12K system, ~12K conversation body) Claude-Code-shaped conversations carrying three cache_control breakpoints - two in `system`, one on the tail - run twice each (round 1 warms, round 2 measures). " +
      "The only variable between the control and midconv arms is whether the third breakpoint rides a role:\"user\" block or a mid-conversation role:\"system\" turn. " +
      "Each arm runs three legs: direct Bedrock Converse over SigV4 with all three cachePoint elements placed by hand (ground truth for what Bifrost should emit), Bifrost /anthropic/v1/messages, and Bifrost /openai/v1/responses. " +
      `Asserts read / (read + write + uncached) >= ${HIT_RATE_FLOOR} - deliberately NOT read / prompt_tokens, which counts a cache write as a miss and is the metric artifact that masked this defect in production dashboards. ` +
      `Model: bedrock/${BEDROCK_MODEL} (the Bedrock inlining branch is gated on IsAnthropicModelFamily, not on Opus 4.8+, so the cheapest Claude reproduces it). ` +
      "Plus four single-leg guard rows: role:developer, reminder-placed-last, a ContentStr-form system message that must NOT grow a breakpoint, and two cache_control-bearing reminders against Bedrock's 4-checkpoint cap. " +
      "Per-cell results are emitted as CACHE_ANCHOR_REPORT console lines. Credentials reuse {{bedrockDirectAccessKeyId}}/{{bedrockDirectSecretAccessKey}}/{{bedrockDirectRegion}} (AWS_* via Infisical, same as the Round 33 token-parity matrix).",
    item: items,
  };
}
