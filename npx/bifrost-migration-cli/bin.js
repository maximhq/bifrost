#!/usr/bin/env node

import { execFileSync } from "child_process";
import { createHash } from "crypto";
import {
    chmodSync,
    createWriteStream,
    existsSync,
    fsyncSync,
    mkdirSync,
    readFileSync,
    renameSync,
    unlinkSync,
} from "fs";
import { homedir } from "os";
import { join } from "path";
import { Readable } from "stream";

const BASE_URL = "https://downloads.getmaxim.ai";
const REQUEST_TIMEOUT_MS = 30_000; // HEAD checks / checksum fetch
const DOWNLOAD_TIMEOUT_MS = 300_000; // binary download (also covers a stalled body)

// Parse migration-cli version from command line arguments
function parseMigrationCliVersion() {
    const args = process.argv.slice(2);
    let migrationCliVersion = "latest"; // Default to latest

    const versionArgIndex = args.findIndex(
        (arg) => arg === "--cli-version" || arg.startsWith("--cli-version="),
    );

    if (versionArgIndex !== -1) {
        const versionArg = args[versionArgIndex];

        if (versionArg.includes("=")) {
            // Format: --cli-version=v1.2.3
            migrationCliVersion = versionArg.split("=")[1];
            if (!migrationCliVersion) {
                console.error("--cli-version requires a value");
                process.exit(1);
            }
            args.splice(versionArgIndex, 1);
        } else if (versionArgIndex + 1 < args.length) {
            // Format: --cli-version v1.2.3
            migrationCliVersion = args[versionArgIndex + 1];
            args.splice(versionArgIndex, 2);
        } else {
            console.error("--cli-version requires a value");
            process.exit(1);
        }
    }

    return {
        version: validateMigrationCliVersion(migrationCliVersion),
        remainingArgs: args,
    };
}

// Validate migration-cli version format
function validateMigrationCliVersion(version) {
    if (version === "latest") {
        return version;
    }

    // Check if version matches v{x.x.x} format
    const versionRegex = /^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;
    if (versionRegex.test(version)) {
        return version;
    }

    console.error(`Invalid migration-cli version format: ${version}`);
    console.error(
        `migration-cli version must be either "latest", "v1.2.3", or "v1.2.3-prerelease1"`,
    );
    process.exit(1);
}

const { version: VERSION, remainingArgs } = parseMigrationCliVersion();

async function getPlatformArchAndBinary() {
    const platform = process.platform;
    const arch = process.arch;

    let platformDir;
    let archDir;
    let binaryName;

    if (platform === "darwin") {
        platformDir = "darwin";
        archDir = arch === "arm64" ? "arm64" : "amd64";
        binaryName = "bifrost-migration-cli";
    } else if (platform === "linux") {
        platformDir = "linux";
        if (arch === "x64") archDir = "amd64";
        else if (arch === "ia32") archDir = "386";
        else archDir = arch; // fallback
        binaryName = "bifrost-migration-cli";
    } else if (platform === "win32") {
        platformDir = "windows";
        if (arch === "x64") archDir = "amd64";
        else if (arch === "ia32") archDir = "386";
        else archDir = arch; // fallback
        binaryName = "bifrost-migration-cli.exe";
    } else {
        console.error(`Unsupported platform/arch: ${platform}/${arch}`);
        process.exit(1);
    }

    return { platformDir, archDir, binaryName };
}

async function downloadBinary(url, dest) {
    const res = await fetch(url, { signal: AbortSignal.timeout(DOWNLOAD_TIMEOUT_MS) });

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
            const nodeStream = Readable.fromWeb(res.body);

            nodeStream.on("data", (chunk) => {
                downloadedSize += chunk.length;
                if (totalSize) {
                    const progress = ((downloadedSize / totalSize) * 100).toFixed(1);
                    process.stdout.write(
                        `\r⏱️ Downloading Binary: ${progress}% (${formatBytes(downloadedSize)}/${formatBytes(totalSize)})`,
                    );
                } else {
                    process.stdout.write(
                        `\r⏱️ Downloaded: ${formatBytes(downloadedSize)}`,
                    );
                }
            });

            nodeStream.pipe(fileStream);
            fileStream.on("finish", () => {
                process.stdout.write("\n");

                try {
                    fsyncSync(fileStream.fd);
                } catch (syncError) {
                    // fsync might fail on some systems, ignore
                }
            });
            fileStream.on("close", resolve)
            fileStream.on("error", reject);
            nodeStream.on("error", reject);
        } catch (error) {
            reject(error);
        }
    });

    chmodSync(dest, 0o755);
}

