// Rows tagged [EXPECT-...] assert a non-200 status on purpose: they send a
// deliberately invalid or async request so the tagged status proves the request
// actually reached the layer under test (a 200 would mean the defect went
// unnoticed). For those rows the tagged status is the success path, so HTTP
// status alone must not mark them failed - only a real assertion failure can.
// The collection-level status gate already fails them on any other status,
// which is the case that actually matters.
//
// Tags come in two shapes: a specific code ([EXPECT-400], [EXPECT-202]) and the
// class wildcard [EXPECT-4XX]. 5xx never matches: that is infrastructure, not
// the rejection the row asked for.
export const isExpectedStatus = (name, code) => {
  const m = String(name || "").match(/\[EXPECT-(\d{3}|4XX)\]/);
  if (!m) return false;
  if (m[1] === "4XX") return code >= 400 && code <= 499;
  return code === Number(m[1]);
};
