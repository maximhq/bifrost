/**
 * Single global setup for all E2E tests.
 * 1. Builds the test plugin into tmp/bifrost-test-plugin.so.
 * 2. Builds and starts the MCP test servers (HTTP/SSE on 3001, auth on 3002,
 *    OAuth on 3003, STDIO test-tools-server), reusing any already listening.
 * Bifrost state (providers, TestClient001, auth) comes from env/config.json and
 * log seeding from scripts/run-e2e.mjs. Returns a teardown that stops the servers.
 */
import {
  execFileSync,
  execSync,
  spawn,
  type ChildProcess,
} from "child_process";
import { existsSync, statSync } from "fs";
import * as http from "http";
import * as net from "net";
import * as os from "os";
import { join, resolve } from "path";
import { setTimeout } from "timers/promises";

const REPO_ROOT = resolve(__dirname, "../..");
const TEST_PLUGIN_PATH = join(REPO_ROOT, "tmp", "bifrost-test-plugin.so");

const MCP_SERVERS: ChildProcess[] = [];
const isWindows = os.platform() === "win32";
const npmCommand = isWindows ? "npm.cmd" : "npm";
const goCommand = isWindows ? "go.exe" : "go";
const httpServerBinaryName = isWindows ? "http-server.exe" : "http-server";
const httpServerExec = isWindows ? "http-server.exe" : "./http-server";

function runCommand(
  command: string,
  args: string[],
  options: { cwd?: string; env?: NodeJS.ProcessEnv } = {},
) {
  execFileSync(command, args, {
    stdio: "inherit",
    ...options,
  });
}

async function checkServerReady(
  port: number,
  maxAttempts = 15,
): Promise<boolean> {
  const hosts = ["127.0.0.1", "localhost", "[::1]"];
  const paths = ["/mcp", "/"];

  const tryInitialize = async (url: string): Promise<boolean> =>
    new Promise((res) => {
      const body = JSON.stringify({
        jsonrpc: "2.0",
        id: 1,
        method: "initialize",
      });
      const req = http.request(
        url,
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "Content-Length": Buffer.byteLength(body),
          },
        },
        (response) => {
          response.on("data", () => {});
          response.on("end", () =>
            res(
              Boolean(
                response.statusCode &&
                response.statusCode >= 200 &&
                response.statusCode < 300,
              ),
            ),
          );
        },
      );
      req.on("error", () => res(false));
      req.setTimeout(1000, () => {
        req.destroy();
        res(false);
      });
      req.write(body);
      req.end();
    });

  for (let i = 0; i < maxAttempts; i++) {
    for (const host of hosts) {
      for (const path of paths) {
        if (await tryInitialize(`http://${host}:${port}${path}`)) return true;
      }
    }
    await setTimeout(1000);
  }
  return false;
}

function isPortListening(port: number): Promise<boolean> {
  return new Promise((res) => {
    const socket = net.connect({ port, host: "127.0.0.1" });
    socket.once("connect", () => {
      socket.destroy();
      res(true);
    });
    socket.once("error", () => res(false));
    socket.setTimeout(1000, () => {
      socket.destroy();
      res(false);
    });
  });
}

async function runPluginSetup(): Promise<void> {
  console.log("Setting up test plugin for E2E tests...");
  if (existsSync(TEST_PLUGIN_PATH)) {
    console.log(`✓ Test plugin already exists at ${TEST_PLUGIN_PATH}`);
    return;
  }
  try {
    console.log("Running make build-test-plugin from repo root...");
    execSync("make build-test-plugin", { cwd: REPO_ROOT, stdio: "inherit" });
    if (existsSync(TEST_PLUGIN_PATH)) {
      console.log(`✓ Test plugin ready at ${TEST_PLUGIN_PATH}`);
    } else {
      throw new Error(
        `Plugin build reported success but file not found at ${TEST_PLUGIN_PATH}`,
      );
    }
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : String(error);
    console.error(`\n⚠️  Failed to build test plugin: ${errorMsg}`);
    console.error("\nBuild manually from repo root: make build-test-plugin\n");
    // Don't throw - allow tests to run and fail gracefully if plugin is missing
  }
}

