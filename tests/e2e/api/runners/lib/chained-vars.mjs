// Chained-request dependency graph for the provider harness.
//
// Several flows in this collection pass state between requests through collection variables:
// one request's TEST script computes a value and pm.collectionVariables.set()s it, and a LATER
// request drops it in as a bare {{var}} - either in its raw body (the token parity matrix's
// _r2body/_r3body rounds, the reasoning-replay pairs, the Vertex batch upload -> create pair)
// or in its URL (the Responses lifecycle rows, whose create captures a resp_ id that the
// retrieve/stream-retrieve/delete rows carry in the path; those are GET/DELETE with no body
// at all, so a body-only scan sees no dependency and leaves the chain unguarded and splittable).
//
// Postman has no notion of that link, and an unset variable is not an error. In a body the
// literal "{{var}}" stays put and the provider answers 400 "Invalid JSON" in ~1ms; in a URL it
// is percent-encoded into a valid-looking path and the provider rejects the id itself. Either
// failure names the CONSUMER, not the producer that actually broke, so one upstream 404 shows up
// as three parity failures. Two callers use this module to make the link explicit:
//
//   augment-provider-harness.mjs - injects a pre-request guard on each consumer, so a broken
//     chain reports "skipped - prerequisite did not set X" once instead of a misleading
//     Invalid JSON per round.
//   filter-collection.mjs - pulls a consumer's producers into any filtered run. A round 2
//     selected by --rerun-failed (or --provider/--feature slicing) without its round 1 in the
//     SAME newman process can never resolve its body: collection variables do not survive
//     across newman invocations, so it is guaranteed to fail for a reason unrelated to the
//     defect being rerun.

