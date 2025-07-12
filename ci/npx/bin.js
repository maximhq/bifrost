#!/usr/bin/env node

import { createWriteStream, chmodSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { execSync } from "child_process";
import fetch from "node-fetch";

const BASE_URL = "https://your.cdn.net";
const VERSION = "latest"; // Or dynamically resolve if needed

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
  // Example: https://your.cdn.net/bifrost/latest/darwin/arm64/bifrost
  const downloadUrl = `${BASE_URL}/bifrost/${VERSION}/${platformDir}/${archDir}/${binaryName}`;

  await downloadBinary(downloadUrl, binaryPath);

  // Get command-line arguments to pass to the binary
  const args = process.argv.slice(2).join(" ");

  // Execute the binary, forwarding the arguments
  try {
    execSync(`${binaryPath} ${args}`, { stdio: "inherit" });
  } catch (error) {
    // The child process will have already printed its error message.
    // Exit with the same status code as the child process.
    process.exit(error.status || 1);
  }
})();
