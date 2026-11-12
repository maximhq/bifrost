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

// schemasLack asserts, on a tools/list answer, that each named tool is listed with an object inputSchema whose
// properties include none of props. A tool or schema that is missing fails rather than passing for want of properties.
export const schemasLack = (tools, props) => A.custom(`${tools.join(", ")} take no ${props.join(" or ")} argument`, [
  "var byName = {}; ((pm.response.json().result || {}).tools || []).forEach(function (t) { byName[t.name] = t; });",
  `${lit(tools)}.forEach(function (n) {`,
  "  pm.expect(byName, n + ' in tools/list').to.have.property(n);",
  "  var schema = byName[n].inputSchema;",
  "  pm.expect(schema, n + ' inputSchema').to.be.an('object');",
  "  var properties = schema.properties || {};",
  `  ${lit(props)}.forEach(function (p) { pm.expect(properties, n).to.not.have.property(p); });`,
  "});",
]);

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

// Upstream OAuth against the mock's authorization server: fleet servers as OAuth protected resources, OAuth
// and per-user OAuth clients, the consent and callback steps, per-user flows and the MCP sessions list.

export const mockAuth = (id, server, title, auth) => mockPost(id, server, "auth", title, { auth });
export const identityCalls = (identity, n) => A.custom(`calls_by_identity[${identity}] is ${n}`, [`pm.expect((pm.response.json().counters.calls_by_identity || {})[${lit(identity)}], 'body: ' + pm.response.text()).to.equal(${n});`]);
export const identityCallsAre = (map) => A.custom(`calls_by_identity is ${lit(map)}`, [`pm.expect(pm.response.json().counters.calls_by_identity || {}, 'body: ' + pm.response.text()).to.eql(${lit(map)});`]);
export const noIdentity = (identity) => A.custom(`calls_by_identity has no ${identity}`, [`pm.expect(pm.response.json().counters.calls_by_identity || {}, 'body: ' + pm.response.text()).to.not.have.property(${lit(identity)});`]);
// serveTools re-adds a fleet server and gives it full tool definitions (annotations, replies, faults).
export const serveTools = (id, name, tools, title) => [
  request(id, `setup: fresh mcpchaos server ${name}`, "POST", "/control", { base: CHAOS, noAuth: true, body: { add: name }, asserts: [A.status(200)] }),
  request(id, `setup: ${name} serves ${title}`, "POST", `/control/${name}/tools/set`, { base: CHAOS, noAuth: true, body: { tools }, asserts: [A.status(200)] }),
];
// ALLOWLIST_TEXT names the private allowlist entry that lets Bifrost dial the mock's authorization server on loopback.
export const ALLOWLIST_TEXT = "mcp_oauth_private_allowlist [\"127.0.0.1\"]";
export const allowlistOn = (id) => [readConfig(id), putConfig(id, `setup: ${ALLOWLIST_TEXT}`, ["cc.mcp_oauth_private_allowlist = ['127.0.0.1'];"], [A.status(200)])];
// asServer re-adds a fleet server (echo_tool and add_tool unless tools are given) and makes it an OAuth protected
// resource with its own authorization server configured by cfg.
export const asServer = (id, name, cfg = {}, tools) => [
  ...(tools ? serveTools(id, name, tools.list, tools.title) : freshServer(id, name, ["echo_tool", "add_tool"])),
  mockAuth(id, name, `setup: ${name} is an OAuth protected resource with its own authorization server${Object.keys(cfg).length ? " (" + lit(cfg) + ")" : ""}`, { oauth: cfg }),
];
export const oauthBody = (clientId, name, server, over = {}) => ({ client_id: clientId, name, connection_type: "http", connection_string: `${CHAOS}/s/${server}/mcp`, auth_type: "oauth", oauth_config: {}, tools_to_execute: ["*"], ...over });
// createOAuth creates an OAuth client and keeps its oauth_config_id and authorize_url in <p>_oc and <p>_au.
export const createOAuth = (id, title, body, p, asserts = []) => request(id, title, "POST", "/api/mcp/client", {
  body, asserts: [A.status(200), ...asserts],
  extraTests: [`pm.collectionVariables.set('${p}_oc', pm.response.json().oauth_config_id);`, `pm.collectionVariables.set('${p}_au', pm.response.json().authorize_url);`],
});
// consentAs follows <p>_au on the mock without following the redirect, approving as identity (login_hint), and keeps
// the Location, Bifrost's callback, in <p>_cb.
export const consentAs = (id, title, p, identity, asserts = [A.status(302)]) => request(id, title, "GET", identity ? `&login_hint=${identity}` : "", {
  base: `{{${p}_au}}`, noAuth: true, noRedirect: true, noCookies: true, asserts,
  extraTests: [`pm.collectionVariables.set('${p}_cb', pm.response.headers.get('Location'));`],
});
export const followCallback = (id, title, p, asserts) => request(id, title, "GET", "", { base: `{{${p}_cb}}`, noAuth: true, noRedirect: true, noCookies: true, asserts });
export const completeOAuth = (id, title, p, asserts) => request(id, title, "POST", `/api/mcp/client/{{${p}_oc}}/complete-oauth`, { asserts });
export const oauthStatus = (id, title, p, query, asserts, extraTests = []) => request(id, title, "GET", `/api/oauth/config/{{${p}_oc}}/status${query}`, { asserts: [A.status(200), ...asserts], extraTests });
export const locationEnds = (s) => A.custom(`Location ends ${lit(s)}`, [
  "var loc = pm.response.headers.get('Location') || '';",
  `pm.expect(loc.slice(-${s.length}), 'Location: ' + loc).to.equal(${lit(s)});`,
]);
export const SUCCESS_CB = "/workspace/mcp-registry/oauth-callback?status=success";
export const callbackSuccess = () => [A.status(302), locationEnds(SUCCESS_CB)];
// connectOAuth creates an OAuth client and takes it through consent, callback and complete.
export const connectOAuth = (id, body, p, identity, label) => [
  createOAuth(id, `setup: create ${label}`, body, p),
  consentAs(id, `setup: consent for ${label}${identity ? " as " + identity : ""}`, p, identity),
  followCallback(id, `setup: follow the callback for ${label}`, p, callbackSuccess()),
  completeOAuth(id, `setup: complete ${label}`, p, [A.status(200)]),
];
// QS_FN parses a URL's query string into an object (ES5, for test scripts).
export const QS_FN = "function qs(u) { var o = {}; (String(u).split('?')[1] || '').split('#')[0].split('&').forEach(function (kv) { var i = kv.indexOf('='); if (i > 0) { o[decodeURIComponent(kv.slice(0, i))] = decodeURIComponent(kv.slice(i + 1).replace(/\\+/g, ' ')); } }); return o; }";
export const statusIs = (v) => A.custom(`status is ${lit(v)}`, [`pm.expect(pm.response.json().status, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
export const credStatus = (v) => A.custom(`credential status is ${lit(v)}`, [`pm.expect((pm.response.json().clients[0].credential || {}).status, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
// PU_TEXT describes the config per-user OAuth tests run with; a per_user_oauth client is verified at create time by an
// admin consent (login_hint admin), and callers then consent as their own identity through the per-user flow.
export const PU_TEXT = (tempTokens) => `${ALLOWLIST_TEXT}, mcp_enable_temp_token_auth ${tempTokens}`;
export const puConfig = (id, tempTokens) => [readConfig(id), putConfig(id, `setup: ${PU_TEXT(tempTokens)}`, ["cc.mcp_oauth_private_allowlist = ['127.0.0.1'];", `cc.mcp_enable_temp_token_auth = ${tempTokens};`], [A.status(200)])];
export const PU_SERVER_TEXT = (p, cfgText = "") => `a fleet server ${p} (echo_tool, add_tool) as an OAuth protected resource with dynamic registration${cfgText}; a per_user_oauth client ${p} on it with tools_to_execute ["*"]`;
export const puBody = (p) => oauthBody(`${p}-u`, p, p, { auth_type: "per_user_oauth" });
// puConnect creates a per_user_oauth client (with over merged into its config) and verifies it by consenting as admin.
export const puConnect = (id, p, cfg = {}, over = {}, tools) => [
  ...asServer(id, p, cfg, tools),
  createOAuth(id, "setup: create the per-user OAuth client", { ...puBody(p), ...over }, p),
  consentAs(id, "setup: consent for the per-user OAuth client as admin", p, "admin"),
  followCallback(id, "setup: follow the callback for the per-user OAuth client", p, callbackSuccess()),
  completeOAuth(id, "setup: complete the per-user OAuth client", p, [A.status(200)]),
];
export const PU_FLOWS = "/api/oauth/per-user/flows/";
export const PU_SUCCESS = "/workspace/mcp-sessions/auth-success";
export const flowDetail = (id, title, v, asserts, opts = {}) => request(id, title, "GET", `${PU_FLOWS}{{${v}}}`, { ...opts, asserts });
// flowStart keeps the authorize_url it returns in <v>_au, which consentAs follows.
export const flowStart = (id, title, v, asserts = [A.status(200)], opts = {}) => request(id, title, "GET", `${PU_FLOWS}{{${v}}}/start`, {
  ...opts, asserts, extraTests: [`pm.collectionVariables.set('${v}_au', pm.response.json().authorize_url || '');`],
});
// puFinish takes flow v through start, consent as identity and the callback, which lands on the public success page.
export const puFinish = (id, v, identity, label) => [
  flowStart(id, `setup: start ${label}`, v),
  consentAs(id, `setup: consent for ${label} as ${identity}`, v, identity),
  followCallback(id, `setup: follow the callback for ${label}`, v, [A.status(302), locationEnds(PU_SUCCESS)]),
];
// TT sends only flow v's temp token, kept in <v>_tt by authFlow.
export const TT = (v) => ({ noAuth: true, noCookies: true, headers: [{ key: "X-Bifrost-Temp-Token", value: `{{${v}_tt}}` }] });
// keepFlow keeps the flow id of a 400's mcp_auth_required in v; with otherThan it also checks the flow is a new one.
export const keepFlow = (v, otherThan) => A.custom(`mcp_auth_required kind "oauth" names a flow${otherThan ? ", a new one" : ""}`, [
  "var r = (pm.response.json().extra_fields || {}).mcp_auth_required;",
  "pm.expect(r && r.kind, 'body: ' + pm.response.text()).to.equal('oauth');",
  "pm.expect(r.session_id, 'body: ' + pm.response.text()).to.be.a('string').and.not.empty;",
  ...(otherThan ? [`pm.expect(r.session_id).to.not.equal(pm.collectionVariables.get(${lit(otherThan)}));`] : []),
  `pm.collectionVariables.set(${lit(v)}, r.session_id);`,
]);
// authFlow checks a 400's mcp_auth_required in full: kind "oauth", authorize_url ending /workspace/mcp-sessions/auth?flow=<flow id>
// and session_id equal to that flow id. Without temp tokens: no #t= and the exact message. With them: a #t= token, kept in <saveAs>_tt, and
// the required-fragment notice. saveAs keeps F; sameAs checks F against a kept one.
export const authFlow = (name, { saveAs, sameAs, tempToken = false }) => A.custom(`mcp_auth_required kind "oauth", authorize_url .../workspace/mcp-sessions/auth?flow=<flow id>${tempToken ? "#t=<token>" : " with no #t="}, session_id is the flow id${sameAs ? ", the same flow id" : ""}, ${tempToken ? "message has \"IMPORTANT: this link includes a required fragment\"" : `message "Authentication required for ${name}. Visit <url> to connect your account."`}`, [
  "var r = (pm.response.json().extra_fields || {}).mcp_auth_required;",
  "pm.expect(r, 'body: ' + pm.response.text()).to.be.an('object');",
  "pm.expect(r.kind).to.equal('oauth');",
  "var m = /\\/workspace\\/mcp-sessions\\/auth\\?flow=([^&#]+)(#t=(.*))?$/.exec(r.authorize_url);",
  "pm.expect(m, 'authorize_url: ' + r.authorize_url).to.not.equal(null);",
  "pm.expect(r.session_id).to.equal(m[1]);",
  ...(tempToken ? [
    "pm.expect(m[3], 'authorize_url: ' + r.authorize_url).to.be.a('string').and.not.empty;",
    "pm.expect(r.message).to.include('IMPORTANT: this link includes a required fragment');",
    ...(saveAs ? [`pm.collectionVariables.set(${lit(saveAs + "_tt")}, m[3]);`] : []),
  ] : [
    "pm.expect(r.authorize_url, 'authorize_url').to.not.include('#t=');",
    `pm.expect(r.message).to.equal(${lit(`Authentication required for ${name}. Visit `)} + r.authorize_url + ' to connect your account.');`,
  ]),
  ...(saveAs ? [`pm.collectionVariables.set(${lit(saveAs)}, m[1]);`] : []),
  ...(sameAs ? [`pm.expect(m[1]).to.equal(pm.collectionVariables.get(${lit(sameAs)}));`] : []),
]);
// startUrl checks a flow start's authorize_url: server p's authorize endpoint with a client_id, S256, a state and
// Bifrost's callback as redirect_uri.
export const startUrl = (p) => A.custom(`authorize_url is ${p}'s authorize endpoint with client_id, S256, a state and the callback redirect_uri`, [QS_FN,
  "var u = pm.response.json().authorize_url;",
  `pm.expect(u, 'body: ' + pm.response.text()).to.include(${lit(`/s/${p}/as/authorize?`)});`,
  "var q = qs(u);",
  "pm.expect(q.client_id, 'query').to.be.a('string').and.not.empty;",
  "pm.expect(q.code_challenge_method, 'query').to.equal('S256');",
  "pm.expect(q.code_challenge, 'query').to.be.a('string').and.not.empty;",
  "pm.expect(q.state, 'query').to.be.a('string').and.not.empty;",
  "pm.expect(/\\/api\\/oauth\\/callback$/.test(q.redirect_uri || ''), 'redirect_uri ' + q.redirect_uri).to.equal(true);",
]);
// The sessions list is read whole (limit 500) and filtered here by client and an ES5 predicate on the entry s.
export const SESSIONS = "/api/mcp/sessions?limit=500";
export const sessionRows = (clientId, pred) => `var found = (pm.response.json().sessions || []).filter(function (s) { return s.mcp_client && s.mcp_client.client_id === ${lit(clientId)} && (${pred}); });`;
export const saveSession = (id, title, clientId, pred, v) => request(id, title, "GET", SESSIONS, {
  asserts: [A.status(200), A.custom("exactly one matching entry", [sessionRows(clientId, pred), "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);"])],
  extraTests: [sessionRows(clientId, pred), `pm.collectionVariables.set(${lit(v)}, found.length ? found[0].id : '');`],
});
export const sessionsHas = (id, title, clientId, pred, what) => request(id, title, "GET", SESSIONS, {
  asserts: [A.status(200), A.custom(what, [sessionRows(clientId, pred), "pm.expect(found.length, 'body: ' + pm.response.text()).to.equal(1);"])],
});
export const vkPred = (v) => `s.kind === 'token' && s.virtual_key && s.virtual_key.id === pm.collectionVariables.get(${lit(v + "_id")})`;
export const sessionPred = (sid) => `s.kind === 'token' && s.session_id === ${lit(sid)}`;
// puVK creates a virtual key granted every tool of client p, keeping its id and value in v_id and v.
export const puVK = (id, label, p, v) => vkCreate(id, `setup: create the ${label} VK, granted every tool of ${p}`, { name: `${id} ${label} key`, mcp_configs: [{ mcp_client_name: p, tools_to_execute: ["*"] }] }, v);
export const vkExec = (v) => ({ headers: [{ key: "x-bf-vk", value: `{{${v}}}` }] });
export const credKind = (v) => A.custom(`credential kind is ${lit(v)}`, [`pm.expect((pm.response.json().clients[0].credential || {}).kind, 'body: ' + pm.response.text()).to.equal(${lit(v)});`]);
