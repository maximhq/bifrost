// Building blocks of the MCP harness collection (runners/build-mcp-harness.mjs), shared with other
// collection builders: requests with their assertions, client config switches, MCP JSON-RPC calls,
// mcpchaos fleet control and virtual keys. Each returns plain Postman v2.1 JSON or ES5 test lines.

export const AUTH = { key: "Authorization", value: "{{admin_auth_header}}" };

export const JSONCT = { key: "Content-Type", value: "application/json" };

export const lit = (v) => JSON.stringify(v);

// assertion builders: each returns ES5 lines
export const A = {
  status: (code) => ({ title: `status is ${code}`, body: [`pm.expect(pm.response.code, 'body: ' + pm.response.text()).to.equal(${code});`] }),
  msgEquals: (m) => ({ title: `error message is ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.equal(${lit(m)});`] }),
  msgContains: (m) => ({ title: `error message contains ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.include(${lit(m)});`] }),
  msgStartsWith: (m) => ({ title: `error message starts with ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.error && b.error.message, 'body: ' + pm.response.text()).to.be.a('string');`, `pm.expect(b.error.message.indexOf(${lit(m)}), 'message: ' + b.error.message).to.equal(0);`] }),
  jsonEquals: (field, v) => ({ title: `${field} is ${lit(v)}`, body: [`var b = pm.response.json();`, `pm.expect(b[${lit(field)}], 'body: ' + pm.response.text()).to.equal(${lit(v)});`] }),
  jsonMessage: (m) => ({ title: `message is ${lit(m)}`, body: [`var b = pm.response.json();`, `pm.expect(b.message, 'body: ' + pm.response.text()).to.equal(${lit(m)});`] }),
  custom: (title, lines) => ({ title, body: lines }),
  // skip marks an assertion as skipped (see request).
  skip: (a) => ({ ...a, skip: true }),
};

export function request(id, name, method, path, { raw, body, asserts = [], pre = [], extraTests = [], noAuth = false, noRedirect = false, base = "{{base_url}}", headers = [], noAccept = false, noCookies = false } = {}) {
  const full = `${id} ${name}`;
  const exec = [];
  for (const a of asserts) {
    // A skipped assertion states behaviour a known issue still breaks; it is reported as skipped until the fix lands.
    exec.push(`${a.skip ? "pm.test.skip" : "pm.test"}(${lit(`${full}: ${a.title}${a.skip ? " (skipped: known issue)" : ""}`)}, function () {`);
    for (const l of a.body) exec.push(`  ${l}`);
    exec.push(`});`);
  }
  exec.push(...extraTests);
  const event = [];
  if (pre.length) event.push({ listen: "prerequest", script: { type: "text/javascript", exec: pre } });
  event.push({ listen: "test", script: { type: "text/javascript", exec } });
  const header = noAuth ? [] : [AUTH];
  const req = { method, header, url: `${base}${path}` };
  const rawBody = raw !== undefined ? raw : body !== undefined ? JSON.stringify(body) : undefined;
  if (rawBody !== undefined) {
    if (!headers.some((h) => h.key.toLowerCase() === "content-type")) header.push(JSONCT);
    req.body = { mode: "raw", raw: rawBody };
  }
  header.push(...headers);
  const item = { name: full, event, request: req };
  const behavior = {};
  // Redirect tests assert the 302 itself, so Newman must not follow it.
  if (noRedirect) behavior.followRedirects = false;
  // Tests about a missing Accept header stop Newman from adding its own.
  if (noAccept) behavior.disabledSystemHeaders = { accept: true };
  // Tests that handle a session cookie keep it out of Newman's shared cookie jar.
  if (noCookies) behavior.disableCookies = true;
  if (Object.keys(behavior).length) item.protocolProfileBehavior = behavior;
  return item;
}

export const folder = (name, description, item) => ({ name, description, item });

// readConfig stores the current client_config; putConfig sends it back with one change.
export const readConfig = (id) => request(id, "setup: read the current client config", "GET", "/api/config", {
  asserts: [A.status(200)],
  extraTests: ["pm.collectionVariables.set('client_config', JSON.stringify(pm.response.json().client_config));"],
});

// mutate is ES5 source operating on `cc`.
export const putConfig = (id, name, mutate, asserts) => request(id, name, "PUT", "/api/config", {
  raw: "{{config_body}}",
  pre: [
    "var cc = JSON.parse(pm.collectionVariables.get('client_config'));",
    ...mutate,
    "pm.variables.set('config_body', JSON.stringify({ client_config: cc }));",
  ],
  asserts,
});

export const restoreConfig = (id) => putConfig(id, "teardown: restore the original config", [], [A.status(200)]);

