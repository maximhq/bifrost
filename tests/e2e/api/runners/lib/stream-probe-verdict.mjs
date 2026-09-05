// Verdict for run-stream-cancellation.mjs's pool-health probe.
//
// Split out of the runner so it can be unit tested: the runner has top-level
// side effects (arg parsing, the case loop, process.exit) and cannot be imported
// from a test.

// The probe accepts three streaming content types, but only one of them is
// text. Holding vnd.amazon.eventstream and octet-stream to SSE field markers
// made them unpassable - both carry binary frames, so a perfectly healthy
// response decodes to text with no `data:` in it, and the probe recorded a
// false failure that exited the runner unsuccessfully. Binary types are held to
// "the stream completed and delivered something" instead, which still catches
// the empty-body case.
//
// bytesRead is the count of raw bytes off the wire, not text.length: a decoded
// binary frame collapses multi-byte sequences into single replacement
// characters, so text.length understates it and can reach 0 for a stream that
// really did deliver data.
export function evaluateProbeStream({ contentType = "", bytesRead = 0, text = "" }) {
  const isSSE = /text\/event-stream/i.test(contentType);
  const looksSSE = /(^|\n)\s*(data:|event:|id:|retry:|:)/i.test(text);
  const streamingType = /event-stream|vnd\.amazon\.eventstream|octet-stream/i.test(contentType);
  const ok = streamingType && bytesRead > 0 && (!isSSE || looksSSE);

  return {
    ok,
    error: ok
      ? undefined
      : `expected SSE after a cancelled stream, got content-type=${contentType || "<empty>"} body=${text.slice(0, 300)}`,
  };
}
