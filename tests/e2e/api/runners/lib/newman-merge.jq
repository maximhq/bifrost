# Merges + slims newman JSON reports. Run with `jq -s -f newman-merge.jq report...`:
# the -s wraps the inputs in an array, so a single report goes through the same path
# as an N-provider merge.
#
# Slimming is not cosmetic. A full harness run produced a 574MB merged report, past
# V8's 0x1fffffe8 (~512MB) max string length, so every reader died in
# readFileSync(path, "utf8") with ERR_STRING_TOO_LONG before JSON.parse even ran.
# Two fields account for nearly all of it:
#
#   run.failures[].parent  - newman re-embeds the ENTIRE enclosing folder (every sibling
#                            request, body, and test script) in each failure. On vertex
#                            that was 84 failures x ~3.1MB = 262MB, versus 20MB of actual
#                            executions. Collapsed to {id, name}; the full folder is still
#                            available under the top-level `collection`.
#   response.stream        - image/audio responses arrive as Buffers, which serialize to
#                            ~4 JSON chars per byte ("255,"). Capped at 20000 elements,
#                            kept as head + tail rather than a plain prefix (see
#                            trimstream).
#
# Everything readers actually consume - executions, assertions, response codes, failure
# error messages - is preserved. Keep this in sync with nothing: it is the single source
# of truth, copied to tmp/newman-merge.jq by the Makefile and read directly by
# lib/read-report.mjs.

def failed: (((.assertions // []) | any(.error?)) or ((.response.code // 0) == 0) or ((.response.code // 0) >= 400) or (.response | not));

# A plain prefix cap silently drops the END of an SSE stream, which is exactly where the
# terminal frames live: OpenAI's usage chunk (reasoning_tokens), Anthropic's message_delta
# (thinking_tokens), Bedrock's metadata event, Gemini's usageMetadata (thoughtsTokenCount).
# Offline readers then see a stream that looks like it never reported usage, and a row that
# passed in newman - whose assertions run against the FULL body at request time - reads as a
# failure when re-checked against the stored report. Keep the same 20000-element budget, but
# spend it on both ends so early markers and terminal usage both survive.
def trimstream:
  if (.response.stream.type? == "Buffer" and ((.response.stream.data // []) | length) > 20000)
  then (.response.stream.data = (.response.stream.data[:12000] + .response.stream.data[-8000:])
        | .response.stream.truncated = true
        | .response.stream.truncatedMiddle = true)
  else . end;

def trimraw:
  if ((.request.body.raw? | type) == "string" and (.request.body.raw | length) > 20000)
  then (.request.body.raw = (.request.body.raw[:20000] + "...[truncated]"))
  else . end;

def sanitize: trimstream;

# Drop the duplicated folder payload and the test scripts; keep enough to identify the
# failing item and its error.
def slimfailure:
  (if .parent then (.parent = {id: .parent.id, name: .parent.name}) else . end)
  | (if .source then (.source = (.source | del(.event) | trimraw)) else . end);

# 429 retry supersession. A retried request appears twice: once as the 429 from the main pass and
# once as whatever the retry produced. Concatenating both would leave the run reporting a failure
# it already recovered from, and would inflate the totals by the retry count.
#
# Last occurrence wins, so the CALLER MUST PASS RETRY REPORTS LAST. The Makefile does that
# explicitly rather than relying on a shell glob, because `newman-report-<shard>-retry1.json`
# sorts BEFORE `newman-report-<shard>.json` ('-' is 0x2D, '.' is 0x2E) - a plain glob would let
# the stale 429 overwrite its own successful retry.
#
# Keyed on item id, never on name: request names repeat across this collection (the criss-cross
# rows are named after their model), so a name-keyed dedupe would collapse unrelated requests into
# one and silently drop most of the sweep. Executions without an id are passed through untouched
# for the same reason - dropping to a name key for them would reintroduce exactly that bug.
# Takes the LAST occurrence's content but sorts it at the FIRST occurrence's position, so a
# retried request stays where it was in the sweep instead of jumping to the end of the report.
def dedupe_retries:
  [range(0; length) as $i | .[$i] + {__i: $i}]
  | (map(select(.item.id? != null))
     | group_by(.item.id)
     | map(. as $g | ($g | max_by(.__i)) + {__i: ($g | map(.__i) | min)}))
    + map(select(.item.id? == null))
  | sort_by(.__i)
  | map(del(.__i));

. as $reports
| ([$reports[].run.executions[]? | sanitize] | dedupe_retries) as $execs
# Surviving ids that are still failing after supersession. A failure entry whose item recovered on
# retry has to go, otherwise `analyze-failures.mjs` still reports it and the run still reads red.
| ([$execs[] | select(failed) | .item.id?] | map(select(. != null)) | unique) as $failedids
|
{
  collection: ($reports[0].collection // {}),
  environment: ($reports[0].environment // {}),
  run: {
    executions: $execs,
    # Failures without a resolvable source id are kept: they cannot be matched against the
    # surviving set, and dropping them would hide real errors rather than superseded ones.
    failures: [
      $reports[].run.failures[]?
      | select((.source.id? == null) or (.source.id as $id | $failedids | index($id)))
      | slimfailure
    ],
    stats: {
      iterations: {total: 1, pending: 0, failed: 0},
      # Recomputed from the deduped executions rather than summed across reports: summing would
      # count every retried request twice and report more requests than the collection contains.
      items: {total: ($execs | length)},
      requests: {
        total: ($execs | length),
        failed: ([$execs[] | select(failed)] | length)
      }
    },
    timings: ($reports[0].run.timings // {})
  }
}
