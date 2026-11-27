// Local workload identity federation fixture for the folder "Workload identity federation against a
// local token endpoint (workload identity fixture)". It plays each provider's token endpoint and API
// on loopback, so the folder runs with no live call, no key and no federation rule. The logic, and
// what a case can ask of it, is in lib/wif-fixture.mjs.
//
//   node tests/e2e/api/runners/wif-fixture.mjs [--port 8800] [--dir tmp/wif-fixture]
//
// On start it writes the identity token files the gateway config reads: <dir>/tokens/*.jwt, the
// directory to allow with BIFROST_OIDC_ALLOWED_CREDENTIAL_DIRS, and <dir>/outside/outside.jwt, which
// must stay outside it. See tests/e2e/api/README.md, "Workload identity federation".

import { mkdirSync, writeFileSync } from "node:fs";
import http from "node:http";
import path from "node:path";
import { pathToFileURL } from "node:url";

import { createWifFixture, encodeIdentityToken, identityClaims } from "./lib/wif-fixture.mjs";

const argValue = (name, fallback) => {
	const i = process.argv.indexOf(name);
	return i !== -1 && process.argv[i + 1] ? process.argv[i + 1] : fallback;
};

// SEED_TOKEN_FILES are the files provider_config/workload-identity.config.json names. The short
// lifetimes let the rotation and single-use cases see a refresh within seconds.
export const SEED_TOKEN_FILES = [
	{ name: "config-file.jwt", sub: "config-file" },
	{ name: "rotating.jwt", sub: "rotating", fx: { expires_in: 6 } },
	{ name: "single-use.jwt", sub: "single-use", jti: true, fx: { expires_in: 6 } },
];

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
	const port = Number(argValue("--port", process.env.WIF_FIXTURE_PORT || "8800"));
	const dir = path.resolve(argValue("--dir", "tmp/wif-fixture"));
	const tokenDir = path.join(dir, "tokens");
	const outsideDir = path.join(dir, "outside");

	const fixture = createWifFixture({ tokenDir });
	for (const seed of SEED_TOKEN_FILES) fixture.writeTokenFile(seed);
	mkdirSync(outsideDir, { recursive: true });
	writeFileSync(path.join(outsideDir, "outside.jwt"), `${encodeIdentityToken(identityClaims({ sub: "outside" }))}\n`, { mode: 0o600 });

	const server = http.createServer(fixture.handle);
	server.listen(port, "127.0.0.1", () => {
		console.log(`wif-fixture listening on http://127.0.0.1:${port} (profiles under /<provider>, control under /__wif)`);
		console.log(`token files: ${tokenDir} (allow it with BIFROST_OIDC_ALLOWED_CREDENTIAL_DIRS); outside the allowlist: ${outsideDir}`);
	});
	for (const signal of ["SIGINT", "SIGTERM"]) {
		process.on(signal, () => server.close(() => process.exit(0)));
	}
}