// Check if a specific version exists on the download server
async function checkVersionExists(version, platformDir, archDir, binaryName) {
    const url = `${BASE_URL}/bifrost-migration-cli/${version}/${platformDir}/${archDir}/${binaryName}`;
    const res = await fetch(url, {
        method: "HEAD",
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
    });
    return res.ok;
}

// Verify the downloaded binary against its SHA-256 checksum
async function verifyChecksum(binaryPath, checksumUrl) {
    const res = await fetch(checksumUrl, { signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
    if (!res.ok) {
        unlinkSync(binaryPath);
        console.error(`❌ Checksum file not available (${res.status}). Refusing to run an unverified binary.`);
        process.exit(1);
    }

    const checksumContent = (await res.text()).trim();
    // Format: "<hash>  <filename>" (shasum output)
    const expectedHash = checksumContent.split(/\s+/)[0];
    if (!expectedHash) {
        unlinkSync(binaryPath);
        console.error("❌ Could not parse checksum file. Refusing to run an unverified binary.");
        process.exit(1);
    }

    const fileBuffer = readFileSync(binaryPath);
    const actualHash = createHash("sha256").update(fileBuffer).digest("hex");

    if (actualHash !== expectedHash) {
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
    const { platformDir, archDir, binaryName } = await getPlatformArchAndBinary();

    let namedVersion;

    if (VERSION === "latest") {
        // For "latest", check if the latest path exists on the server
        const latestExists = await checkVersionExists(
            "latest",
            platformDir,
            archDir,
            binaryName,
        );
        if (latestExists) {
            namedVersion = "latest";
        } else {
            console.error(`❌ Could not find latest bifrost-migration-cli version.`);
            console.error(`Please specify a version with --cli-version v1.0.0`);
            process.exit(1);
        }
    } else {
        const versionExists = await checkVersionExists(
            VERSION,
            platformDir,
            archDir,
            binaryName,
        );
        if (!versionExists) {
            console.error(`❌ bifrost-migration-cli version '${VERSION}' not found.`);
            console.error(
                `See https://docs.getbifrost.ai/changelogs for release versions you can pass to --cli-version.`,
            );
            process.exit(1);
        }
        namedVersion = VERSION;
    }

    const downloadUrl = `${BASE_URL}/bifrost-migration-cli/${namedVersion}/${platformDir}/${archDir}/${binaryName}`;

    // Pinned versions are immutable, so they're safe to cache under
    // ~/.bifrost/versions/<tag>/. "latest" is a moving target — never cache
    // it, always install fresh to ~/.bifrost/bin/ so it can't get stuck stale.
    const isPinned = namedVersion !== "latest";
    const installDir = isPinned
        ? join(homedir(), ".bifrost", "versions", namedVersion)
        : join(homedir(), ".bifrost", "bin");
    mkdirSync(installDir, { recursive: true });
    const binaryPath = join(installDir, binaryName);

    if (!isPinned || !existsSync(binaryPath)) {
        const tempBinaryPath = `${binaryPath}.download-${process.pid}-${Date.now()}`;
        try {
            await downloadBinary(downloadUrl, tempBinaryPath);

            const checksumUrl = `${downloadUrl}.sha256`;
            await verifyChecksum(tempBinaryPath, checksumUrl);
            renameSync(tempBinaryPath, binaryPath);
        } catch (err) {
            try { unlinkSync(tempBinaryPath); } catch { }
            throw err;
        }
        console.log(`✅ Downloaded binary to ${binaryPath}`);
    }

    try {
        execFileSync(binaryPath, remainingArgs, { stdio: "inherit" });
    } catch (execError) {
        if (execError.status != null) {
            // Child ran and exited non-zero; it already printed its own error to
            // stderr via inherited stdio. Just forward the exit code.
            process.exit(execError.status);
        }
        console.error(
            `❌ Failed to run bifrost-migration-cli. Error:`,
            execError.message,
        );
        if (execError.code) {
            console.error(`Error code: ${execError.code}`);
        }
        process.exit(1);
    }
}

main().catch((error) => {
    if (error.name === "AbortError" || error.name === "TimeoutError") {
        console.error(`❌ Network request timed out: ${error.message}`);
    } else {
        console.error(`❌ Failed to run bifrost-migration-cli installer: ${error.message}`);
    }
    process.exit(1);
});