// Shared pieces of the semantic cache wiring collection (build-semantic-cache-wiring.mjs).
//
// Every request in that collection asserts through one sandbox library, scwLib below. The
// generator serializes it with Function.prototype.toString into a collection variable, and each
// request's test script evals it and hands it a declarative spec:
//
//   eval(pm.collectionVariables.get('scw_lib'));
//   scw.run({ name: 'D1 B: repeat is a direct hit', poll: true, status: 200, outcome: 'direct', same: 'd1' });
//
// scwLib and scwNamespaceSuffix are therefore written in ES5 with no imports and no reference to
// anything outside their own bodies, so the copy the sandbox runs is exactly the code the unit
// tests in semantic-cache-wiring.test.mjs exercise.

// The vector store namespace suffix for a run id. Weaviate class names must start with an
// uppercase letter and allow only letters, digits and underscores, so the hyphenated run id is
// reduced to its alphanumerics and every namespace is a fixed capitalized prefix plus this.
export function scwNamespaceSuffix(runId) {
  return String(runId || "").replace(/[^A-Za-z0-9]/g, "");
}

// The sandbox library. pm is the Postman sandbox object (or a fake in the unit tests).
export function scwLib(pm) {
  var MAX_ATTEMPTS = 8;
  // One response header per cache_debug field, and how its value parses.
  var HEADER_FIELDS = [
    ["x-bifrost-cache-hit", "cache_hit", "bool"],
    ["x-bifrost-cache-id", "cache_id", "string"],
    ["x-bifrost-cache-hit-type", "hit_type", "string"],
    ["x-bifrost-cache-hit-latency-ms", "cache_hit_latency", "int"],
    ["x-bifrost-cache-requested-provider", "requested_provider", "string"],
    ["x-bifrost-cache-requested-model", "requested_model", "string"],
    ["x-bifrost-cache-similarity", "similarity", "float"],
    ["x-bifrost-cache-threshold", "threshold", "float"],
    ["x-bifrost-cache-provider-used", "provider_used", "string"],
    ["x-bifrost-cache-model-used", "model_used", "string"],
    ["x-bifrost-cache-input-tokens", "input_tokens", "int"],
  ];
  var EMBEDDING_FIELDS = ["provider_used", "model_used", "input_tokens"];

  function fail(msg) {
    throw new Error(msg);
  }
  function check(cond, msg) {
    if (!cond) fail(msg);
  }
  function getVar(name) {
    var v = pm.collectionVariables.get(name);
    return v === undefined || v === null ? "" : String(v);
  }
  function setVar(name, value) {
    pm.collectionVariables.set(name, value);
  }
  function header(name) {
    var v = pm.response.headers.get(name);
    return v === undefined || v === null ? undefined : String(v);
  }
  function show(v) {
    var s = typeof v === "string" ? v : JSON.stringify(v);
    return s === undefined ? "undefined" : s.length > 600 ? s.slice(0, 600) + "..." : s;
  }
  function bodyText() {
    var t = pm.response.text();
    return t === undefined || t === null ? "" : String(t);
  }
  function parseJson(text) {
    try {
      return JSON.parse(text);
    } catch (e) {
      return undefined;
    }
  }
  function isStream() {
    return (header("content-type") || "").toLowerCase().indexOf("text/event-stream") >= 0;
  }

  // Reads an SSE body into its data events. "[DONE]" sets done and is not an event; a data line
  // that is not JSON is kept as a string so a malformed frame still counts.
  function parseSse(text) {
    var out = { events: [], types: [], done: false };
    var lines = String(text || "").split(/\r?\n/);
    var eventName = "";
    for (var i = 0; i < lines.length; i++) {
      var line = lines[i];
      if (line.indexOf("event:") === 0) {
        eventName = line.slice(6).trim();
        continue;
      }
      if (line.indexOf("data:") !== 0) continue;
      var payload = line.slice(5).trim();
      if (payload === "[DONE]") {
        out.done = true;
        continue;
      }
      var parsed = parseJson(payload);
      out.events.push(parsed === undefined ? payload : parsed);
      out.types.push(eventName || (parsed && parsed.type) || "");
      eventName = "";
    }
    return out;
  }

  function debugOf(obj) {
    return obj && typeof obj === "object" && obj.extra_fields && obj.extra_fields.cache_debug ? obj.extra_fields.cache_debug : undefined;
  }

  // The text a stream event contributes, so a replay can be compared chunk by chunk: chat and
  // text completion deltas, tool call fragments, and the Responses API's delta events.
  function eventText(e) {
    if (!e || typeof e !== "object") return String(e);
    var parts = [];
    var c = e.choices && e.choices[0];
    if (c) {
      if (c.delta) {
        if (typeof c.delta.content === "string") parts.push(c.delta.content);
        var tcs = c.delta.tool_calls || [];
        for (var i = 0; i < tcs.length; i++) {
          var fn = tcs[i] && tcs[i]["function"];
          if (fn) parts.push((fn.name || "") + (fn.arguments || ""));
        }
      }
      if (typeof c.text === "string") parts.push(c.text);
    }
    if (typeof e.delta === "string") parts.push(e.delta);
    return parts.join("");
  }

  // The fields of a stream event a replay must keep besides its text: how the choice ends, every
  // tool call fragment with its index, id and type, and usage. extra_fields is left out: latency
  // and cache_debug legitimately differ on a replay.
  function eventShape(e) {
    if (!e || typeof e !== "object") return "";
    var shape = {};
    var c = e.choices && e.choices[0];
    if (c && c.finish_reason) shape.finish_reason = c.finish_reason;
    var tcs = (c && c.delta && c.delta.tool_calls) || [];
    if (tcs.length) {
      shape.tool_calls = [];
      for (var i = 0; i < tcs.length; i++) {
        var tc = tcs[i] || {};
        var fn = tc["function"] || {};
        shape.tool_calls.push([tc.index, tc.id || "", tc.type || "", fn.name || "", fn.arguments || ""]);
      }
    }
    var usage = e.usage || (e.response && e.response.usage);
    if (usage) shape.usage = usage;
    return JSON.stringify(shape);
  }

  function streamSummary(sse) {
    var texts = [];
    var shapes = [];
    for (var i = 0; i < sse.events.length; i++) {
      texts.push(eventText(sse.events[i]));
      shapes.push(eventShape(sse.events[i]));
    }
    return { count: sse.events.length, types: sse.types, texts: texts, shapes: shapes };
  }

  // cache_debug as the x-bifrost-cache-* headers carry it, or undefined when none is sent.
  function debugFromHeaders() {
    if (header("x-bifrost-cache-hit") === undefined) return undefined;
    var out = {};
    for (var i = 0; i < HEADER_FIELDS.length; i++) {
      var raw = header(HEADER_FIELDS[i][0]);
      if (raw === undefined) continue;
      var kind = HEADER_FIELDS[i][2];
      out[HEADER_FIELDS[i][1]] = kind === "bool" ? raw === "true" : kind === "int" ? parseInt(raw, 10) : kind === "float" ? parseFloat(raw) : raw;
    }
    return out;
  }

  function anyCacheHeader() {
    for (var i = 0; i < HEADER_FIELDS.length; i++) if (header(HEADER_FIELDS[i][0]) !== undefined) return HEADER_FIELDS[i][0];
    return undefined;
  }

  // Compares the headers with the body's cache_debug field by field. skipLatency leaves out the
  // hit latency, which a stream measures when its headers are written, before the last chunk.
  function compareHeaders(cd, skipLatency) {
    var hd = debugFromHeaders() || {};
    for (var i = 0; i < HEADER_FIELDS.length; i++) {
      var field = HEADER_FIELDS[i][1];
      if (skipLatency && field === "cache_hit_latency") continue;
      var a = cd[field];
      var b = hd[field];
      if (a === undefined && b === undefined) continue;
      check(a !== undefined, "header " + HEADER_FIELDS[i][0] + " = " + show(b) + " but cache_debug has no " + field);
      check(b !== undefined, "cache_debug." + field + " = " + show(a) + " but header " + HEADER_FIELDS[i][0] + " is missing");
      if (HEADER_FIELDS[i][2] === "float") {
        check(Math.abs(Number(a) - Number(b)) < 1e-9, "header " + HEADER_FIELDS[i][0] + " = " + b + " but cache_debug." + field + " = " + a);
      } else {
        check(a === b, "header " + HEADER_FIELDS[i][0] + " = " + show(b) + " but cache_debug." + field + " = " + show(a));
      }
    }
  }

  // Walks a dotted path ("choices.0.message.content"); "" is the value itself.
  function pathGet(obj, path) {
    if (!path) return obj;
    var parts = String(path).split(".");
    var cur = obj;
    for (var i = 0; i < parts.length; i++) {
      if (cur === undefined || cur === null) return undefined;
      cur = cur[parts[i]];
    }
    return cur;
  }

  // 32-bit FNV-1a over a string, so large payloads (images, audio, vectors) compare by value
  // without keeping them in collection variables.
  function fnv(str) {
    var h = 0x811c9dc5;
    for (var i = 0; i < str.length; i++) {
      h ^= str.charCodeAt(i);
      h = (h + ((h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24))) >>> 0;
    }
    return ("00000000" + h.toString(16)).slice(-8);
  }
  function fingerprint(value) {
    var s = typeof value === "string" ? value : JSON.stringify(value);
    if (s === undefined) s = "undefined";
    return s.length + ":" + fnv(s);
  }

  function deepEqual(a, b) {
    return JSON.stringify(a) === JSON.stringify(b);
  }

  function statusOk(expected, code) {
    if (expected === undefined) return true;
    if (Object.prototype.toString.call(expected) === "[object Array]") return expected.indexOf(code) >= 0;
    if (expected === "2xx") return code >= 200 && code < 300;
    if (expected === "4xx") return code >= 400 && code < 500;
    return code === expected;
  }

  function errorMessage(json, text) {
    if (json && json.error && typeof json.error.message === "string") return json.error.message;
    if (json && typeof json.message === "string") return json.message;
    return text;
  }

  function checkOutcome(cd, spec) {
    var outcome = spec.outcome;
    if (outcome === "none") {
      check(cd === undefined, "expected no cache_debug, got " + show(cd));
      var stray = anyCacheHeader();
      check(stray === undefined, "expected no x-bifrost-cache-* headers, got " + stray + ": " + header(stray));
      return;
    }
    check(cd !== undefined, "expected cache_debug (" + outcome + "), got none");
    check(typeof cd.cache_id === "string" && cd.cache_id !== "", "cache_debug.cache_id missing: " + show(cd));
    if (outcome === "miss") {
      check(cd.cache_hit === false, "expected a miss, got " + show(cd));
      check(cd.hit_type === undefined, "a miss must carry no hit_type: " + show(cd));
      check(cd.cache_hit_latency === undefined, "a miss must carry no cache_hit_latency: " + show(cd));
      check(cd.similarity === undefined && cd.threshold === undefined, "a miss must carry no similarity or threshold: " + show(cd));
    } else {
      check(cd.cache_hit === true, "expected a " + outcome + " hit, got " + show(cd));
      check(cd.hit_type === outcome, "expected hit_type " + outcome + ", got " + show(cd));
      check(typeof cd.cache_hit_latency === "number" && cd.cache_hit_latency >= 0, "a hit must carry cache_hit_latency: " + show(cd));
      if (outcome === "direct") {
        check(cd.similarity === undefined && cd.threshold === undefined, "a direct hit must carry no similarity or threshold: " + show(cd));
      } else {
        check(typeof cd.similarity === "number" && typeof cd.threshold === "number", "a semantic hit must carry similarity and threshold: " + show(cd));
        check(cd.similarity >= cd.threshold - 1e-9, "similarity " + cd.similarity + " is below threshold " + cd.threshold);
      }
    }
    var embedding = spec.embedding;
    if (embedding === undefined && outcome === "direct") embedding = false;
    if (embedding === undefined && outcome === "semantic") embedding = true;
    if (embedding === true) {
      check(typeof cd.provider_used === "string" && cd.provider_used !== "", "expected provider_used: " + show(cd));
      check(typeof cd.model_used === "string" && cd.model_used !== "", "expected model_used: " + show(cd));
      check(typeof cd.input_tokens === "number" && cd.input_tokens > 0, "expected input_tokens > 0: " + show(cd));
    } else if (embedding === false) {
      for (var i = 0; i < EMBEDDING_FIELDS.length; i++) {
        check(cd[EMBEDDING_FIELDS[i]] === undefined, "expected no " + EMBEDDING_FIELDS[i] + ": " + show(cd));
      }
    }
    if (spec.providerUsed !== undefined) check(cd.provider_used === spec.providerUsed, "expected provider_used " + spec.providerUsed + ": " + show(cd));
    if (spec.modelUsed !== undefined) check(cd.model_used === spec.modelUsed, "expected model_used " + spec.modelUsed + ": " + show(cd));
    if (spec.threshold !== undefined) check(Math.abs(cd.threshold - spec.threshold) < 1e-9, "expected threshold " + spec.threshold + ": " + show(cd));
    if (spec.same) {
      var want = getVar("cid_" + spec.same);
      check(want !== "", "no cache_id was saved as " + spec.same);
      check(cd.cache_id === want, "expected cache_id " + want + " (" + spec.same + "), got " + cd.cache_id);
    }
    var distinct = spec.distinct || [];
    for (var j = 0; j < distinct.length; j++) {
      var other = getVar("cid_" + distinct[j]);
      check(other !== "", "no cache_id was saved as " + distinct[j]);
      check(cd.cache_id !== other, "cache_id " + cd.cache_id + " should differ from " + distinct[j]);
    }
  }

  function checkField(json, f) {
    var v = pathGet(json, f.path);
    var where = f.path || "body";
    if (f.eq !== undefined) check(deepEqual(v, f.eq), where + " = " + show(v) + ", expected " + show(f.eq));
    if (f.equals !== undefined) check(JSON.stringify(v) === getVar("f_" + f.equals), where + " = " + show(v) + ", expected the saved " + f.equals + " " + show(getVar("f_" + f.equals)));
    if (f.equalsVar !== undefined) check(v === getVar(f.equalsVar), where + " = " + show(v) + ", expected " + f.equalsVar + " " + show(getVar(f.equalsVar)));
    if (f.includes !== undefined) check(typeof v === "string" && v.indexOf(f.includes) >= 0, where + " = " + show(v) + " does not include " + show(f.includes));
    if (f.oneOf !== undefined) check(f.oneOf.indexOf(v) >= 0, where + " = " + show(v) + ", expected one of " + show(f.oneOf));
    if (f.absentOrFalse) check(v === undefined || v === null || v === false, where + " = " + show(v) + ", expected absent or false");
    if (f.contains !== undefined) check(Object.prototype.toString.call(v) === "[object Array]" && v.indexOf(f.contains) >= 0, where + " = " + show(v) + " does not contain " + show(f.contains));
    if (f.containsName !== undefined) {
      var named = false;
      for (var i = 0; v && i < v.length; i++) if (v[i] && v[i].name === f.containsName) named = true;
      check(named, where + " has no entry named " + f.containsName + ": " + show(v));
    }
    if (f.gt !== undefined) check(typeof v === "number" && v > f.gt, where + " = " + show(v) + ", expected > " + f.gt);
    if (f.atLeastSavedPlus !== undefined) {
      var base = Number(getVar("f_" + f.atLeastSavedPlus[0]));
      check(typeof v === "number" && v >= base + f.atLeastSavedPlus[1], where + " = " + show(v) + ", expected >= " + base + " + " + f.atLeastSavedPlus[1]);
    }
    if (f.nonEmpty) check(v !== undefined && v !== null && v !== "" && !(v.length === 0), where + " is empty: " + show(v));
  }

  // plugin_logs on a log row is a JSON string of {plugin: [{level, message}]}.
  function checkPluginLogs(json, spec) {
    var raw = json && json.plugin_logs;
    var parsed = typeof raw === "string" ? parseJson(raw) : raw;
    var entries = (parsed && parsed[spec.plugin]) || [];
    var matching = [];
    for (var i = 0; i < entries.length; i++) {
      var e = entries[i] || {};
      if (spec.level && e.level !== spec.level) continue;
      if (spec.includes && String(e.message || "").indexOf(spec.includes) < 0) continue;
      matching.push(e);
    }
    if (spec.absent) {
      check(matching.length === 0, "expected no " + spec.plugin + " " + (spec.level || "") + " entry, got " + show(matching));
    } else {
      check(matching.length > 0, "expected a " + spec.plugin + " " + (spec.level || "") + " entry containing " + show(spec.includes) + ", plugin_logs " + show(raw));
    }
  }

  function checkIds(json, spec) {
    var rows = pathGet(json, spec.path) || [];
    var ids = [];
    for (var i = 0; i < rows.length; i++) ids.push(rows[i] && rows[i].id);
    var inc = spec.include || [];
    for (var j = 0; j < inc.length; j++) {
      var want = getVar("rid_" + inc[j]);
      check(want !== "" && ids.indexOf(want) >= 0, "expected request " + inc[j] + " (" + want + ") among " + ids.length + " rows");
    }
    var exc = spec.exclude || [];
    for (var k = 0; k < exc.length; k++) {
      var not = getVar("rid_" + exc[k]);
      check(not !== "", "no request id was saved as " + exc[k]);
      check(ids.indexOf(not) < 0, "request " + exc[k] + " (" + not + ") should not be listed");
    }
  }

  function checkStream(sse, cdIndex, spec) {
    var st = spec.stream;
    if (st.done) check(sse.done, "stream has no [DONE] terminator");
    if (st.minChunks) check(sse.events.length >= st.minChunks, "expected at least " + st.minChunks + " data chunks, got " + sse.events.length);
    if (st.finalOnly) {
      check(cdIndex === sse.events.length - 1, "cache_debug must ride only on the final data chunk; found it on chunk " + cdIndex + " of " + sse.events.length);
      for (var i = 0; i < sse.events.length - 1; i++) check(debugOf(sse.events[i]) === undefined, "chunk " + i + " carries cache_debug before the final chunk");
    }
    var summary = streamSummary(sse);
    if (st.compare) {
      var saved = parseJson(getVar("st_" + st.compare));
      check(saved, "no stream was saved as " + st.compare);
      check(summary.count === saved.count, "replay has " + summary.count + " chunks, the original had " + saved.count);
      if (st.compareTypes !== false) check(deepEqual(summary.types, saved.types), "event types differ: " + show(summary.types) + " vs " + show(saved.types));
      if (st.compareTexts !== false) check(deepEqual(summary.texts, saved.texts), "chunk contents differ: " + show(summary.texts) + " vs " + show(saved.texts));
      if (st.compareTexts !== false) check(deepEqual(summary.shapes, saved.shapes), "chunk finish_reason, tool calls or usage differ: " + show(summary.shapes) + " vs " + show(saved.shapes));
    }
    if (st.sameCount) {
      var other = parseJson(getVar("st_" + st.sameCount));
      check(other, "no stream was saved as " + st.sameCount);
      check(summary.count === other.count, "stream has " + summary.count + " chunks, " + st.sameCount + " had " + other.count);
    }
    return summary;
  }

  // Replaces {{var}} references in every string of a spec, so expectations can name run-scoped
  // values such as provider names the same way the request does.
  function resolveSpec(v) {
    if (typeof v === "string") return v.indexOf("{{") >= 0 ? pm.variables.replaceIn(v) : v;
    if (Object.prototype.toString.call(v) === "[object Array]") {
      var arr = [];
      for (var i = 0; i < v.length; i++) arr.push(resolveSpec(v[i]));
      return arr;
    }
    if (v && typeof v === "object") {
      var out = {};
      for (var k in v) if (Object.prototype.hasOwnProperty.call(v, k)) out[k] = resolveSpec(v[k]);
      return out;
    }
    return v;
  }

  // Runs every assertion in spec against the current response and returns what saves need.
  function evaluate(spec) {
    var code = pm.response.code;
    var text = bodyText();
    var json = parseJson(text);
    check(statusOk(spec.status, code), "status " + code + ", expected " + show(spec.status) + "; body " + show(text));
    if (spec.contentType !== undefined) {
      check((header("content-type") || "").indexOf(spec.contentType) >= 0, "content-type " + header("content-type") + ", expected " + spec.contentType);
    }
    if (spec.message !== undefined) {
      var msg = errorMessage(json, text);
      check(String(msg).indexOf(spec.message) >= 0, "message " + show(msg) + " does not include " + show(spec.message));
    }
    var source = spec.source || "auto";
    var sse = null;
    var cd;
    var cdIndex = -1;
    if (source === "headers") {
      cd = debugFromHeaders();
    } else if (source === "log") {
      cd = json && json.cache_debug ? json.cache_debug : undefined;
    } else if (isStream()) {
      sse = parseSse(text);
      for (var i = sse.events.length - 1; i >= 0; i--) {
        if (debugOf(sse.events[i])) {
          cd = debugOf(sse.events[i]);
          cdIndex = i;
          break;
        }
      }
    } else {
      cd = debugOf(json);
    }
    if (spec.noBodyCacheDebug) {
      check(text.indexOf("cache_debug") < 0, "the body must carry no cache_debug on this route: " + show(text));
    }
    if (spec.outcome) checkOutcome(cd, spec);
    if (spec.noHeaders) {
      var stray = anyCacheHeader();
      check(stray === undefined, "expected no x-bifrost-cache-* headers, got " + stray);
    }
    if (cd && source === "auto" && spec.outcome && spec.outcome !== "none") {
      // Every inference route that ran a lookup sends the headers; they must agree with the body.
      if (spec.headersMatch === "all") compareHeaders(cd, false);
      else if (spec.headersMatch === "stream") compareHeaders(cd, true);
      else {
        check(header("x-bifrost-cache-hit") === String(cd.cache_hit), "x-bifrost-cache-hit " + header("x-bifrost-cache-hit") + " disagrees with cache_debug " + show(cd));
        check(header("x-bifrost-cache-id") === cd.cache_id, "x-bifrost-cache-id " + header("x-bifrost-cache-id") + " disagrees with cache_debug " + show(cd));
      }
    }
    if (spec.cdEquals) {
      var savedCd = parseJson(getVar("cd_" + spec.cdEquals));
      check(savedCd, "no cache_debug was saved as " + spec.cdEquals);
      check(deepEqual(sortKeys(cd), sortKeys(savedCd)), "log cache_debug " + show(cd) + " differs from the response's " + show(savedCd));
    }
    var summary = null;
    if (spec.stream) {
      check(sse, "expected an event stream, got content-type " + header("content-type"));
      summary = checkStream(sse, cdIndex, spec);
    }
    var fields = spec.fields || [];
    for (var f = 0; f < fields.length; f++) checkField(json, fields[f]);
    var fps = spec.fingerprint || [];
    for (var p = 0; p < fps.length; p++) {
      // A missing payload is not a payload: two responses that both lack it must not match.
      var target = fps[p].path === undefined ? text : pathGet(json, fps[p].path);
      check(target !== undefined && target !== null && target !== "", (fps[p].path || "the response body") + " is missing, so there is nothing to fingerprint");
      if (fps[p].equals) {
        var fp = fingerprint(target);
        check(fp === getVar("fp_" + fps[p].equals), "payload fingerprint " + fp + " differs from " + fps[p].equals + " " + getVar("fp_" + fps[p].equals));
      }
    }
    if (spec.pluginLogs) {
      var pls = Object.prototype.toString.call(spec.pluginLogs) === "[object Array]" ? spec.pluginLogs : [spec.pluginLogs];
      for (var q = 0; q < pls.length; q++) checkPluginLogs(json, pls[q]);
    }
    if (spec.ids) checkIds(json, spec.ids);
    return { cd: cd, json: json, text: text, summary: summary };
  }

  function sortKeys(o) {
    if (!o || typeof o !== "object") return o;
    var out = {};
    var keys = Object.keys(o).sort();
    for (var i = 0; i < keys.length; i++) out[keys[i]] = o[keys[i]];
    return out;
  }

  function saveAll(spec, res) {
    if (spec.save && res.cd) setVar("cid_" + spec.save, res.cd.cache_id);
    if (spec.saveCd && res.cd) setVar("cd_" + spec.saveCd, JSON.stringify(res.cd));
    if (spec.saveReq) setVar("rid_" + spec.saveReq, header("x-bifrost-request-id") || "");
    if (spec.stream && spec.stream.save && res.summary) setVar("st_" + spec.stream.save, JSON.stringify(res.summary));
    var fields = spec.fields || [];
    for (var i = 0; i < fields.length; i++) {
      var v = pathGet(res.json, fields[i].path);
      if (fields[i].save) setVar("f_" + fields[i].save, JSON.stringify(v));
      if (fields[i].saveAs) setVar(fields[i].saveAs, v === undefined || v === null ? "" : String(v));
    }
    var fps = spec.fingerprint || [];
    for (var j = 0; j < fps.length; j++) {
      if (fps[j].save) setVar("fp_" + fps[j].save, fingerprint(fps[j].path === undefined ? res.text : pathGet(res.json, fps[j].path)));
    }
  }

  function sleep(ms) {
    var start = Date.now();
    while (Date.now() - start < ms) {}
  }

  // Asserts spec against the current response. A polled spec re-sends the same request with
  // exponential backoff until it passes, and records a test only on the terminal attempt.
  function run(spec) {
    var key = "__poll_" + pm.info.requestName;
    var attempt = parseInt(getVar(key) || "0", 10);
    var res = null;
    var err = null;
    try {
      spec = resolveSpec(spec);
      res = evaluate(spec);
    } catch (e) {
      err = e;
    }
    if (!err) {
      setVar(key, "0");
      saveAll(spec, res);
      pm.test(spec.name, function () {});
      return true;
    }
    if (spec.poll && attempt < MAX_ATTEMPTS) {
      setVar(key, String(attempt + 1));
      sleep(Math.min(250 * Math.pow(2, attempt), 4000));
      pm.execution.setNextRequest(pm.info.requestName);
      return false;
    }
    setVar(key, "0");
    pm.test(spec.name, function () {
      throw err;
    });
    return false;
  }

  // Busy-waits before the first attempt of a request only, so a polled retry does not wait again.
  function waitFirst(seconds) {
    var attempt = parseInt(getVar("__poll_" + pm.info.requestName) || "0", 10);
    if (attempt === 0 && seconds > 0) sleep(seconds * 1000);
  }

  return {
    run: run,
    waitFirst: waitFirst,
    parseSse: parseSse,
    streamSummary: streamSummary,
    debugFromHeaders: debugFromHeaders,
    fingerprint: fingerprint,
    pathGet: pathGet,
  };
}

// The JS source the collection stores in its scw_lib variable: evaluating it defines `scw`.
export function scwLibSource() {
  return "var scw = (" + scwLib.toString() + ")(pm);";
}
