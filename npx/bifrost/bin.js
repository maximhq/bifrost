#!/usr/bin/env node

import { execFileSync } from "child_process";
import { createHash } from "crypto";
import {
	chmodSync,
	createWriteStream,
	existsSync,
	fsyncSync,
	mkdirSync,
	mkdtempSync,
	readdirSync,
	readFileSync,
	renameSync,
	rmSync,
	unlinkSync,
} from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { Readable } from "stream";

const BASE_URL = "https://downloads.getmaxim.ai";
const REQUEST_TIMEOUT_MS = 30_000; // checksum fetch

// First release whose binaries ship with a .sha256 file. Older releases were
// published without one, so a missing checksum is only tolerated below this.
const CHECKSUM_REQUIRED_SINCE = "v2.3.3";

function isTransportVersionFlag(arg) {
	const eq = arg.indexOf("=");
	return (eq === -1 ? arg : arg.slice(0, eq)) === "--transport-version";
}

// Keep in sync with transports/bifrost-http/main.go (flag.StringVar names).
const BIFROST_HTTP_KNOWN_FLAGS = new Set(["app-dir", "host", "log-level", "log-style", "port"]);

// Go's flag package registers -help / -h (and commonly -version); not declared in main.go.
const GO_FLAG_PACKAGE_ALIASES = new Set(["help", "h", "version"]);

function bifrostHttpFlagToken(arg) {
	if (!arg.startsWith("-") || arg === "-") {
		return null;
	}
	const stripped = arg.replace(/^-+/, "");
	if (!stripped) {
		return null;
	}
	const eq = stripped.indexOf("=");
	return eq === -1 ? stripped : stripped.slice(0, eq);
}

function validateRemainingArgsForBifrostHttp(args) {
	let afterDoubleDash = false;
	for (const arg of args) {
		if (arg === "--") {
			afterDoubleDash = true;
			continue;
		}
		if (afterDoubleDash) {
			continue;
		}
		if (!arg.startsWith("-")) {
			continue;
		}
		const token = bifrostHttpFlagToken(arg);
		if (!token) {
			continue;
		}
		if (BIFROST_HTTP_KNOWN_FLAGS.has(token)) {
			continue;
		}
		if (GO_FLAG_PACKAGE_ALIASES.has(token)) {
			continue;
		}
		// Some builds link test helpers that register extra flags (e.g. -testify.m).
		if (token.startsWith("testify.")) {
			continue;
		}
		// Hyphenated names are treated as likely real bifrost-http flags even before this wrapper is updated.
		if (token.includes("-")) {
			continue;
		}
		// Linked Go test/runtime flags use -name=value; the token is only the name part, so inspect `arg`.
		if (arg.includes("=")) {
			continue;
		}
		// Numeric-only token avoids mis-parsing odd argv like -123 as a bogus flag name.
		if (/^\d+$/.test(token)) {
			continue;
		}
		console.error(`❌ Unknown argument: ${arg}`);
		const knownList = [...BIFROST_HTTP_KNOWN_FLAGS].sort().map((f) => `-${f}`).join(" ");
		console.error(`Known bifrost-http flags: ${knownList}`);
		console.error(`Use --help or -h for gateway usage.`);
		console.error(
			`Pin a release from this wrapper with: --transport-version <tag> (see https://docs.getbifrost.ai/changelogs).`,
		);
		process.exit(1);
	}
}

