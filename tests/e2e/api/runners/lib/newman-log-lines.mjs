// Line shapes newman's `cli` reporter emits, and the classifier harness-monitor
// uses to turn a tailed log back into per-request pass/fail counts.
//
// Split out of harness-monitor.mjs so the patterns can be pinned by tests: the
// monitor has top-level side effects (arg validation, process.exit, timers,
// alt-screen escapes) and cannot be imported. These regexes are the entire
// basis of the status table's numbers, so a silent mismatch here shows up as a
// table that quietly undercounts rather than as an error.

// "❏ 4. Bedrock (via /bedrock)" - a folder heading.
export const RE_FOLDER = /^❏\s+(.+?)\s*$/;

// "↳ Prompt caching (cache_control: ephemeral)" - a request starting.
export const RE_REQUEST = /^↳\s+(.+?)\s*$/;

// The line newman prints under ↳: "POST http://host/path [200 OK, 1.2kB, 340ms]".
export const RE_METHOD_URL = /^(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s+https?:\/\//i;

// The "[<status> <text>, <size>, <duration>]" summary that closes a request.
//
// The status text is one OR MORE words. It was previously `(?:\s+[A-Za-z]+)?`,
// matching at most one, which silently dropped every multi-word status newman
// prints - and the only common one is "400 Bad Request". A request whose
// summary does not match here is marked neither done nor failed, so it is
// counted as neither a pass nor a fail and vanishes from the table entirely.
// On the last full local sweep that was 1362 of 1937 completed requests,
// including 327 of openai's 375 and 106 of azure's 108 - i.e. the table read as
// near-empty for exactly the providers that were failing hardest.
export const RE_REQUEST_DONE =
  /\[\s*\d+(?:\s+[A-Za-z]+)*,\s*[\d.]+\s*[kMG]?B,\s*[\d.]+\s*m?s\s*\]/;

// A request that never got a response (connection refused, DNS, timeout) gets
// "[errored]" where the [status, size, duration] summary would be. Without this
// it matches no rule at all and the request is counted as neither pass nor fail
// - so a gateway that is down reads as a table of zeroes rather than failures.
export const RE_REQUEST_ERRORED = /\[errored\]/;

// Newman numbers assertion failures under the request: "  1. expected 400 to be
// within 200..299". Passing assertions are printed with a ✓ instead.
export const RE_ASSERT_FAIL = /^\s*\d+\.\s+(.+?)$/;

// "[openai] " - the prefix the Makefile's `sed` adds to each provider fork's
// output so one monitor can tell concurrent logs apart.
export const RE_PREFIX = /^\[([a-z_]+)\]\s?(.*)$/;

// classifyLine names what a single (ANSI-stripped, prefix-stripped) log line
// means to the counters. Order matters: a request-done summary is checked
// before the assertion-fail pattern, because "[400 Bad Request, 1.2kB, 340ms]"
// on an indented line can otherwise be read as a numbered assertion.
export function classifyLine(line) {
  const trimmed = line.trimStart();
  if (RE_FOLDER.test(trimmed)) return "folder";
  if (RE_REQUEST.test(trimmed)) return "request";
  if (RE_REQUEST_DONE.test(trimmed)) return "done";
  if (RE_REQUEST_ERRORED.test(trimmed)) return "errored";
  if (RE_METHOD_URL.test(trimmed)) return "method-url";
  if (RE_ASSERT_FAIL.test(trimmed)) return "assert-fail";
  return "other";
}
