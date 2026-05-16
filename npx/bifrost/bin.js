#!/usr/bin/env node

import { execFileSync } from "child_process";
import { chmodSync, createWriteStream, existsSync, fsyncSync, mkdirSync, mkdtempSync, rmSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { Readable } from "stream";

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
	const res = await fetch(releaseUrl);
	if (!res.ok) {
		return null;
	}
	const data = await res.json();
	return data.name;
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

	for (let i = 0; i < downloadUrls.length; i++) {
		const binaryPath = join(bifrostBinDir, `${binaryName}-${i}`);

		if (!namedVersionFound || !existsSync(binaryPath)) {
			await downloadBinary(downloadUrls[i], binaryPath);
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
