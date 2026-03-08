#!/usr/bin/env node

import { createHash } from "crypto";
import { execFileSync } from "child_process";
import { chmodSync, createWriteStream, existsSync, fsyncSync, mkdirSync, readFileSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { Readable } from "stream";

const BASE_URL = "https://downloads.getmaxim.ai";

// Parse CLI version from command line arguments
function parseCliVersion() {
	const args = process.argv.slice(2);
	let cliVersion = "latest"; // Default to latest

	// Find --cli-version argument
	const versionArgIndex = args.findIndex((arg) => arg.startsWith("--cli-version"));

	if (versionArgIndex !== -1) {
		const versionArg = args[versionArgIndex];

		if (versionArg.includes("=")) {
			// Format: --cli-version=v1.2.3
			cliVersion = versionArg.split("=")[1];
			if (!cliVersion) {
				console.error("--cli-version requires a value");
				process.exit(1);
			}
		} else if (versionArgIndex + 1 < args.length) {
			// Format: --cli-version v1.2.3
			cliVersion = args[versionArgIndex + 1];
		} else {
			console.error("--cli-version requires a value");
			process.exit(1);
		}

		// Remove the cli-version arguments from args array so they don't get passed to the binary
		if (versionArg.includes("=")) {
			args.splice(versionArgIndex, 1);
		} else {
			args.splice(versionArgIndex, 2);
		}
	}

	return { version: validateCliVersion(cliVersion), remainingArgs: args };
}

// Validate CLI version format
function validateCliVersion(version) {
	if (version === "latest") {
		return version;
	}

	// Check if version matches v{x.x.x} format
	const versionRegex = /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;
	if (versionRegex.test(version)) {
		return version;
	}

	console.error(`Invalid CLI version format: ${version}`);
	console.error(`CLI version must be either "latest", "v1.2.3", or "v1.2.3-prerelease1"`);
	process.exit(1);
}

const { version: VERSION, remainingArgs } = parseCliVersion();

function getPlatformArchAndBinary() {
	const platform = process.platform;
	const arch = process.arch;

	let platformDir;
	let archDir;
	let binaryName;

	if (platform === "darwin") {
		platformDir = "darwin";
		if (arch === "arm64") archDir = "arm64";
		else archDir = "amd64";
		binaryName = "bifrost";
	} else if (platform === "linux") {
		platformDir = "linux";
		if (arch === "x64") archDir = "amd64";
		else if (arch === "ia32") archDir = "386";
		else archDir = arch; // fallback
		binaryName = "bifrost";
	} else if (platform === "win32") {
		platformDir = "windows";
		if (arch === "x64") archDir = "amd64";
		else if (arch === "ia32") archDir = "386";
		else archDir = arch; // fallback
		binaryName = "bifrost.exe";
	} else {
		console.error(`Unsupported platform/arch: ${platform}/${arch}`);
		process.exit(1);
	}

	return { platformDir, archDir, binaryName };
}

async function downloadBinary(url, dest) {
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

// Check if a specific version exists on the download server
async function checkVersionExists(version, platformDir, archDir, binaryName) {
	const url = `${BASE_URL}/bifrost-cli/${version}/${platformDir}/${archDir}/${binaryName}`;
	const res = await fetch(url, { method: "HEAD" });
	return res.ok;
}

// Verify the downloaded binary against its SHA-256 checksum
async function verifyChecksum(binaryPath, checksumUrl) {
	const res = await fetch(checksumUrl);
	if (!res.ok) {
		console.warn(`⚠️ Checksum file not available (${res.status}), skipping verification`);
		return;
	}

	const checksumContent = (await res.text()).trim();
	// Format: "<hash>  <filename>" (shasum output)
	const expectedHash = checksumContent.split(/\s+/)[0];
	if (!expectedHash) {
		console.warn("⚠️ Could not parse checksum file, skipping verification");
		return;
	}

	const fileBuffer = readFileSync(binaryPath);
	const actualHash = createHash("sha256").update(fileBuffer).digest("hex");

	if (actualHash !== expectedHash) {
		const { unlinkSync } = await import("fs");
		unlinkSync(binaryPath);
		console.error(`❌ Checksum verification failed!`);
		console.error(`   Expected: ${expectedHash}`);
		console.error(`   Got:      ${actualHash}`);
		console.error(`   The downloaded binary has been deleted for safety.`);
		process.exit(1);
	}

	console.log("✅ Checksum verified");
}

function formatBytes(bytes) {
	if (bytes === 0) return "0 B";
	const k = 1024;
	const sizes = ["B", "KB", "MB", "GB"];
	const i = Math.floor(Math.log(bytes) / Math.log(k));
	return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + " " + sizes[i];
}

async function main() {
	const { platformDir, archDir, binaryName } = getPlatformArchAndBinary();

	let namedVersion;

	if (VERSION === "latest") {
		// For "latest", check if the latest path exists on the server
		const latestExists = await checkVersionExists("latest", platformDir, archDir, binaryName);
		if (latestExists) {
			namedVersion = "latest";
		} else {
			console.error(`❌ Could not find latest CLI version.`);
			console.error(`Please specify a version with --cli-version v1.0.0`);
			process.exit(1);
		}
	} else {
		// For explicitly specified versions, verify it exists on the server
		const versionExists = await checkVersionExists(VERSION, platformDir, archDir, binaryName);
		if (!versionExists) {
			console.error(`❌ CLI version '${VERSION}' not found.`);
			console.error(`Please verify the version exists at: ${BASE_URL}/bifrost-cli/`);
			process.exit(1);
		}
		namedVersion = VERSION;
	}

	const downloadUrl = `${BASE_URL}/bifrost-cli/${VERSION}/${platformDir}/${archDir}/${binaryName}`;

	// Use cache for named versions, tmpdir for latest
	const useCache = namedVersion !== "latest";
	const bifrostBinDir = useCache ? join(cacheDir(), "bifrost-cli", namedVersion, "bin") : tmpdir();

	// For non-cached downloads, use a unique filename to avoid race conditions
	const uniqueSuffix = useCache ? "" : `-${process.pid}-${Date.now()}`;

	// If the binary directory doesn't exist, create it
	try {
		if (useCache && !existsSync(bifrostBinDir)) {
			mkdirSync(bifrostBinDir, { recursive: true });
		}
	} catch (mkdirError) {
		console.error(`❌ Failed to create directory ${bifrostBinDir}:`, mkdirError.message);
		process.exit(1);
	}

	const binaryPath = join(bifrostBinDir, `${binaryName}${uniqueSuffix}`);

	if (!useCache || !existsSync(binaryPath)) {
		await downloadBinary(downloadUrl, binaryPath);
		console.log(`✅ Downloaded CLI binary to ${binaryPath}`);

		// Verify checksum of the downloaded binary
		const checksumUrl = `${BASE_URL}/bifrost-cli/${VERSION}/${platformDir}/${archDir}/${binaryName}.sha256`;
		await verifyChecksum(binaryPath, checksumUrl);
	}

	// Execute the CLI binary
	try {
		execFileSync(binaryPath, remainingArgs, { stdio: "inherit" });
	} catch (execError) {
		if (execError.status !== undefined) {
			process.exit(execError.status);
		}

		console.error(`❌ Failed to start Bifrost CLI. Error:`, execError.message);

		if (execError.code) {
			console.error(`Error code: ${execError.code}`);
		}
		if (execError.errno) {
			console.error(`System error: ${execError.errno}`);
		}
		if (execError.signal) {
			console.error(`Signal: ${execError.signal}`);
		}

		// For specific Linux issues, show diagnostic info
		if (process.platform === "linux" && (execError.code === "ENOENT" || execError.code === "ETXTBSY")) {
			console.error(`\n💡 This appears to be a Linux compatibility issue.`);
			console.error(`   The binary may be incompatible with your Linux distribution.`);
		}

		process.exit(execError.status || 1);
	}
}

main().catch((error) => {
	console.error(`❌ Failed to bootstrap Bifrost CLI: ${error.message}`);
	process.exit(1);
});
