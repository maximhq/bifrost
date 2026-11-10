import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const binJs = join(here, "..", "bin.js");
const preload = join(here, "mockfetch.cjs");
const MARKER = /BIFROST_TEST_BINARY_RAN/;
// What mockfetch.cjs serves as the release binary.
const GENUINE = "#!/bin/sh\necho BIFROST_TEST_BINARY_RAN\n";
const TAMPERED = "#!/bin/sh\necho BIFROST_TEST_TAMPERED_RAN\n";

// Throwaway paths, removed when the test process exits (t.after needs Node 18.13; engines allows 18.0).
const cleanup = [];
process.on("exit", () => {
	for (const path of cleanup) {
		rmSync(path, { recursive: true, force: true });
	}
});

// Runs bin.js against the mocked download server with a throwaway cache dir. seed, when given,
// is { version, contents }: a binary already in the cache for that version before the run.
function run(scenario, args = [], seed = null) {
	const cache = mkdtempSync(join(tmpdir(), "bifrost-npx-test-"));
	cleanup.push(cache);
	// bin.js picks its cache root per platform (see cacheDir); point every one of them at the
	// throwaway dir so a run never reads or writes the real user cache.
	const cacheEnv = { XDG_CACHE_HOME: cache, HOME: cache, LOCALAPPDATA: cache, USERPROFILE: cache };
	const cacheRoot = process.platform === "darwin" ? join(cache, "Library", "Caches") : cache;
	if (seed) {
		const dir = join(cacheRoot, "bifrost", seed.version, "bin");
		mkdirSync(dir, { recursive: true });
		writeFileSync(join(dir, "bifrost-http-0"), seed.contents);
		chmodSync(join(dir, "bifrost-http-0"), 0o755);
		for (const [name, contents] of Object.entries(seed.extraFiles || {})) {
			writeFileSync(join(dir, name), contents);
		}
		scenario = { ...scenario, cachedPath: join(dir, "bifrost-http-0") };
	}
	const result = spawnSync(process.execPath, ["--require", preload, binJs, ...args], {
		encoding: "utf8",
		env: { ...process.env, ...cacheEnv, BIFROST_TEST_SCENARIO: JSON.stringify(scenario), BIFROST_TRANSPORT_VERSION: "" },
	});
	const cachedFiles = (version) => {
		const dir = join(cacheRoot, "bifrost", version, "bin");
		return existsSync(dir) ? readdirSync(dir) : [];
	};
	const cachedBinary = (version) => {
		const path = join(cacheRoot, "bifrost", version, "bin", "bifrost-http-0");
		return existsSync(path) ? readFileSync(path, "utf8") : null;
	};
	return { ...result, cachedFiles, cachedBinary };
}

const skip = process.platform === "win32" ? "the stub binary is a shell script" : false;

test("runs the binary when the checksum matches", { skip }, () => {
	const r = run({ latest: "v2.4.0", checksum: "match" });
	assert.equal(r.status, 0, r.stderr);
	assert.match(r.stdout, MARKER);
	assert.deepEqual(r.cachedFiles("v2.4.0"), ["bifrost-http-0"]);
});

test("refuses a binary whose checksum does not match", { skip }, () => {
	const r = run({ latest: "v2.4.0", checksum: "mismatch" });
	assert.equal(r.status, 1, r.stdout + r.stderr);
	assert.doesNotMatch(r.stdout, MARKER);
	assert.match(r.stderr, /Checksum verification failed/);
	assert.deepEqual(r.cachedFiles("v2.4.0"), [], "rejected download must not stay in the cache");
});

test("refuses a malformed checksum file", { skip }, () => {
	const r = run({ latest: "v2.4.0", checksum: "malformed" });
	assert.equal(r.status, 1, r.stdout + r.stderr);
	assert.doesNotMatch(r.stdout, MARKER);
	assert.match(r.stderr, /Could not parse checksum file/);
});

test("refuses a missing checksum for a release that must have one", { skip }, () => {
	for (const version of ["v2.4.0", "v2.3.3", "v2.3.3-prerelease1"]) {
		const r = run({ checksum: "missing" }, ["--transport-version", version]);
		assert.equal(r.status, 1, `${version}: ${r.stdout}${r.stderr}`);
		assert.doesNotMatch(r.stdout, MARKER, version);
		assert.match(r.stderr, /Checksum file not available/, version);
		assert.deepEqual(r.cachedFiles(version), [], version);
	}
});

test("refuses a missing checksum when the latest version cannot be resolved", { skip }, () => {
	const r = run({ checksum: "missing" });
	assert.equal(r.status, 1, r.stdout + r.stderr);
	assert.doesNotMatch(r.stdout, MARKER);
	assert.match(r.stderr, /Checksum file not available/);
});

test("refuses a missing checksum when latest resolves to a release older than the checksum cutoff", { skip }, () => {
	// Only a version the user pinned may skip verification; a resolver answer is not a pin.
	const r = run({ latest: "v2.3.2", checksum: "missing" });
	assert.equal(r.status, 1, r.stdout + r.stderr);
	assert.doesNotMatch(r.stdout, MARKER);
	assert.match(r.stderr, /Checksum file not available/);
	assert.deepEqual(r.cachedFiles("v2.3.2"), []);
});

test("ignores a latest version that is not a release tag", { skip }, () => {
	// The resolved tag becomes a cache path segment, so one that climbs out of the cache is not used.
	const escape = `bifrost-npx-escape-${process.pid}-${Date.now()}`;
	cleanup.push(join(tmpdir(), escape));
	const r = run({ latest: `../../${escape}`, checksum: "match" });
	assert.equal(r.status, 0, r.stderr);
	assert.match(r.stdout, MARKER);
	assert.equal(existsSync(join(tmpdir(), escape)), false, "the download escaped the cache directory");
});