const SETTER_RE = /collectionVariables\.set\(\s*['"]([^'"]+)['"]/g;
const TEMPLATE_RE = /\{\{([A-Za-z0-9_-]+)\}\}/g;

// Depth-first walk preserving collection order, so callers can rely on "producers appear before
// consumers" - the whole scheme only works because the harness authors ordered them that way.
export function walkRequests(items, ancestors = [], out = []) {
  for (const item of items || []) {
    if (Array.isArray(item.item)) {
      walkRequests(item.item, [...ancestors, item.name || ""], out);
    } else if (item.request) {
      out.push({ item, ancestors });
    }
  }
  return out;
}

const scriptsOf = (item, listen) =>
  (item.event || [])
    .filter((e) => (listen ? e.listen === listen : true))
    .map((e) => (e.script?.exec || []).join("\n"));

export function rawBodyOf(item) {
  const raw = item.request?.body?.raw;
  return typeof raw === "string" ? raw : "";
}

// Every string on a request that Postman resolves {{var}} templates in, as one array to scan.
// A URL is either a plain string or an object, and the object form carries a pre-joined `raw`
// alongside exploded host/path/query. Neither half is load-bearing: hand-written rows in the
// collection tend to have both, programmatically built ones may have only the exploded parts,
// and the two can disagree. Scanning all of them and de-duplicating by variable name (the
// caller keys a Map) is cheaper than deciding which one to trust.
export function templateSourcesOf(item) {
  const url = item.request?.url;
  if (typeof url === "string") return [rawBodyOf(item), url];
  const query = (url?.query || []).flatMap((q) => [q?.key, q?.value]);
  return [rawBodyOf(item), url?.raw, ...(url?.host || []), ...(url?.path || []), ...query].filter(
    (s) => typeof s === "string"
  );
}

// Variables this item's scripts write. Used both to build the producer index and to exclude
// self-references: a request that sets and reads the same variable is not chained to anything.
export function varsSetBy(item) {
  const out = new Set();
  for (const src of scriptsOf(item)) {
    for (const m of src.matchAll(SETTER_RE)) out.add(m[1]);
  }
  return out;
}

// var -> the FIRST request that sets it (collection order). First rather than last because the
// earliest producer is the one guaranteed to have run before every consumer.
//
// The value is the request object, not its name. Names are not unique in the harness collection
// - most are just "<provider>/<model>", and hundreds repeat - so a name round-trip resolves to
// whichever request claimed that name first, which need not be the one that sets the variable.
// Holding the object means the identity survives no matter how the collection is named.
export function buildProducerIndex(entries) {
  const index = new Map();
  for (const { item } of entries) {
    for (const name of varsSetBy(item)) {
      if (!index.has(name)) index.set(name, item);
    }
  }
  return index;
}

// Variables this item drops into its body or URL that are produced by ANOTHER request's script.
// Returns [{ variable, producer, producerItem }] sorted by variable name for stable output,
// where `producer` is the producing request's name (for logs and guard messages) and
// `producerItem` is the request itself. Callers that need to act on the producer - pulling it
// into a filtered set, say - must use producerItem: looking the name back up is what a
// duplicated name breaks.
//
// The self-check compares object identity rather than names for the same reason: a different
// request that merely shares this one's name is a real dependency, not self-reference.
export function chainedDependencies(item, producerIndex) {
  const own = varsSetBy(item);
  const deps = new Map();
  for (const source of templateSourcesOf(item)) {
    for (const m of source.matchAll(TEMPLATE_RE)) {
      const name = m[1];
      if (own.has(name)) continue;
      const producer = producerIndex.get(name);
      if (producer && producer !== item) deps.set(name, producer);
    }
  }
  return [...deps.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([variable, producer]) => ({ variable, producer: producer.name, producerItem: producer }));
}

// Marker on the injected pre-request script, so re-running the augment step over a collection
// that already carries guards replaces them instead of stacking a second copy.
const GUARD_MARKER = "// [chained-var-guard]";

// The guard itself. It cannot live in the body or URL (both are dumb string substitutions with
// no failure mode), so it runs as a pre-request script: if any variable the request needs is
// still missing or still template-shaped, record ONE clearly-named failed assertion naming the
// producer, then skip the request instead of shipping "{{var}}" to a provider as if it were a
// real value. Asserting rather than silently skipping is deliberate - a chain that never ran
// must not let the suite go green.
function guardScript(deps, itemName) {
  const vars = JSON.stringify(deps.map((d) => d.variable));
  const producers = JSON.stringify(deps.map((d) => `${d.variable} <- "${d.producer}"`).join("; "));
  const label = JSON.stringify(`${itemName}: skipped - prerequisite request did not set its chained variable`);
  return [
    GUARD_MARKER,
    `var __cbgMissing = ${vars}.filter(function (name) {`,
    "  var v = pm.collectionVariables.get(name);",
    // A leading "{{" means a prior pass wrote the template back, or the value is itself
    // unresolved - either way the request cannot be well formed.
    '  return v === undefined || v === null || v === "" || /^\\s*\\{\\{/.test(String(v));',
    "});",
    "if (__cbgMissing.length) {",
    `  pm.test(${label}, function () {`,
    `    pm.expect(__cbgMissing, "unresolved chained variable(s); produced by: " + ${producers}).to.eql([]);`,
    "  });",
    // Feature-detected the same way the model-catalog collection does it - older sandboxes
    // have no pm.execution, and there the request still goes out; the assertion above is what
    // makes the cause legible either way.
    '  if (pm.execution && typeof pm.execution.skipRequest === "function") pm.execution.skipRequest();',
    "}",
  ];
}

// Injects a guard on every request whose body or URL consumes another request's script-set
// variable. Returns the number of requests guarded.
export function injectChainedVarGuards(collection) {
  const entries = walkRequests(collection.item);
  const producerIndex = buildProducerIndex(entries);
  let guarded = 0;

  for (const { item } of entries) {
    const deps = chainedDependencies(item, producerIndex);
    if (!deps.length) continue;

    // Prepend to any existing pre-request script rather than adding a second prerequest event:
    // the guard must run before whatever setup the item already does, and a skipped request
    // should not have run that setup at all.
    const events = (item.event || []).filter(
      (e) => !(e.listen === "prerequest" && (e.script?.exec || []).some((l) => l.includes(GUARD_MARKER)))
    );
    const existing = events.find((e) => e.listen === "prerequest");
    const lines = guardScript(deps, item.name);
    if (existing) {
      existing.script = { ...existing.script, type: existing.script?.type || "text/javascript", exec: [...lines, ...(existing.script?.exec || [])] };
      item.event = events;
    } else {
      item.event = [{ listen: "prerequest", script: { type: "text/javascript", exec: lines } }, ...events];
    }
    guarded += 1;
  }

  return guarded;
}
