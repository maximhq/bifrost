// Preload for bin.test.js (node --require, which every supported Node accepts): replaces fetch so
// bin.js talks to an in-memory download server.
// BIFROST_TEST_SCENARIO is JSON: { latest, checksum: "match" | "mismatch" | "missing" | "malformed" |
// "unavailable" | "unreachable" | "body-error", removeCached: "before-read" | "after-read", cachedPath }. removeCached
// stands in for a second launcher deleting the cached binary while this one is checking it.
const { createHash } = require("crypto");
const fs = require("fs");
const { syncBuiltinESMExports } = require("module");

const scenario = JSON.parse(process.env.BIFROST_TEST_SCENARIO || "{}");
const binary = "#!/bin/sh\necho BIFROST_TEST_BINARY_RAN\n";
const binaryHash = createHash("sha256").update(binary).digest("hex");

if (scenario.removeCached === "after-read") {
	const readFileSync = fs.readFileSync;
	fs.readFileSync = function (path, ...rest) {
		const contents = readFileSync.call(this, path, ...rest);
		if (String(path) === scenario.cachedPath) {
			fs.rmSync(scenario.cachedPath, { force: true });
		}
		return contents;
	};
	syncBuiltinESMExports();
}

globalThis.fetch = async (url, init = {}) => {
	const target = String(url);
	if (target.endsWith("/latest-release")) {
		return scenario.latest
			? new Response(JSON.stringify({ name: scenario.latest }), { headers: { "content-type": "application/json" } })
			: new Response("unavailable", { status: 503 });
	}
	if (target.endsWith(".sha256")) {
		if (scenario.removeCached === "before-read") {
			fs.rmSync(scenario.cachedPath, { force: true });
		}
		switch (scenario.checksum) {
			case "match":
				return new Response(`${binaryHash}  bifrost-http\n`);
			case "mismatch":
				return new Response(`${"0".repeat(64)}  bifrost-http\n`);
			case "malformed":
				return new Response("<html>not a checksum</html>");
			case "unavailable":
				return new Response("unavailable", { status: 503 });
			case "unreachable":
				throw new TypeError("fetch failed");
			case "body-error":
				return new Response(new ReadableStream({ start: (controller) => controller.error(new Error("socket hang up")) }));
			default:
				return new Response("not found", { status: 404 });
		}
	}
	if (target.endsWith("/bifrost-http")) {
		return init.method === "HEAD" ? new Response(null) : new Response(binary);
	}
	return new Response("not found", { status: 404 });
};
