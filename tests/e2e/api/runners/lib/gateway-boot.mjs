// Boots a throwaway bifrost-http for one log-store configuration, the node twin of
// lib/clean-bifrost.sh: a fresh app dir, no providers, logging on, inference auth off, the setup
// token in the environment, and whatever logs_store (sqlite, postgres, with or without object
// storage) the caller names. The store is read at boot only, so every store configuration needs a
// server of its own.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import net from "node:net";
import { spawn } from "node:child_process";

export const DEFAULT_SETUP_TOKEN = "bifrost-e2e-setup-token";

export function freePort() {
	return new Promise((resolve, reject) => {
		const srv = net.createServer();
		srv.once("error", reject);
		srv.listen(0, "127.0.0.1", () => {
			const { port } = srv.address();
			srv.close(() => resolve(port));
		});
	});
}

// gatewayConfig builds the config.json for one boot. logsStore is the logs_store block minus
// "enabled"; client overrides merge over the clean defaults.
export function gatewayConfig({ dir, logsStore, client = {} }) {
	return {
		$schema: "https://www.getbifrost.ai/schema",
		client: {
			drop_excess_requests: false,
			initial_pool_size: 50,
			allowed_origins: ["*"],
			enable_logging: true,
			enforce_auth_on_inference: false,
			max_request_body_size_mb: 200,
			...client,
		},
		config_store: { enabled: true, type: "sqlite", config: { path: path.join(dir, "config.db") } },
		logs_store: { enabled: true, ...logsStore },
	};
}

// bootGateway starts binary with the config and resolves once it reports it has started. It
// resolves to { baseURL, dir, port, logPath, stop(keepDir) }.
export async function bootGateway({ binary, logsStore, client, env = {}, timeoutMs = 120000 }) {
	if (!binary || !fs.existsSync(binary)) throw new Error(`gateway binary not found: ${binary}`);
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), "bifrost-e2e-"));
	const port = await freePort();
	const config = gatewayConfig({ dir, logsStore: typeof logsStore === "function" ? logsStore(dir) : logsStore, client });
	fs.writeFileSync(path.join(dir, "config.json"), JSON.stringify(config, null, 2));
	const logPath = path.join(dir, "server.log");
	const out = fs.openSync(logPath, "a");
	const child = spawn(binary, ["--app-dir", dir, "--port", String(port), "--log-level", "info"], {
		stdio: ["ignore", out, out],
		env: { ...process.env, BIFROST_SETUP_TOKEN: process.env.BIFROST_SETUP_TOKEN || DEFAULT_SETUP_TOKEN, BIFROST_DISABLE_PROFILER: "1", ...env },
	});
	let exited = null;
	child.once("exit", (code) => {
		exited = code ?? -1;
	});
	const started = Date.now();
	for (;;) {
		if (exited !== null) throw new Error(`bifrost exited (${exited}) before it was ready:\n${tail(logPath)}`);
		if (fs.readFileSync(logPath, "utf8").includes("successfully started bifrost")) break;
		if (Date.now() - started > timeoutMs) {
			child.kill("SIGKILL");
			throw new Error(`bifrost did not start within ${timeoutMs / 1000}s:\n${tail(logPath)}`);
		}
		await new Promise((r) => setTimeout(r, 300));
	}
	async function stop(keepDir = false) {
		if (exited === null) {
			child.kill("SIGTERM");
			const deadline = Date.now() + 35000;
			while (exited === null && Date.now() < deadline) await new Promise((r) => setTimeout(r, 200));
			if (exited === null) child.kill("SIGKILL");
		}
		fs.closeSync(out);
		if (!keepDir) fs.rmSync(dir, { recursive: true, force: true });
	}
	return { baseURL: `http://127.0.0.1:${port}`, dir, port, logPath, config, stop };
}

export function tail(file, lines = 40) {
	try {
		return fs.readFileSync(file, "utf8").trimEnd().split("\n").slice(-lines).join("\n");
	} catch {
		return "";
	}
}