// Parse transport version from command line arguments
function parseTransportVersion() {
	const args = process.argv.slice(2);
	// Some runners invoke the bin as `node bin.js bifrost ...`; strip one prefix so flags parse reliably.
	if (args.length > 0 && args[0] === "bifrost") {
		args.shift();
	}

	let transportVersion = "latest"; // Default to latest
	const envOverride = process.env.BIFROST_TRANSPORT_VERSION?.trim();

	// Only wrapper-owned flags before "--"; everything after "--" is forwarded to bifrost-http verbatim.
	const passthroughIdx = args.indexOf("--");
	const argsForTransportFlag = passthroughIdx === -1 ? args : args.slice(0, passthroughIdx);
	const versionArgIndex = argsForTransportFlag.findIndex(isTransportVersionFlag);

	if (versionArgIndex !== -1) {
		const versionArg = args[versionArgIndex];

		if (versionArg.includes("=")) {
			// Format: --transport-version=v1.2.3
			transportVersion = versionArg.split("=")[1] ?? "";
			if (!transportVersion) {
				console.error("--transport-version requires a value");
				process.exit(1);
			}
		} else if (versionArgIndex + 1 < args.length) {
			// Format: --transport-version v1.2.3
			const next = args[versionArgIndex + 1];
			if (next.startsWith("-")) {
				console.error("--transport-version requires a value");
				process.exit(1);
			}
			transportVersion = next;
		} else {
			console.error("--transport-version requires a value");
			process.exit(1);
		}

		// Remove the transport-version arguments from args array so they don't get passed to the binary
		if (versionArg.includes("=")) {
			args.splice(versionArgIndex, 1);
		} else {
			args.splice(versionArgIndex, 2);
		}
	} else if (envOverride) {
		// Fallback when flags are not forwarded to the script (some npx / CI wrappers)
		transportVersion = envOverride;
	}

	return { version: validateTransportVersion(transportVersion), remainingArgs: args };
}

// Validate transport version format
function validateTransportVersion(version) {
	if (version === "latest") {
		return version;
	}

	// Check if version matches v{x.x.x} format
	const versionRegex = /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;
	if (versionRegex.test(version)) {
		return version;
	}

	console.error(`Invalid transport version format: ${version}`);
	console.error(`Transport version must be either "latest", "v1.2.3", or "v1.2.3-prerelease1"`);
	process.exit(1);
}

const { version: VERSION, remainingArgs } = parseTransportVersion();
validateRemainingArgsForBifrostHttp(remainingArgs);

async function getPlatformArchAndBinary() {
	const platform = process.platform;
	const arch = process.arch;

	let platformDir;
	let archDir;
	let binaryName;

	if (platform === "darwin") {
		platformDir = "darwin";
		if (arch === "arm64") archDir = "arm64";
		else archDir = "amd64";
		binaryName = "bifrost-http";
	} else if (platform === "linux") {
		platformDir = "linux";
		if (arch === "x64") archDir = "amd64";
		else if (arch === "ia32") archDir = "386";
		else archDir = arch; // fallback
		binaryName = "bifrost-http";
	} else if (platform === "win32") {
		platformDir = "windows";
		if (arch === "x64") archDir = "amd64";
		else if (arch === "ia32") archDir = "386";
		else archDir = arch; // fallback
		binaryName = "bifrost-http.exe";
	} else {
		console.error(`Unsupported platform/arch: ${platform}/${arch}`);
		process.exit(1);
	}

	return { platformDir, archDir, binaryName };
}

async function downloadBinary(url, dest) {
	// console.log(`🔄 Downloading binary from ${url}...`);

	const res = await fetch(url);

	if (!res.ok) {
		console.error(`❌ Download failed: ${res.status} ${res.statusText}`);
		process.exit(1);
	}

	const contentLength = res.headers.get("content-length");
	const totalSize = contentLength ? parseInt(contentLength, 10) : null;
	let downloadedSize = 0;

	const fileStream = createWriteStream(dest, { flags: "w" });
	await new Promise((resolve, reject) => {
		try {
			// Convert the fetch response body to a Node.js readable stream
			const nodeStream = Readable.fromWeb(res.body);

			// Add progress tracking
			nodeStream.on("data", (chunk) => {
				downloadedSize += chunk.length;
				if (totalSize) {
					const progress = ((downloadedSize / totalSize) * 100).toFixed(1);
					process.stdout.write(`\r⏱️ Downloading Binary: ${progress}% (${formatBytes(downloadedSize)}/${formatBytes(totalSize)})`);
				} else {
					process.stdout.write(`\r⏱️ Downloaded: ${formatBytes(downloadedSize)}`);
				}
			});

			nodeStream.pipe(fileStream);
			fileStream.on("finish", () => {
				process.stdout.write("\n");

				// Ensure file is fully written to disk
				try {
					fsyncSync(fileStream.fd);
				} catch (syncError) {
					// fsync might fail on some systems, ignore
				}

				resolve();
			});
			fileStream.on("error", reject);
			nodeStream.on("error", reject);
		} catch (error) {
			reject(error);
		}
	});

	chmodSync(dest, 0o755);
}

