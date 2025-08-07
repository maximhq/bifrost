#!/usr/bin/env node

import { execFileSync } from "child_process";
import { chmodSync, createWriteStream, existsSync, fsyncSync } from "fs";
import { tmpdir } from "os";
import { join } from "path";
import { Readable } from "stream";

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

  const contentLength = res.headers.get('content-length');
  const totalSize = contentLength ? parseInt(contentLength, 10) : null;
  let downloadedSize = 0;
    
  const fileStream = createWriteStream(dest, { flags: "w" });
  await new Promise((resolve, reject) => {
    try {
      // Convert the fetch response body to a Node.js readable stream
      const nodeStream = Readable.fromWeb(res.body);
      
      // Add progress tracking
      nodeStream.on('data', (chunk) => {
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
        process.stdout.write('\n');
        
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

function formatBytes(bytes) {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return parseFloat((bytes / Math.pow(k, i)).toFixed(1)) + ' ' + sizes[i];
}

(async () => {
  const platformInfo = await getPlatformArchAndBinary();
  const { platformDir, archDir, binaryName } = platformInfo;

  // For future use when we want to add multiple fallback binaries
  const downloadUrls = [];
  
  downloadUrls.push(`${BASE_URL}/bifrost/${VERSION}/${platformDir}/${archDir}/${binaryName}`);

  let lastError = null;
  let binaryWorking = false;

  for (let i = 0; i < downloadUrls.length; i++) {
    const downloadUrl = downloadUrls[i];
    // Use unique file path for each attempt to avoid ETXTBSY
    const binaryPath = join(tmpdir(), `${binaryName}-${i}`);
    
    try {
      await downloadBinary(downloadUrl, binaryPath);
      
      // Verify the binary is executable before trying to run it
      if (!existsSync(binaryPath)) {
        throw new Error(`Binary not found at: ${binaryPath}`);
      }

      // Add a small delay to ensure file is fully written and not busy
      await new Promise(resolve => setTimeout(resolve, 100));

      // Test if the binary can execute
      try {
        execFileSync(binaryPath, remainingArgs, { stdio: "inherit" });
        binaryWorking = true;
        break;
      } catch (execError) {
        // If execution fails (ENOENT, ETXTBSY, etc.), try next binary
        lastError = execError;
        continue;
      }
    } catch (downloadError) {
      lastError = downloadError;
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
    if (process.platform === 'linux' && (lastError.code === 'ENOENT' || lastError.code === 'ETXTBSY')) {
      console.error(`\n💡 This appears to be a Linux compatibility issue.`);
      console.error(`   The binary may be incompatible with your Linux distribution.`);
    }
    
    process.exit(lastError.status || 1);
  }
})();
