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
#                            ~4 JSON chars per byte ("255,"). Capped at 20000 elements.
#
# Everything readers actually consume - executions, assertions, response codes, failure
# error messages - is preserved. Keep this in sync with nothing: it is the single source
# of truth, copied to tmp/newman-merge.jq by the Makefile and read directly by
# lib/read-report.mjs.

def failed: (((.assertions // []) | any(.error?)) or ((.response.code // 0) == 0) or ((.response.code // 0) >= 400) or (.response | not));

def trimstream:
  if (.response.stream.type? == "Buffer" and ((.response.stream.data // []) | length) > 20000)
  then (.response.stream.data = .response.stream.data[:20000] | .response.stream.truncated = true)
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

{
  collection: (.[0].collection // {}),
  environment: (.[0].environment // {}),
  run: {
    executions: [.[].run.executions[]? | sanitize],
    failures: [.[].run.failures[]? | slimfailure],
    stats: {
      iterations: {total: 1, pending: 0, failed: 0},
      items: {total: ([.[].run.stats.items.total // 0] | add)},
      requests: {
        total: ([.[].run.stats.requests.total // 0] | add),
        failed: ([.[].run.stats.requests.failed // 0] | add)
      }
    },
    timings: (.[0].run.timings // {})
  }
}
