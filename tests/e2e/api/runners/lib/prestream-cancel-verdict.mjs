// Verdict for run-stream-cancellation.mjs's pre-header stream abort trials.
//
// Split out of the runner so it can be unit tested: the runner has top-level
// side effects (arg parsing, the case loop, process.exit) and cannot be imported
// from a test.
//
// A client that drops a stream request before the upstream answers leaves no
// provider goroutine to end the request for plugins. Core skipped the terminal
// post-hooks on that path, so every such disconnect left bifrost_active_requests
// one higher for the life of the process, and the logging row in `processing`.
// The mid-stream abort cases never hit it: they only abort after reading bytes.
//
// The invariant: the gauge for the request's method is idle before the trials and
// back to idle once they settle. A non-idle baseline is rejected: a slot held by
// other traffic and released during the trials would cancel out a trial's leak. A trial whose headers arrived before the abort still
// counts toward the gauge check, but at least one trial must have aborted before
// headers or the path under test was never exercised.

export function evaluatePreStreamCancel({ method, trials, abortedBeforeHeaders, gaugeBefore, gaugeAfter }) {
  if ((gaugeBefore ?? 0) > 0) {
    return {
      verdict: "FAIL",
      detail: `bifrost_active_requests{method="${method}"} was not idle before the trials (${gaugeBefore} slot(s) held); drift cannot be attributed to them`,
    };
  }
  if (abortedBeforeHeaders === 0) {
    return {
      verdict: "FAIL",
      detail: `headers arrived before all ${trials} abort(s); no trial exercised a pre-header disconnect - lower --prestream-abort-ms`,
    };
  }
  if (gaugeAfter === undefined) {
    return {
      verdict: "FAIL",
      detail: `bifrost_active_requests{method="${method}"} missing from /metrics after ${trials} request(s); is the telemetry plugin enabled?`,
    };
  }
  const drift = gaugeAfter - (gaugeBefore ?? 0);
  if (drift > 0) {
    return {
      verdict: "FAIL",
      detail: `bifrost_active_requests{method="${method}"} rose by ${drift} after ${abortedBeforeHeaders} pre-header abort(s): terminal post-hooks never ran for the abandoned streams`,
    };
  }
  if (drift < 0) {
    return {
      verdict: "FAIL",
      detail: `bifrost_active_requests{method="${method}"} fell by ${-drift}: a slot was released twice (or other traffic was in flight)`,
    };
  }
  return {
    verdict: "PASS",
    detail: `gauge balanced after ${abortedBeforeHeaders}/${trials} pre-header abort(s)`,
  };
}
