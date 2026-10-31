// Setup token for the calls a harness script makes itself.
//
// While dashboard auth is not active, the OSS setup lock refuses every /api call that lacks the
// operator's setup token. augment-provider-harness.mjs adds the token to each row's own request
// from a collection-level prerequest, but a call a script sends through pm.sendRequest is a
// separate request no collection script sees, so every in-script management call (creating
// providers, keys, virtual keys and rules, reading /api/logs) is refused with 401. Reassigning
// pm.sendRequest does not help: the sandbox ignores the assignment.
//
// So each script that calls pm.sendRequest is rewritten to call a helper defined at its top. The
// helper adds the token to calls aimed at this gateway's /api and passes every other call through
// as it was, so the token never reaches a local fixture or a third party. A script that names
// X-Bifrost-Setup-Token manages the header itself (the lockout rows) and is left alone.

export const SEND_HELPER = "__bfSend";
const CALL = "pm.sendRequest(";
const TOKEN_HEADER = "X-Bifrost-Setup-Token";

// The helper's source, reading the token from the collection variable tokenVar.
export function setupTokenHelper(tokenVar) {
  return [
    `function ${SEND_HELPER}(req, cb) {`,
    `  var token = pm.variables.get('${tokenVar}'), base = pm.variables.get('baseUrl') || '';`,
    "  if (typeof req === 'string') { req = { url: req, method: 'GET' }; }",
    "  var url = String((req && req.url && req.url.raw) || (req && req.url) || '');",
    "  var ours = (base !== '' && url.indexOf(base + '/api/') === 0) || url.indexOf('{{baseUrl}}/api/') === 0;",
    "  if (token && ours) {",
    `    var named = function (k) { return String(k).toLowerCase() === '${TOKEN_HEADER.toLowerCase()}'; };`,
    "    var h = req.header;",
    "    if (Array.isArray(h)) {",
    `      if (!h.some(function (x) { return x && named(x.key); })) { h = h.concat([{ key: '${TOKEN_HEADER}', value: token }]); }`,
    "    } else if (typeof h === 'string') {",
    `      if (!/^x-bifrost-setup-token\\s*:/im.test(h)) { h = h + (h ? '\\n' : '') + '${TOKEN_HEADER}: ' + token; }`,
    "    } else {",
    `      h = Object.assign({}, h || {}); if (!Object.keys(h).some(named)) { h['${TOKEN_HEADER}'] = token; }`,
    "    }",
    "    req = Object.assign({}, req, { header: h });",
    "  }",
    "  return pm.sendRequest(req, cb);",
    "}",
  ];
}

// Rewrites every collection, folder and request script that calls pm.sendRequest to go through
// the helper, and returns how many scripts it rewrote. Running it again changes nothing.
export function routeScriptCallsThroughSetupToken(collection, tokenVar) {
  let rewritten = 0;
  const visit = (events) => {
    for (const event of events || []) {
      const script = event && event.script;
      if (!script || script.exec == null) continue;
      const source = Array.isArray(script.exec) ? script.exec.join("\n") : String(script.exec);
      if (!source.includes(CALL) || source.includes(TOKEN_HEADER) || source.includes(`function ${SEND_HELPER}(`)) continue;
      script.exec = [...setupTokenHelper(tokenVar), ...source.split(CALL).join(`${SEND_HELPER}(`).split("\n")];
      rewritten++;
    }
  };
  const walk = (items) => {
    for (const item of items || []) {
      visit(item.event);
      walk(item.item);
    }
  };
  visit(collection.event);
  walk(collection.item);
  return rewritten;
}