async function startHttpServer(
  httpServerDir: string,
  httpServerBinary: string,
): Promise<void> {
  console.log("Starting HTTP/SSE server on port 3001...");
  if (!existsSync(httpServerBinary)) {
    throw new Error(`HTTP server binary not found at ${httpServerBinary}`);
  }

  const httpServer = spawn(httpServerExec, [], {
    cwd: httpServerDir,
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });

  let serverOutput = "";
  httpServer.stdout?.on("data", (data) => {
    const output = data.toString();
    serverOutput += output;
    console.log(`[HTTP Server] ${output.trim()}`);
  });
  httpServer.stderr?.on("data", (data) => {
    const output = data.toString();
    serverOutput += output;
    console.error(`[HTTP Server Error] ${output.trim()}`);
  });
  httpServer.on("exit", (code, signal) => {
    if (code !== null && code !== 0) {
      console.error(`HTTP server exited with code ${code}, signal ${signal}`);
      console.error(`Server output: ${serverOutput}`);
    }
  });
  httpServer.on("error", (err) => {
    console.error(`Failed to spawn HTTP server: ${err.message}`);
  });

  if (!httpServer.pid) {
    throw new Error("Failed to start HTTP server - no PID assigned");
  }
  console.log(`HTTP server started with PID: ${httpServer.pid}`);
  httpServer.unref();
  MCP_SERVERS.push(httpServer);

  await setTimeout(2000);
  console.log("Waiting for HTTP/SSE server to be ready...");
  const isReady = await checkServerReady(3001, 20);
  if (!isReady) {
    if (httpServer.pid) {
      try {
        process.kill(httpServer.pid, "SIGTERM");
      } catch (e) {
        console.error(`Failed to kill server: ${e}`);
      }
    }
    throw new Error(
      `HTTP server failed to start on port 3001 after 20 attempts. Server output: ${serverOutput || "No output captured"}`,
    );
  }

  await setTimeout(1000);
  const stillReady = await checkServerReady(3001, 2);
  if (!stillReady) {
    throw new Error("HTTP server started but then stopped immediately");
  }
  console.log("✓ HTTP/SSE server is ready on http://localhost:3001/");
}