// OAuth endpoints answer {error, error_description}.
export const O = {
  error: (code) => A.custom(`error is ${lit(code)}`, ["var b = pm.response.json();", `pm.expect(b.error, 'body: ' + pm.response.text()).to.equal(${lit(code)});`]),
  desc: (text) => A.custom(`error_description contains ${lit(text)}`, ["var b = pm.response.json();", `pm.expect(b.error_description, 'body: ' + pm.response.text()).to.include(${lit(text)});`]),
  emptyBody: () => A.custom("body is empty", ["pm.expect(pm.response.text(), 'body: ' + pm.response.text()).to.equal('');"]),
  noLocation: () => A.custom("there is no Location header", ["pm.expect(pm.response.headers.has('Location'), 'Location: ' + pm.response.headers.get('Location')).to.equal(false);"]),
  descEquals: (text) => A.custom(`error_description is ${lit(text)}`, ["var b = pm.response.json();", `pm.expect(b.error_description, 'body: ' + pm.response.text()).to.equal(${lit(text)});`]),
  noStore: () => A.custom("Cache-Control is no-store", ["pm.expect(pm.response.headers.get('Cache-Control')).to.equal('no-store');"]),
};

// setMode switches mcp_server_auth_mode with issuer_url = base_url, or the given issuer; restoreHeaders puts it back.
export const setMode = (id, mode, issuer) => putConfig(id, `setup: switch mcp_server_auth_mode to ${mode} with issuer_url set to ${issuer || "the base URL"}`, [
  `cc.mcp_server_auth_mode = ${lit(mode)};`,
  `cc.oauth2_server_config = { issuer_url: ${issuer ? lit(issuer) : "pm.variables.get('base_url')"} };`,
], [A.status(200)]);

export const restoreHeaders = (id) => putConfig(id, "teardown: restore mcp_server_auth_mode headers", ["cc.mcp_server_auth_mode = 'headers';"], [A.status(200)]);

export const withMode = (id, mode, items, issuer) => [readConfig(id), setMode(id, mode, issuer), ...items, restoreHeaders(id)];

// authMode sets mcp_server_auth_mode (H, B or O) and enforce_auth_on_inference; B and O also set
// issuer_url (the base URL unless issuer is given) and temp-token auth to tt.
export const MODE_NAMES = { H: "headers", B: "both", O: "oauth" };

// modePrefix names a request's mode in a test that repeats requests per mode; headers, the boot mode, adds nothing.
export const modePrefix = (m) => (m === "B" || m === "O" ? `${MODE_NAMES[m]}: ` : "");

export const authMode = (id, m, enforce, tt = true, issuer) => putConfig(id, `setup: mcp_server_auth_mode ${MODE_NAMES[m]}, enforce_auth_on_inference ${enforce ? "on" : "off"}${m === "H" ? "" : `, issuer_url set to ${issuer || "the base URL"}, temp-token auth ${tt ? "on" : "off"}`}`, [
  `cc.mcp_server_auth_mode = ${lit(MODE_NAMES[m])};`,
  `cc.enforce_auth_on_inference = ${enforce};`,
  ...(m === "H" ? [] : [`cc.oauth2_server_config = { issuer_url: ${issuer ? lit(issuer) : "pm.variables.get('base_url')"} };`, `cc.mcp_enable_temp_token_auth = ${tt};`]),
], [A.status(200)]);

export const CHAOS = "{{chaos_url}}";

// freshServer re-adds a fleet server, which resets its tools, faults, auth and
// counters, then gives it the test's tools.
export const freshServer = (id, name, tools) => [
  request(id, `setup: fresh mcpchaos server ${name}`, "POST", "/control", { base: CHAOS, noAuth: true, body: { add: name }, asserts: [A.status(200)] }),
  request(id, `setup: ${name} serves ${tools.join(", ")}`, "POST", `/control/${name}/tools/set`, { base: CHAOS, noAuth: true, body: { tools: tools.map((t) => ({ name: t })) }, asserts: [A.status(200)] }),
];

export const mockGet = (id, name, title, asserts, extraTests = []) => request(id, title, "GET", `/control/${name}`, { base: CHAOS, noAuth: true, asserts: [A.status(200), ...asserts], extraTests });

export const mockPost = (id, name, verb, title, body) => request(id, title, "POST", `/control/${name}/${verb}`, { base: CHAOS, noAuth: true, body, asserts: [A.status(200)] });

export const createClient = (id, cfg, title) => request(id, title || `setup: create client ${cfg.name}`, "POST", "/api/mcp/client", {
  body: cfg, asserts: [A.status(200)],
});

export const deleteClient = (id, clientId) => request(id, `teardown: delete client ${clientId}`, "DELETE", `/api/mcp/client/${clientId}`, { asserts: [A.status(200)] });

export const getClient = (id, clientId, title, asserts, opts = {}) => request(id, title, "GET", `/api/mcp/clients?server=${clientId}`, {
  ...opts,
  asserts: [A.status(200), A.custom("exactly one client is returned", ["pm.expect(pm.response.json().clients.length, 'body: ' + pm.response.text()).to.equal(1);"]), ...asserts],
});

export const toolNamesAre = (names) => A.custom(`tools are ${lit(names)}`, [
  "var c = pm.response.json().clients[0];",
  "var names = (c.tools || []).map(function (t) { return t.name; }).sort();",
  `pm.expect(names, 'body: ' + pm.response.text()).to.eql(${lit(names)});`,
]);

