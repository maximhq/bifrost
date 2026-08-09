// Resolves the --ci-interval flag of harness-monitor.mjs into a setInterval delay.
//
// Split out of harness-monitor.mjs so it can be unit tested: the monitor itself
// has top-level side effects (arg validation, process.exit, timers) and cannot
// be imported from a test.

export const CI_INTERVAL_DEFAULT_SECONDS = 5;
export const CI_INTERVAL_MIN_SECONDS = 5;
export const CI_INTERVAL_MAX_SECONDS = 45 * 60;

// raw is the flag value as parsed from argv, i.e. a string or undefined.
//
// Both ends are clamped because both produce the same failure: a ~1ms timer that
// floods an append-only Actions log. Makefile forwards MONITOR_INTERVAL here
// verbatim, so the value is user-supplied and neither end is hypothetical.
//
//   - Too small / non-numeric: Math.max propagates NaN rather than ignoring it,
//     so it is not a guard on its own - setInterval(fn, NaN) is ~1ms in Node.
//   - Too large: Node stores a timer delay in a 32-bit signed int and resets
//     anything over 2_147_483_647ms to 1ms with a TimeoutOverflowWarning.
//
// The upper bound is a domain cap well below that overflow point. A progress
// snapshot slower than 45 minutes is useless in a job whose own timeout-minutes
// is 90, so an interval that large is a mistake regardless of the overflow.
export function resolveCiIntervalMs(raw) {
  const parsed = parseInt(raw, 10);
  const seconds = Number.isFinite(parsed) ? parsed : CI_INTERVAL_DEFAULT_SECONDS;
  const clamped = Math.min(
    CI_INTERVAL_MAX_SECONDS,
    Math.max(CI_INTERVAL_MIN_SECONDS, seconds)
  );
  return clamped * 1000;
}
