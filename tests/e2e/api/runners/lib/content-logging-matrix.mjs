// Content-logging permutation matrix: every combination of the layers that decide whether a
// request's content is stored in the log store and exported to an observability connector.
//
//   virtual key        inherit | on (disable_content_logging: false) | off (true)
//   client (global)    on | off                (client.disable_content_logging false | true)
//   connector (OTel)   on | off                (profile disable_content_logging false | true)
//   per-request gate   allowed | not allowed   (client.allow_per_request_content_storage_override)
//   request header     absent | on | off       (x-bf-disable-content-logging: false | true)
//
// Pure: no network. run-content-logging-matrix.mjs drives a live gateway through every case and
// checks it against expectedOutcome; content-logging-matrix.test.mjs pins the table itself.

export const VK_MODES = ["inherit", "on", "off"];
export const GLOBAL_MODES = ["on", "off"];
export const CONNECTOR_MODES = ["on", "off"];
export const OVERRIDE_MODES = ["allowed", "blocked"];
export const HEADER_MODES = ["absent", "on", "off"];

// vkDisableValue is what the key's disable_content_logging field is set to for a mode: undefined
// means the field is omitted on create, so the key inherits.
export function vkDisableValue(vk) {
	if (vk === "on") return false;
	if (vk === "off") return true;
	return undefined;
}

// headerValue is the x-bf-disable-content-logging header for a mode, or undefined to omit it.
export function headerValue(header) {
	if (header === "on") return "false";
	if (header === "off") return "true";
	return undefined;
}

// expectedOutcome is the contract under test.
//
// Log store: the client flag decides, the key's own decision overrides it in both directions, and
// an allowed per-request header overrides both. A header is ignored while overrides are blocked.
//
// Connector: the connector's own flag decides. A key set to off also strips content for its
// traffic; a key set to on never loosens a connector that disables content. The per-request header
// never reaches a connector.
export function expectedOutcome({ vk, global, connector, override, header }) {
	let logDisabled = global === "off";
	if (vk === "on") logDisabled = false;
	if (vk === "off") logDisabled = true;
	if (override === "allowed") {
		if (header === "on") logDisabled = false;
		if (header === "off") logDisabled = true;
	}
	const connectorDisabled = connector === "off" || vk === "off";
	return { logStoresContent: !logDisabled, connectorExportsContent: !connectorDisabled };
}

// allCases is the full cross product, grouped so every case that shares a gateway configuration
// (global, connector, override) is adjacent: the runner reconfigures once per group.
export function allCases() {
	const cases = [];
	for (const global of GLOBAL_MODES) {
		for (const connector of CONNECTOR_MODES) {
			for (const override of OVERRIDE_MODES) {
				for (const vk of VK_MODES) {
					for (const header of HEADER_MODES) {
						const c = { vk, global, connector, override, header };
						cases.push({ ...c, id: caseID(c), expected: expectedOutcome(c) });
					}
				}
			}
		}
	}
	return cases;
}

export function caseID({ vk, global, connector, override, header }) {
	return `vk-${vk}.global-${global}.connector-${connector}.override-${override}.header-${header}`;
}

// groupByConfig splits cases into the gateway configurations they need, in allCases order.
export function groupByConfig(cases) {
	const groups = new Map();
	for (const c of cases) {
		const key = `${c.global}|${c.connector}|${c.override}`;
		if (!groups.has(key)) {
			groups.set(key, { global: c.global, connector: c.connector, override: c.override, cases: [] });
		}
		groups.get(key).cases.push(c);
	}
	return [...groups.values()];
}

// DEFAULT_LOG_RETENTION_DAYS mirrors defaultRetentionDays in framework/logstore/cleaner.go, which
// the cleaner applies to any configured value below 1.
export const DEFAULT_LOG_RETENTION_DAYS = 365;

// normaliseRetentionDays returns the log_retention_days value to write back when round-tripping
// client_config: GET reports an unset value as 0, which PUT rejects. The cleaner already treats that
// unset value as its 365-day default, so writing the default keeps effective retention unchanged;
// writing 1 (the smallest accepted value) would make the cleaner delete every log older than a day.
export function normaliseRetentionDays(days) {
	return days >= 1 ? days : DEFAULT_LOG_RETENTION_DAYS;
}

// columnText renders one raw column value (string, Buffer from a sqlite blob, JSON from pg) as text.
function columnText(value) {
	if (value == null) return "";
	if (Buffer.isBuffer(value)) return value.toString("utf8");
	if (typeof value === "object") return JSON.stringify(value);
	return String(value);
}

// isTrue reads a boolean column as either driver returns it: pg gives true/false, sqlite 1/0.
function isTrue(value) {
	return value === true || value === 1 || value === "1" || value === "t" || value === "true";
}

// logRowFailures checks a raw logs-table row, read straight from the logs database, for one case.
//
// The marker is a random token that only ever travels inside the request's message content, so it
// is scanned for across every column rather than a list of content columns: a column added later
// that starts carrying content is caught without this list having to know about it.
//   - expectStored false: the row must be content_hidden and no column may hold the marker.
//   - expectStored true: the row must not be content_hidden and input_history must hold the marker.
export function logRowFailures(row, marker, expectStored) {
	const failures = [];
	const hidden = isTrue(row.content_hidden);
	if (expectStored) {
		if (hidden) failures.push("raw row is content_hidden, want visible content");
		if (!columnText(row.input_history).includes(marker)) failures.push("raw row input_history lacks the request's content");
		return failures;
	}
	if (!hidden) failures.push("raw row is not content_hidden");
	const leaked = Object.keys(row).filter((col) => columnText(row[col]).includes(marker));
	if (leaked.length > 0) failures.push(`raw row still holds content in ${leaked.join(", ")}`);
	return failures;
}

// callbackEndpoints says where the runner's local echo provider and OTLP collector listen, and how
// the gateway should address them. The gateway dials both, so they must be reachable from where the
// gateway runs: loopback when it shares the runner's host (the default, and every CI path today),
// or a caller-supplied callbackHost (BIFROST_E2E_CALLBACK_HOST, e.g. host.docker.internal) when it
// runs in another container or host, in which case the mocks listen on every interface.
export function callbackEndpoints({ callbackHost, providerPort, collectorPort }) {
	const host = callbackHost || "127.0.0.1";
	const loopback = host === "127.0.0.1" || host === "localhost" || host === "::1";
	return {
		listenHost: loopback ? "127.0.0.1" : "0.0.0.0",
		providerBaseURL: `http://${host}:${providerPort}`,
		collectorURL: `http://${host}:${collectorPort}/v1/traces`,
	};
}