// Parses "v1.2.3" / "v1.2.3-pre" into [major, minor, patch]; null when unparseable.
function parseCoreVersion(version) {
	const match = /^v(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?$/.exec(version || "");
	return match ? match.slice(1, 4).map(Number) : null;
}

// A checksum is mandatory for unresolved versions ("latest") and for any release
// at or above CHECKSUM_REQUIRED_SINCE, prereleases of that line included.
function isChecksumRequired(version) {
	const current = parseCoreVersion(version);
	if (!current) {
		return true;
	}
	const since = parseCoreVersion(CHECKSUM_REQUIRED_SINCE);
	for (let i = 0; i < 3; i++) {
		if (current[i] !== since[i]) {
			return current[i] > since[i];
		}
	}
	return true;
}

function refuseBinary(binaryPath, ...messages) {
	try {
		unlinkSync(binaryPath);
	} catch {
		// already gone
	}
	for (const message of messages) {
		console.error(message);
	}
	process.exit(1);
}

// Checks binaryPath against its published SHA-256 checksum. Returns null when it may run (verified, or
// an older release published without one), otherwise { messages, rejected }. rejected is true when the
// file itself failed (wrong hash, or gone), false when no usable checksum was fetched to compare it with.
async function checksumProblem(binaryPath, checksumUrl, version) {
	const unchecked = (message) => ({ messages: [message], rejected: false });
	let res;
	try {
		res = await fetch(checksumUrl, { signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
	} catch (error) {
		return unchecked(`❌ Could not fetch checksum file: ${error.message}. Refusing to run an unverified binary.`);
	}

	if (res.status === 404 && !isChecksumRequired(version)) {
		console.warn(`⚠️ Release ${version} was published without a checksum file; skipping verification.`);
		return null;
	}
	if (!res.ok) {
		return unchecked(`❌ Checksum file not available (${res.status}). Refusing to run an unverified binary.`);
	}

	// Format: "<hash>  <filename>" (shasum output)
	let body;
	try {
		body = await res.text();
	} catch (error) {
		return unchecked(`❌ Could not read checksum file: ${error.message}. Refusing to run an unverified binary.`);
	}
	const expectedHash = body.trim().split(/\s+/)[0].toLowerCase();
	if (!/^[0-9a-f]{64}$/.test(expectedHash)) {
		return unchecked("❌ Could not parse checksum file. Refusing to run an unverified binary.");
	}

	let contents;
	try {
		contents = readFileSync(binaryPath);
	} catch (error) {
		// Another launcher removed it while this one was fetching the checksum.
		if (error.code === "ENOENT") {
			return { messages: [`❌ ${binaryPath} disappeared before it could be verified.`], rejected: true };
		}
		throw error;
	}
	const actualHash = createHash("sha256").update(contents).digest("hex");
	if (actualHash !== expectedHash) {
		return {
			messages: [
				"❌ Checksum verification failed!",
				`   Expected: ${expectedHash}`,
				`   Got:      ${actualHash}`,
				"   The downloaded binary has been deleted for safety.",
			],
			rejected: true,
		};
	}
	return null;
}

function processAlive(pid) {
	try {
		process.kill(pid, 0);
		return true;
	} catch (error) {
		return error.code === "EPERM";
	}
}

// Removes temp downloads (see the download loop) left in dir by launchers that were interrupted:
// Ctrl+C, a kill or a crash. The pid in the name keeps a concurrent launcher's download in progress.
function removeAbandonedDownloads(dir, binaryName) {
	let names;
	try {
		names = readdirSync(dir);
	} catch {
		return;
	}
	for (const name of names) {
		const match = /\.download-(\d+)-\d+$/.exec(name);
		if (!match || !name.startsWith(`${binaryName}-`)) {
			continue;
		}
		const pid = Number(match[1]);
		// pid 0 would signal this process group, so only a positive pid is checked.
		if (!(pid > 0) || processAlive(pid)) {
			continue;
		}
		try {
			unlinkSync(join(dir, name));
		} catch {
			// another launcher removed it first
		}
	}
}

// Verify the downloaded binary against its published SHA-256 checksum, deleting it and exiting if it fails
async function verifyChecksum(binaryPath, checksumUrl, version) {
	const problem = await checksumProblem(binaryPath, checksumUrl, version);
	if (problem) {
		refuseBinary(binaryPath, ...problem.messages);
	}
}

// Returns the os cache directory path for storing binaries
// Linux: $XDG_CACHE_HOME or ~/.cache
// macOS: ~/Library/Caches
// Windows: %LOCALAPPDATA% or %USERPROFILE%\AppData\Local
function cacheDir() {
	if (process.platform === "linux") {
		return process.env.XDG_CACHE_HOME || join(process.env.HOME || "", ".cache");
	}
	if (process.platform === "darwin") {
		return join(process.env.HOME || "", "Library", "Caches");
	}
	if (process.platform === "win32") {
		return process.env.LOCALAPPDATA || join(process.env.USERPROFILE || "", "AppData", "Local");
	}
	console.error(`Unsupported platform/arch: ${process.platform}/${process.arch}`);
	process.exit(1);
}

// gets the latest version number for transport
// Returns the latest release tag, or null when it cannot be resolved. The tag becomes a cache path
// segment and a URL segment, so anything that is not a release tag ("v1.2.3", "v1.2.3-pre") is
// treated as unresolved.
async function getLatestVersion() {
	const releaseUrl = "https://getbifrost.ai/latest-release";
	const res = await fetch(releaseUrl);
	if (!res.ok) {
		return null;
	}
	let data;
	try {
		data = await res.json();
	} catch {
		return null;
	}
	const name = data && data.name;
	return typeof name === "string" && parseCoreVersion(name) ? name : null;
}

// Check if a specific version exists on the download server
async function checkVersionExists(version, platformDir, archDir, binaryName) {
	const url = `${BASE_URL}/bifrost/${version}/${platformDir}/${archDir}/${binaryName}`;
	const res = await fetch(url, { method: "HEAD" });
	return res.ok;
}

function formatBytes(bytes) {
	if (bytes === 0) return "0 B";
	const k = 1024;
	const sizes = ["B", "KB", "MB", "GB"];
	const i = Math.floor(Math.log(bytes) / Math.log(k));
	return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
}

(async () => {
	const platformInfo = await getPlatformArchAndBinary();
	const { platformDir, archDir, binaryName } = platformInfo;

	let namedVersion;

	if (VERSION === "latest") {
		// For "latest", fetch the latest version from the API
		namedVersion = await getLatestVersion();
	} else {
		// For explicitly specified versions, verify it exists on the server
		const versionExists = await checkVersionExists(VERSION, platformDir, archDir, binaryName);
		if (!versionExists) {
			console.error(`❌ Transport version '${VERSION}' not found.`);
			console.error(`See https://docs.getbifrost.ai/changelogs for release versions you can pass to --transport-version.`);
			process.exit(1);
		}
		namedVersion = VERSION;
	}

	// Check if we got a valid version for namedVersion
	// If namedVersion is null, there is no way to get the latest version
	// In that case, we proceed without caching
	const namedVersionFound = !!namedVersion;

	// For future use when we want to add multiple fallback binaries
	const downloadUrls = [];

	// Use the same path segment as the on-disk cache dir (resolved tag), not the synthetic "latest"
	// string. Otherwise the cached file and URL can disagree and version switches look "stuck".
	const urlVersion = namedVersionFound ? namedVersion : VERSION;
	downloadUrls.push(`${BASE_URL}/bifrost/${urlVersion}/${platformDir}/${archDir}/${binaryName}`);

	let lastError = null;
	let binaryWorking = false;

	const bifrostBinDir = namedVersionFound
		? join(cacheDir(), "bifrost", namedVersion, "bin")
		: mkdtempSync(join(tmpdir(), "bifrost-npx-"));

	if (!namedVersionFound) {
		process.once("exit", () => {
			try {
				if (existsSync(bifrostBinDir)) {
					rmSync(bifrostBinDir, { recursive: true, force: true });
				}
			} catch {
				// best-effort cleanup of ephemeral download dir
			}
		});
	}

	// if the binary directory doesn't exist, create it
	try {
		if (namedVersionFound && !existsSync(bifrostBinDir)) {
			mkdirSync(bifrostBinDir, { recursive: true });
		}
	} catch (mkdirError) {
		console.error(`❌ Failed to create directory ${bifrostBinDir}:`, mkdirError.message);
		process.exit(1);
	}
	if (namedVersionFound) {
		removeAbandonedDownloads(bifrostBinDir, binaryName);
	}

	for (let i = 0; i < downloadUrls.length; i++) {
		const binaryPath = join(bifrostBinDir, `${binaryName}-${i}`);
		const checksumUrl = `${downloadUrls[i]}.sha256`;
		// Only a version the user pinned may fall under the legacy no-checksum exception; a
		// resolved "latest" always needs one, whatever tag the resolver answered with.
		const checksumVersion = VERSION === "latest" ? VERSION : urlVersion;

		// A cached binary is held to the same checksum policy as a fresh download: launcher versions
		// before checksums cached whatever they downloaded. One that fails is downloaded again; one
		// whose checksum cannot be fetched is kept but not run. A concurrent launcher may remove the
		// file at any point here, which is a cache miss.
		let cached = namedVersionFound && existsSync(binaryPath);
		const problem = cached ? await checksumProblem(binaryPath, checksumUrl, checksumVersion) : null;
		if (problem && !problem.rejected) {
			for (const message of problem.messages) {
				console.error(message);
			}
			console.error(`   The cached binary was kept: ${binaryPath}`);
			process.exit(1);
		}
		if (problem) {
			console.warn(`⚠️ Cached binary ${binaryPath} could not be verified; downloading it again.`);
			try {
				unlinkSync(binaryPath);
			} catch (unlinkError) {
				if (unlinkError.code !== "ENOENT") {
					console.error(`❌ Failed to remove unverified cached binary ${binaryPath}:`, unlinkError.message);
					process.exit(1);
				}
			}
			cached = false;
		}

		if (!cached) {
			// Download next to the final path and only move it into place once verified,
			// so an interrupted or rejected download never lands in the cache.
			const tempBinaryPath = `${binaryPath}.download-${process.pid}-${Date.now()}`;
			try {
				await downloadBinary(downloadUrls[i], tempBinaryPath);
				await verifyChecksum(tempBinaryPath, checksumUrl, checksumVersion);
				renameSync(tempBinaryPath, binaryPath);
			} catch (err) {
				try {
					unlinkSync(tempBinaryPath);
				} catch {
					// already gone
				}
				throw err;
			}
			console.log(`✅ Downloaded binary to ${binaryPath}`);

			// Add a small delay to ensure file is fully written and not busy
			await new Promise((resolve) => setTimeout(resolve, 100));
		}

		// Test if the binary can execute
		try {
			execFileSync(binaryPath, remainingArgs, { stdio: "inherit" });
			binaryWorking = true;
			break;
		} catch (execError) {
			// If execution fails (ENOENT, ETXTBSY, etc.), try next binary
			lastError = execError;
			continue;
			// Continue to next URL silently
		}
	}

	if (!binaryWorking) {
		console.error(`❌ Failed to start Bifrost. Error:`, lastError.message);

		// Show critical error details for troubleshooting
		if (lastError.code) {
			console.error(`Error code: ${lastError.code}`);
		}
		if (lastError.errno) {
			console.error(`System error: ${lastError.errno}`);
		}
		if (lastError.signal) {
			console.error(`Signal: ${lastError.signal}`);
		}

		// For specific Linux issues, show diagnostic info
		if (process.platform === "linux" && (lastError.code === "ENOENT" || lastError.code === "ETXTBSY")) {
			console.error(`\n💡 This appears to be a Linux compatibility issue.`);
			console.error(`   The binary may be incompatible with your Linux distribution.`);
		}

		process.exit(lastError.status || 1);
	}
})();
