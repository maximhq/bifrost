// Rate-limit (429) retry policy for the provider harness.
//
// Sharding the sweep by provider x modality class puts up to ~80 newman processes against the
// same set of API keys at once, so 429s stop being an exotic failure and become an expected one.
// analyze-failures.mjs already categorises them separately ("Backoff/retry; not a harness bug"),
// which is the tell that the run should be doing the backoff itself rather than reporting it.
//
// Backoff is header-driven where the provider gives us a header, and exponential where it does
// not. Both upstreams document `retry-after` in SECONDS on a 429:
//   Anthropic: https://platform.claude.com/docs/en/api/rate-limits
//     "you will get a 429 error ... along with a `retry-after` header indicating how long to wait"
//     and the header table defines it as "The number of seconds to wait until you can retry".
//   OpenAI:    https://developers.openai.com/api/docs/guides/rate-limits
//     `Retry-After` - "The minimum number of seconds to wait before retrying a temporary
//     rate-limit error", and explicitly only that it MAY be present on 429 responses.
//
// That "may" is why the exponential fallback exists rather than being a nicety: a provider can
// legitimately 429 with no header at all, and the harness also routes through Bedrock, Vertex,
// Azure and OpenRouter, whose header behaviour is not assumed here.

// Header lookup is case-insensitive: newman preserves whatever casing the origin sent, and the
// two documented spellings already differ ("retry-after" vs "Retry-After").
export const headerValue = (execution, name) => {
  const wanted = name.toLowerCase();
  const found = (execution?.response?.header || []).find(
    (h) => String(h?.key || "").toLowerCase() === wanted
  );
  return found ? String(found.value) : null;
};

export const isRateLimited = (execution) => (execution?.response?.code ?? 0) === 429;

// Statuses the retry pass replays. All three mean "not now" rather than "this request is wrong",
// and with ~80 shards against one set of keys all three are expected outcomes rather than exotic
// ones. Each is quoted from the upstream that defines it, because the three do NOT overlap across
// providers and picking the wrong one leaves a provider uncovered:
//
//   429 - the account's quota. Both upstreams document Retry-After on it (links at the top).
//   503 - OpenAI's overload signal: "The engine is currently overloaded, please try again later"
//         and "Slow Down", with the documented remedy "Retry your request after a brief wait".
//         https://developers.openai.com/api/docs/guides/error-codes
//   529 - Anthropic's overload signal: "529 - `overloaded_error`: The API is temporarily
//         overloaded", noted as occurring "when the API experiences high traffic across all
//         users". https://platform.claude.com/docs/en/api/errors
//
// 529 is here rather than left out because Anthropic's error table has NO 503 entry at all - 529
// is its whole equivalent. A 503-only set would therefore cover OpenAI and silently miss the
// anthropic and bedrock forks, which are two of the sweep's largest. Bifrost core already groups
// the pair for the same reason: see transientServerStatusCodes in core/utils.go, which retries
// 503 and 529 on the SAME key precisely because neither is the credential's fault.
//
// Deliberately NOT 500/502/504, which core does treat as transient. The harness has the opposite
// problem to the proxy: those three are ambiguous between "upstream blipped" and "Bifrost has a
// defect", and this sweep exists to find the second kind. Replaying them would turn a
// deterministic failure into a flaky-looking pass, the same trade the "assertion failures are
// never retried" rule already refuses. 503 and 529 are the codes whose documented meaning is
// specifically "come back later", so they are the ones that get to be replayed.
export const RETRYABLE_CODES = new Set([429, 503, 529]);

// The predicate the retry pass actually runs on. isRateLimited stays 429-only because
// analyze-failures.mjs reports the two differently: a 429 says the sweep outran the account's
// quota (reduce concurrency, lift it, rerun later), a 503 says the provider was down. Handing
// someone a quota fix hint for an upstream outage sends them to the wrong place.
export const isRetryable = (execution) => RETRYABLE_CODES.has(execution?.response?.code ?? 0);