export const clientState = (state) => A.custom(`state is ${lit(state)}`, ["pm.expect(pm.response.json().clients[0].state, 'body: ' + pm.response.text()).to.equal(" + lit(state) + ");"]);

// waitForClient polls the client list, 250ms apart, until cond (ES5 on the client c) holds or tries polls ran (20 by
// default, about 5s).
export const waitForClient = (clientId, cond, tries = 20) => [
  `var left = ${tries};`,
  "function poll() {",
  `  pm.sendRequest({ url: pm.variables.get('base_url') + '/api/mcp/clients?server=${clientId}', method: 'GET', header: { Authorization: pm.variables.get('admin_auth_header') } }, function (err, res) {`,
  "    var list = !err && res.code === 200 ? res.json().clients : null;",
  "    var c = list && list[0];",
  `    if ((c && (${cond})) || --left <= 0) { return; }`,
  "    setTimeout(poll, 250);",
  "  });",
  "}",
  "poll();",
];

export const httpClient = (clientId, name, server, over = {}) => ({ client_id: clientId, name, connection_type: "http", connection_string: `${CHAOS}/s/${server}/mcp`, auth_type: "none", ...over });

export const MCP_ACCEPT = { key: "Accept", value: "application/json, text/event-stream" };

export const rpc = (id, title, message, opts = {}) => request(id, title, "POST", opts.path || "/mcp", {
  noAuth: true, raw: typeof message === "string" ? message : JSON.stringify(message), headers: opts.headers || [MCP_ACCEPT], asserts: opts.asserts || [], noAccept: opts.noAccept, pre: opts.pre || [],
});

export const rpcResultEquals = (v) => A.custom(`result is ${lit(v)}`, ["var b = pm.response.json();", `pm.expect(b.result, 'body: ' + pm.response.text()).to.eql(${lit(v)});`]);

export const rpcError = (code, message) => A.custom(`error.code ${code}${message ? ", message " + lit(message) : ""}`, [
  "var b = pm.response.json();",
  `pm.expect(b.error && b.error.code, 'body: ' + pm.response.text()).to.equal(${code});`,
  ...(message ? [`pm.expect(b.error.message).to.equal(${lit(message)});`] : []),
]);

export const rpcResult = () => A.custom("a JSON-RPC result and no error", ["var b = pm.response.json();", "pm.expect(b, 'body: ' + pm.response.text()).to.have.property('result');", "pm.expect(b).to.not.have.property('error');"]);

export const setEnforce = (id, on) => putConfig(id, `${on ? "setup" : "teardown"}: set enforce_auth_on_inference ${on}`, [`cc.enforce_auth_on_inference = ${on};`], [A.status(200)]);

export const toolNamesInList = (names) => A.custom(`tools/list names are exactly ${lit(names)}`, [
  "var names = pm.response.json().result.tools.map(function (t) { return t.name; });",
  `pm.expect(names, 'body: ' + pm.response.text()).to.eql(${lit(names)});`,
]);

export const toolResultError = (startsWith, contains) => A.custom(`isError true${startsWith ? ", text starts " + lit(startsWith) : ""}${contains ? ", text contains " + lit(contains) : ""}`, [
  "var r = pm.response.json().result;",
  "pm.expect(r && r.isError, 'body: ' + pm.response.text()).to.equal(true);",
  "var text = r.content[0].text;",
  ...(startsWith ? [`pm.expect(text.indexOf(${lit(startsWith)}), 'text: ' + text).to.equal(0);`] : []),
  ...(contains ? [`pm.expect(text, 'text: ' + text).to.include(${lit(contains)});`] : []),
]);

export const mockCalls = (id, server, tool, n) => mockGet(id, server, `${server} calls[${tool}] is ${n}`, [A.custom(`calls[${tool}] is ${n}`, [
  `pm.expect(pm.response.json().counters.calls[${lit(tool)}] || 0, 'body: ' + pm.response.text()).to.equal(${n});`,
])]);

export const call = (name, args = {}) => ({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name, arguments: args } });

export const init = (version) => ({ jsonrpc: "2.0", id: 1, method: "initialize", params: { ...(version ? { protocolVersion: version } : {}), capabilities: {}, clientInfo: { name: "mcp-harness", version: "1.0.0" } } });

export const vkCreate = (id, title, body, varPrefix) => request(id, title, "POST", "/api/governance/virtual-keys", {
  body, asserts: [A.status(200)],
  extraTests: [`pm.collectionVariables.set(${lit(varPrefix + "_id")}, pm.response.json().virtual_key.id);`, `pm.collectionVariables.set(${lit(varPrefix)}, pm.response.json().virtual_key.value);`],
});

export const vkDelete = (id, title, varPrefix) => request(id, title, "DELETE", `/api/governance/virtual-keys/{{${varPrefix}_id}}`, { asserts: [A.status(200)] });
