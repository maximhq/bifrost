#!/usr/bin/env node

import { execSync } from "child_process";
import { chmodSync, createWriteStream } from "fs";
import fetch from "node-fetch";
import { tmpdir } from "os";
import { join } from "path";

const BASE_URL = "https://downloads.getmaxim.ai";

// Parse transport version from command line arguments
function parseTransportVersion() {
  const args = process.argv.slice(2);
  let transportVersion = "latest"; // Default to latest
  
  // Find --transport-version argument
  const versionArgIndex = args.findIndex(arg => arg.startsWith("--transport-version"));
  
  if (versionArgIndex !== -1) {
    const versionArg = args[versionArgIndex];
    
    if (versionArg.includes("=")) {
      // Format: --transport-version=v1.2.3
      transportVersion = versionArg.split("=")[1];
    } else if (versionArgIndex + 1 < args.length) {
      // Format: --transport-version v1.2.3
      transportVersion = args[versionArgIndex + 1];
    }
    
    // Remove the transport-version arguments from args array so they don't get passed to the binary
    if (versionArg.includes("=")) {
      args.splice(versionArgIndex, 1);
    } else {
      args.splice(versionArgIndex, 2);
    }
  }
  
  return { version: validateTransportVersion(transportVersion), remainingArgs: args };
}

// Validate transport version format
function validateTransportVersion(version) {
  if (version === "latest") {
    return version;
  }
  
  // Check if version matches v{x.x.x} format
  const versionRegex = /^v\d+\.\d+\.\d+$/;
  if (versionRegex.test(version)) {
    return version;
  }
  
  console.error(`Invalid transport version format: ${version}`);
  console.error(`Transport version must be either "latest" or in format v1.2.3`);
  process.exit(1);
}

const { version: VERSION, remainingArgs } = parseTransportVersion();

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
  const res = await fetch(url);

  if (!res.ok) {
    console.error(`Download failed: ${res.status} ${res.statusText}`);
    process.exit(1);
  }

  const fileStream = createWriteStream(dest);
  await new Promise((resolve, reject) => {
    res.body.pipe(fileStream);
    res.body.on("error", reject);
    fileStream.on("finish", resolve);
  });

  chmodSync(dest, 0o755);
}

(async () => {
  const { platformDir, archDir, binaryName } = getPlatformArchAndBinary();
  const binaryPath = join(tmpdir(), binaryName);

  // The download URL now matches the CI pipeline's S3 structure with arch
  // Example: https://downloads.getmaxim.ai/bifrost/latest/darwin/arm64/bifrost
  const downloadUrl = `${BASE_URL}/bifrost/${VERSION}/${platformDir}/${archDir}/${binaryName}`;

  try {
    await downloadBinary(downloadUrl, binaryPath);
  } catch (error) {
    console.error(`❌ Failed to download binary from ${downloadUrl}:`, error.message);
    process.exit(1);
  }

  // Get command-line arguments to pass to the binary (excluding --transport-version)
  const args = remainingArgs.join(" ");

  // Execute the binary, forwarding the arguments
  try {
    execSync(`${binaryPath} ${args}`, { stdio: "inherit" });
  } catch (error) {
    // The child process will have already printed its error message.
    // Exit with the same status code as the child process.
    process.exit(error.status || 1);
  }
})();