async function runMCPSetup(): Promise<void> {
  console.log("Setting up MCP test servers...");

  const httpServerDir = join(
    REPO_ROOT,
    "examples",
    "mcps",
    "http-no-ping-server",
  );
  const httpServerBinary = join(httpServerDir, httpServerBinaryName);

  if (!existsSync(httpServerBinary)) {
    console.log("Building HTTP/SSE server...");
    runCommand(goCommand, ["build", "-o", httpServerBinaryName, "main.go"], {
      cwd: httpServerDir,
      env: { ...process.env, CGO_ENABLED: "0" },
    });
  } else {
    console.log("✓ HTTP/SSE server binary already exists");
  }

  if (await checkServerReady(3001, 1)) {
    console.log("✓ HTTP/SSE server already listening on 3001, reusing it");
  } else {
    await startHttpServer(httpServerDir, httpServerBinary);
  }

  const stdioServerDir = join(
    REPO_ROOT,
    "examples",
    "mcps",
    "test-tools-server",
  );
  const stdioServerDist = join(stdioServerDir, "dist", "index.js");
  if (!existsSync(stdioServerDist)) {
    console.log("Building STDIO server...");
    runCommand(npmCommand, ["install"], { cwd: stdioServerDir });
    runCommand(npmCommand, ["run", "build"], { cwd: stdioServerDir });
  } else {
    console.log("✓ STDIO server already built");
  }

  // Build and start auth-demo-server on port 3002
  try {
    const authServerBinaryName = isWindows
      ? "auth-demo-server.exe"
      : "auth-demo-server";
    const authServerDir = join(
      REPO_ROOT,
      "examples",
      "mcps",
      "auth-demo-server",
    );
    const authServerBinary = join(authServerDir, authServerBinaryName);
    const authServerExec = isWindows
      ? authServerBinaryName
      : "./auth-demo-server";

    if (!existsSync(authServerBinary)) {
      console.log("Building auth-demo-server...");
      runCommand(goCommand, ["build", "-o", authServerBinaryName, "main.go"], {
        cwd: authServerDir,
        env: { ...process.env, CGO_ENABLED: "0" },
      });
    } else {
      console.log("✓ auth-demo-server binary already exists");
    }

    if (await isPortListening(3002)) {
      console.log(
        "✓ port 3002 already in use, assuming auth-demo-server is running",
      );
    } else {
      console.log("Starting auth-demo-server on port 3002...");
      const authServer = spawn(authServerExec, [], {
        cwd: authServerDir,
        detached: true,
        stdio: ["ignore", "pipe", "pipe"],
      });
      authServer.stdout?.on("data", (data) =>
        console.log(`[Auth Server] ${data.toString().trim()}`),
      );
      authServer.stderr?.on("data", (data) =>
        console.error(`[Auth Server Error] ${data.toString().trim()}`),
      );
      if (authServer.pid) {
        authServer.unref();
        MCP_SERVERS.push(authServer);
        await setTimeout(1000);
        console.log("✓ auth-demo-server started on http://localhost:3002/");
      }
    }
  } catch (err) {
    console.warn(
      `⚠️  Failed to start auth-demo-server (header auth tests may skip): ${(err as Error).message}`,
    );
  }

  // Build and start oauth-demo-server on port 3003
  try {
    const oauthServerBinaryName = isWindows
      ? "oauth-demo-server.exe"
      : "oauth-demo-server";
    const oauthServerDir = join(
      REPO_ROOT,
      "examples",
      "mcps",
      "oauth-demo-server",
    );
    const oauthServerBinary = join(oauthServerDir, oauthServerBinaryName);
    const oauthServerExec = isWindows
      ? oauthServerBinaryName
      : "./oauth-demo-server";

    // Rebuild when main.go changed: the OAuth specs select elements of the page it serves.
    const oauthServerStale =
      !existsSync(oauthServerBinary) ||
      statSync(join(oauthServerDir, "main.go")).mtimeMs >
        statSync(oauthServerBinary).mtimeMs;
    if (oauthServerStale) {
      console.log("Building oauth-demo-server...");
      runCommand(goCommand, ["build", "-o", oauthServerBinaryName, "main.go"], {
        cwd: oauthServerDir,
        env: { ...process.env, CGO_ENABLED: "0" },
      });
    } else {
      console.log("✓ oauth-demo-server binary already exists");
    }

    if (await isPortListening(3003)) {
      console.log(
        "✓ port 3003 already in use, assuming oauth-demo-server is running",
      );
    } else {
      console.log("Starting oauth-demo-server on port 3003...");
      const oauthServer = spawn(oauthServerExec, [], {
        cwd: oauthServerDir,
        detached: true,
        stdio: ["ignore", "pipe", "pipe"],
      });
      oauthServer.stdout?.on("data", (data) =>
        console.log(`[OAuth Server] ${data.toString().trim()}`),
      );
      oauthServer.stderr?.on("data", (data) =>
        console.error(`[OAuth Server Error] ${data.toString().trim()}`),
      );
      if (oauthServer.pid) {
        oauthServer.unref();
        MCP_SERVERS.push(oauthServer);
        await setTimeout(1000);
        console.log("✓ oauth-demo-server started on http://localhost:3003/");
      }
    }
  } catch (err) {
    console.warn(
      `⚠️  Failed to start oauth-demo-server (OAuth tests may fail): ${(err as Error).message}`,
    );
  }

  console.log("✓ MCP servers ready");
  console.log("  - HTTP/SSE server: http://localhost:3001/");
  console.log("  - Auth demo server: http://localhost:3002/");
  console.log("  - OAuth demo server: http://localhost:3003/");
  console.log("  - STDIO server: test-tools-server/dist/index.js");
}

function runMCPTeardown(): void {
  console.log("Tearing down MCP test servers...");
  MCP_SERVERS.forEach((server, index) => {
    try {
      if (server.pid && !server.killed) {
        try {
          process.kill(-server.pid, "SIGTERM");
          console.log(`✓ Stopped MCP server ${index + 1} (PID: ${server.pid})`);
        } catch {
          server.kill("SIGTERM");
        }
      } else if (!server.killed) {
        server.kill("SIGTERM");
        console.log(`✓ Stopped MCP server ${index + 1}`);
      }
    } catch (error) {
      console.error(`Failed to stop MCP server ${index + 1}:`, error);
    }
  });
}

async function globalSetup(): Promise<() => Promise<void>> {
  await runPluginSetup();
  try {
    await runMCPSetup();
  } catch (error: unknown) {
    const err = error as Error;
    console.error(
      `\n❌ Failed to setup MCP servers: ${err?.message || String(error)}`,
    );
    console.error("\nTo setup manually:");
    console.error(
      "  cd examples/mcps/http-no-ping-server && go build -o http-server main.go && ./http-server &",
    );
    console.error(
      "  cd examples/mcps/test-tools-server && npm install && npm run build",
    );
    runMCPTeardown();
    throw error;
  }
  return async () => {
    runMCPTeardown();
  };
}

export default globalSetup;