test("runs older releases published without a checksum, with a warning", { skip }, () => {
	const r = run({ checksum: "missing" }, ["--transport-version", "v2.3.2"]);
	assert.equal(r.status, 0, r.stderr);
	assert.match(r.stdout, MARKER);
	assert.match(r.stderr, /published without a checksum file/);
});

test("runs a cached binary that still matches its published checksum", { skip }, () => {
	const r = run({ latest: "v2.4.0", checksum: "match" }, [], { version: "v2.4.0", contents: GENUINE });
	assert.equal(r.status, 0, r.stderr);
	assert.match(r.stdout, MARKER);
	assert.doesNotMatch(r.stdout, /Downloaded binary/, "a verified cache hit must not download again");
});

test("replaces a cached binary that does not match its published checksum", { skip }, () => {
	// Covers binaries cached by launcher versions that did not verify downloads.
	const r = run({ latest: "v2.4.0", checksum: "match" }, [], { version: "v2.4.0", contents: TAMPERED });
	assert.equal(r.status, 0, r.stderr);
	assert.doesNotMatch(r.stdout, /BIFROST_TEST_TAMPERED_RAN/, "the unverified cached binary ran");
	assert.match(r.stdout, MARKER);
	assert.equal(r.cachedBinary("v2.4.0"), GENUINE);
});

test("refuses a cached binary when the published checksum does not match the release either", { skip }, () => {
	const r = run({ latest: "v2.4.0", checksum: "mismatch" }, [], { version: "v2.4.0", contents: GENUINE });
	assert.equal(r.status, 1, r.stdout + r.stderr);
	assert.doesNotMatch(r.stdout, MARKER);
	assert.match(r.stderr, /Checksum verification failed/);
	assert.deepEqual(r.cachedFiles("v2.4.0"), []);
});

test("refuses a cached binary when latest resolves to a release older than the checksum cutoff", { skip }, () => {
	const r = run({ latest: "v2.3.2", checksum: "missing" }, [], { version: "v2.3.2", contents: GENUINE });
	assert.equal(r.status, 1, r.stdout + r.stderr);
	assert.doesNotMatch(r.stdout, MARKER);
	assert.match(r.stderr, /Checksum file not available/);
	assert.equal(r.cachedBinary("v2.3.2"), GENUINE, "an unchecked cached binary is kept, not deleted");
});

test("runs a cached pinned older release published without a checksum, with a warning", { skip }, () => {
	const r = run({ checksum: "missing" }, ["--transport-version", "v2.3.2"], { version: "v2.3.2", contents: GENUINE });
	assert.equal(r.status, 0, r.stderr);
	assert.match(r.stdout, MARKER);
	assert.match(r.stderr, /published without a checksum file/);
});

test("keeps a cached binary without running it when its checksum cannot be checked", { skip }, () => {
	// The binary was never compared, so it is not deleted; it runs once its checksum can be verified.
	for (const [checksum, reason] of [
		["unreachable", /Could not fetch checksum file/],
		["unavailable", /Checksum file not available \(503\)/],
		["malformed", /Could not parse checksum file/],
		["body-error", /Could not read checksum file: socket hang up/],
	]) {
		const r = run({ latest: "v2.4.0", checksum }, [], { version: "v2.4.0", contents: GENUINE });
		assert.equal(r.status, 1, `${checksum}: ${r.stdout}${r.stderr}`);
		assert.doesNotMatch(r.stdout, MARKER, checksum);
		assert.match(r.stderr, reason, checksum);
		assert.match(r.stderr, /cached binary was kept/, checksum);
		assert.doesNotMatch(r.stdout, /Downloaded binary/, checksum);
		assert.equal(r.cachedBinary("v2.4.0"), GENUINE, checksum);
	}
});

test("downloads again when another launcher removes the cached binary during the check", { skip }, () => {
	// before-read: gone before it is hashed; after-read: hashed, then gone before this launcher removes it.
	for (const removeCached of ["before-read", "after-read"]) {
		const r = run({ latest: "v2.4.0", checksum: "match", removeCached }, [], { version: "v2.4.0", contents: TAMPERED });
		assert.equal(r.status, 0, `${removeCached}: ${r.stdout}${r.stderr}`);
		assert.doesNotMatch(r.stdout, /BIFROST_TEST_TAMPERED_RAN/, removeCached);
		assert.match(r.stdout, MARKER, removeCached);
		assert.equal(r.cachedBinary("v2.4.0"), GENUINE, removeCached);
	}
});

test("removes downloads abandoned by launchers that are gone, not ones still running", { skip }, () => {
	// A finished child's pid is a launcher that was interrupted mid-download; this process stands in
	// for a concurrent launcher whose download is still in progress.
	const deadPid = spawnSync(process.execPath, ["-e", ""]).pid;
	const abandoned = `bifrost-http-0.download-${deadPid}-1`;
	const inProgress = `bifrost-http-0.download-${process.pid}-1`;
	const r = run({ latest: "v2.4.0", checksum: "match" }, [], {
		version: "v2.4.0",
		contents: GENUINE,
		extraFiles: { [abandoned]: "partial", [inProgress]: "partial" },
	});
	assert.equal(r.status, 0, r.stderr);
	assert.match(r.stdout, MARKER);
	assert.deepEqual(r.cachedFiles("v2.4.0").sort(), ["bifrost-http-0", inProgress].sort());
});

