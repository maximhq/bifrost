#!/usr/bin/env node

import { existsSync, mkdirSync, readFileSync, appendFileSync } from "fs";
import { downloadVerified, getAgreedHash } from "./verify.js";
import { homedir } from "os";
import { dirname, join } from "path";
import { createInterface } from "readline";

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
	}

	return validateCliVersion(cliVersion);
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

const VERSION = parseCliVersion();

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

// Check if a specific version exists on the download server
async function checkVersionExists(version, platformDir, archDir, binaryName) {
	const url = `${BASE_URL}/bifrost-cli/${version}/${platformDir}/${archDir}/${binaryName}`;
	const res = await fetch(url, { method: "HEAD", redirect: "error", signal: AbortSignal.timeout(15000) });
	return res.ok;
}

// Detect the user's shell and return the RC file path and the PATH export line
function getShellConfig() {
	const home = homedir();
	const shell = (process.env.SHELL || "").toLowerCase();

	if (process.platform === "win32") {
		return null; // Windows — manual PATH setup
	}

	if (shell.endsWith("/fish") || shell.endsWith("/fish.exe")) {
		return {
			rcFile: join(home, ".config", "fish", "config.fish"),
			exportLine: "fish_add_path $HOME/.bifrost/bin",
			shellName: "fish",
		};
	}

	if (shell.endsWith("/zsh")) {
		return {
			rcFile: join(home, ".zshrc"),
			exportLine: 'export PATH="$HOME/.bifrost/bin:$PATH"',
			shellName: "zsh",
		};
	}

	if (shell.endsWith("/bash") || shell.endsWith("/bash.exe")) {
		const rcFiles =
			process.platform === "darwin"
				? [join(home, ".bash_profile"), join(home, ".bashrc")]
				: [join(home, ".bashrc")];
		return {
			rcFile: rcFiles[0],
			extraRcFiles: rcFiles.slice(1),
			exportLine: 'export PATH="$HOME/.bifrost/bin:$PATH"',
			shellName: "bash",
		};
	}

	return null;
}

// Check if the PATH export line is already present in the given file
function hasPathLine(filePath) {
	if (!existsSync(filePath)) return false;
	try {
		const content = readFileSync(filePath, "utf-8");
		return content.includes(".bifrost/bin");
	} catch {
		return false;
	}
}

// Prompt the user with a yes/no question
async function promptYesNo(question) {
	if (!process.stdin.isTTY || !process.stdout.isTTY) {
		return false;
	}
	const rl = createInterface({ input: process.stdin, output: process.stdout });
	return new Promise((resolve) => {
		rl.question(question, (answer) => {
			rl.close();
			const normalized = (answer || "").trim().toLowerCase();
			resolve(normalized === "" || normalized === "y" || normalized === "yes");
		});
	});
}

function printStartMessage(message) {
	console.log(`\n${message}`);
	console.log(`Enter 'bifrost' when you're ready to start the CLI.`);
}

async function main() {
	const { platformDir, archDir, binaryName } = getPlatformArchAndBinary();

	let namedVersion;

	if (VERSION === "latest") {
		const response = await fetch(`${BASE_URL}/bifrost-cli/latest/version.txt`, {
			redirect: "error", signal: AbortSignal.timeout(15000),
		});
		if (!response.ok) throw new Error("Could not resolve the latest CLI version");
		namedVersion = (await response.text()).trim();
		if (!/^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(namedVersion)) throw new Error("Invalid latest CLI version");

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

	const downloadUrl = `${BASE_URL}/bifrost-cli/${namedVersion}/${platformDir}/${archDir}/${binaryName}`;

	// Install to ~/.bifrost/bin/
	const installDir = join(homedir(), ".bifrost", "bin");
	mkdirSync(installDir, { recursive: true });
	const binaryPath = join(installDir, binaryName);

	const expected = await getAgreedHash(downloadUrl, `cli/${namedVersion}`, `${platformDir}/${archDir}/${binaryName}`);
	await downloadVerified(downloadUrl, binaryPath, expected);

	console.log(`✅ Installed bifrost to ${binaryPath}`);

	// Shell PATH setup
	const shellConfig = getShellConfig();

	if (!shellConfig) {
		// Windows — print manual instructions
		console.log(`\nTo complete installation, add the following directory to your PATH:`);
		console.log(`  ${installDir}`);
		console.log(`\nYou can do this in System Settings > Environment Variables.`);
		printStartMessage("The installer won't start Bifrost automatically.");
		return;
	}

	// Check if PATH is already configured
	const allRcFiles = [shellConfig.rcFile, ...(shellConfig.extraRcFiles || [])];
	const missingRcFiles = allRcFiles.filter((rcFile) => !hasPathLine(rcFile));

	if (missingRcFiles.length === 0) {
		console.log(`\n✅ PATH already configured for bifrost.`);
		printStartMessage("The installer won't start Bifrost automatically.");
		return;
	}

	// Prompt user to add PATH
	const rcDisplayName = shellConfig.rcFile.startsWith(homedir()) ? shellConfig.rcFile.slice(homedir().length + 1) : shellConfig.rcFile.split("/").pop();
	const shouldAdd = await promptYesNo(`\nAdd bifrost to your PATH in ~/${rcDisplayName}? [Y/n] `);

	if (!shouldAdd) {
		console.log(`\nSkipped PATH setup. You can manually add this to your shell config:`);
		console.log(`  ${shellConfig.exportLine}`);
		printStartMessage("After updating your PATH, start Bifrost manually.");
		return;
	}

	// Append the export line to RC file(s)
	for (const rcFile of missingRcFiles) {
		try {
			mkdirSync(dirname(rcFile), { recursive: true });
			appendFileSync(rcFile, `\n# Added by bifrost installer\n${shellConfig.exportLine}\n`);
			console.log(`✅ Added PATH to ${rcFile}`);
		} catch (err) {
			console.error(`⚠️ Failed to update ${rcFile}: ${err.message}`);
		}
	}

	const rcRelative = shellConfig.rcFile.startsWith(homedir()) ? shellConfig.rcFile.slice(homedir().length + 1) : shellConfig.rcFile.split("/").pop();
	printStartMessage(`Run 'source ~/${rcRelative}' or open a new terminal first.`);
}

main().catch((error) => {
	console.error(`❌ Failed to install Bifrost CLI: ${error.message}`);
	process.exit(1);
});
