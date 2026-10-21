#!/usr/bin/env node

import { execFileSync } from "child_process";
import { existsSync, mkdirSync, unlinkSync } from "fs";
import { downloadVerified, getAgreedHash, verifyFile } from "./verify.js";
import { join } from "path";

const BASE_URL = "https://downloads.getmaxim.ai";

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
async function getLatestVersion() {
	const releaseUrl = "https://getbifrost.ai/latest-release";
	const res = await fetch(releaseUrl, { redirect: "error", signal: AbortSignal.timeout(15000) });
	if (!res.ok) {
		throw new Error("Could not resolve the latest release version");
	}
	const data = await res.json();
	if (typeof data.name !== "string" || !/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(data.name)) throw new Error("Invalid latest release version");
	return data.name;
}

// Check if a specific version exists on the download server
async function checkVersionExists(version, platformDir, archDir, binaryName) {
	const url = `${BASE_URL}/bifrost/${version}/${platformDir}/${archDir}/${binaryName}`;
	const res = await fetch(url, { method: "HEAD", redirect: "error", signal: AbortSignal.timeout(15000) });
	return res.ok;
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

	const downloadUrls = [`${BASE_URL}/bifrost/${namedVersion}/${platformDir}/${archDir}/${binaryName}`];
	let lastError = null;
	let binaryWorking = false;
	const bifrostBinDir = join(cacheDir(), "bifrost", namedVersion, "bin");
	mkdirSync(bifrostBinDir, { recursive: true });

	for (let i = 0; i < downloadUrls.length; i++) {
		const binaryPath = join(bifrostBinDir, `${binaryName}-${i}`);

		const expected = await getAgreedHash(downloadUrls[i], `transports/${namedVersion}`, `${platformDir}/${archDir}/${binaryName}`);
		if (existsSync(binaryPath)) {
			// Older launchers wrote straight to this path and could leave a truncated
			// file behind. Replace a cache entry that fails verification instead of
			// failing on it forever; the fresh download is verified before it runs.
			try {
				await verifyFile(binaryPath, expected);
			} catch {
				console.error(`Cached binary at ${binaryPath} failed verification; downloading again`);
				unlinkSync(binaryPath);
			}
		}
		if (!existsSync(binaryPath)) {
			await downloadVerified(downloadUrls[i], binaryPath, expected);
		}
		// A cache hit is not evidence of integrity. Verify again before execution.
		await verifyFile(binaryPath, expected);

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
})().catch((error) => {
	console.error(`Failed to start Bifrost: ${error.message}`);
	process.exit(1);
});
