// Verdict for run-stream-cancellation.mjs's non-streaming abort trials (#6972).
//
// Split out of the runner so it can be unit tested: the runner has top-level
// side effects (arg parsing, the case loop, process.exit) and cannot be imported
// from a test.
//
// The worker hands a finished non-streaming result (or error) to the caller
// through a cap-1 channel that is drained on acquire. When the caller has
// already gone, that send and ctx.Done() are both ready and Go picks between
// them at random, so the terminal post-hooks (billing, the log row's final
// status) ran for about half of the abandoned requests. The logging plugin
// inserts the row as `processing` at pre-hook; only the terminal hook moves it
// on. So the invariant is: every abandoned request's row exists AND carries a
// terminal status. Cost presence is not part of the invariant: once the
// transport cancels the context on a client socket close (#7106) the upstream
// call is cut with a 499 and usually has no usage.
export const TERMINAL_STATUSES = new Set(["cancelled", "error", "success"]);

export function evaluateNonStreamCancel({ row, racedToCompletion = false, aborted = false }) {
  if (racedToCompletion) {
    return {
      verdict: "FAIL",
      detail:
        "response returned before the abort fired; the trial did not exercise a disconnect - lower --nonstream-abort-ms or raise max_tokens",
    };
  }
  if (!aborted) {
    return { verdict: "SKIP", detail: "abort never fired" };
  }
  if (!row) {
    return {
      verdict: "FAIL",
      detail: "no log row: terminal post-hooks never ran for the abandoned request (#6972)",
    };
  }
  if (!TERMINAL_STATUSES.has(row.status)) {
    return {
      verdict: "FAIL",
      detail: `status=${row.status}: row never reached a terminal status, the abandoned request was dropped before billing/logging (#6972)`,
    };
  }
  return { verdict: "PASS", detail: `status=${row.status}` };
}