// Seconds the provider asked us to wait, or null when it did not say. Only a plain numeric value
// is honoured: Retry-After also permits an HTTP-date, but neither provider above documents that
// form here, and mis-parsing a date into a huge sleep would stall a whole shard.
export const retryAfterSeconds = (execution) => {
  const raw = headerValue(execution, "retry-after");
  if (raw === null) return null;
  // An empty or whitespace-only header is "the provider said nothing", not "wait zero seconds".
  // Number("") is 0, so without this guard the header would report a real, satisfied wait of 0
  // and suppress the exponential term that a headerless execution is supposed to fall back to.
  const trimmed = raw.trim();
  if (trimmed === "") return null;
  const n = Number(trimmed);
  return Number.isFinite(n) && n >= 0 ? n : null;
};

export const DEFAULT_POLICY = {
  baseSeconds: 5,
  // Ceiling per wait. A provider that asks for a 15-minute cooldown is telling us the run is
  // over; better to fail the shard and surface it than to hold ~80 processes open for it.
  maxSeconds: 120,
  maxAttempts: 3,
};

// How long to wait before attempt N (1-based) given the executions that just failed.
//
// Takes the MAXIMUM Retry-After across the RETRYABLE executions, not the first or the mean: the
// wait has to satisfy every request being retried, and the shard replays them together. Providers
// that sent no header contribute the exponential term instead, so a mixed shard waits long enough
// for the one that actually told us. An overloaded provider (503/529) usually sends no header, so
// it lands on the exponential term - which is the right shape for "come back later" anyway.
//
// Filters on isRetryable, NOT isRateLimited: this function's 0 is what the Makefile reads as "do
// not retry this shard", so a 429-only filter here would silently strip every 503/529 shard of its
// retry no matter what the rest of the module said about them.
export const backoffSeconds = (executions, attempt, policy = DEFAULT_POLICY) => {
  const limited = (executions || []).filter(isRetryable);
  if (limited.length === 0) return 0;

  const asked = limited.map(retryAfterSeconds).filter((s) => s !== null);
  const exponential = policy.baseSeconds * Math.pow(2, Math.max(0, attempt - 1));

  // Any execution WITHOUT a header still needs a sane wait, so the exponential term is the floor
  // whenever at least one such execution is present.
  const anyHeaderless = asked.length < limited.length;
  const candidates = [...asked, ...(anyHeaderless ? [exponential] : [])];
  const wait = candidates.length ? Math.max(...candidates) : exponential;

  // Floor of 1 second, because this number crosses a CLI boundary where 0 already means something
  // else. The Makefile retry loop keeps a shard only when the printed wait is > 0, and line 59
  // returns 0 for "nothing was rate limited". Every rate-limited execution answering
  // `Retry-After: 0` would otherwise collapse onto that same 0 and silently lose its retry.
  return Math.max(1, Math.min(Math.ceil(wait), policy.maxSeconds));
};

// Item names the retry pass should replay, for filter-collection.mjs --rerun-rate-limited. Names
// rather than ids because its existing --rerun-failed selection is name-keyed and this reuses it.
export const retryableNames = (report) => {
  const names = new Set();
  for (const e of report?.run?.executions || []) {
    if (isRetryable(e) && e.item?.name) names.add(e.item.name);
  }
  return names;
};

// The 429-only counterpart, kept separate because "we outran the quota" and "the provider was
// down" are different findings about a run even though both are replayed. analyze-failures.mjs
// reports them under different categories with different fix hints.
export const rateLimitedNames = (report) => {
  const names = new Set();
  for (const e of report?.run?.executions || []) {
    if (isRateLimited(e) && e.item?.name) names.add(e.item.name);
  }
  return names;
};

// True when a shard is worth retrying at all: it failed, and at least one of its failures was a
// retryable status. A shard that failed purely on assertions must NOT be replayed - the retry
// would burn quota re-confirming a real defect, and would make a flaky-looking pass out of a
// deterministic failure.
export const shouldRetry = (report) => {
  const execs = report?.run?.executions || [];
  return execs.some(isRetryable);
};
