/**
 * Single global setup for all E2E tests.
 * 1. Builds test plugin (plugins) and copies to /tmp.
 * 2. Builds and starts MCP test servers (HTTP/SSE on 3001, STDIO test-tools-server).
 * 3. Ensures TestClient001 MCP client exists and is connected (create or reconnect as needed).
 * 4. Sends a POST /v1/responses request to validate the proxy with MCP.
 * Returns a teardown function that stops MCP servers.
 */
import { execFileSync, execSync, spawn, type ChildProcess } from 'child_process'
import { existsSync } from 'fs'
import * as http from 'http'
import * as os from 'os'
import { join, resolve } from 'path'
import { setTimeout } from 'timers/promises'

const TEST_MCP_CLIENT_NAME = 'TestClient001'
const BIFROST_BASE_URL = process.env.BIFROST_BASE_URL ?? 'http://localhost:8080'

const REPO_ROOT = resolve(__dirname, '../..')
const TEST_PLUGIN_PATH = join(REPO_ROOT, 'tmp', 'bifrost-test-plugin.so')

const MCP_SERVERS: ChildProcess[] = []
const isWindows = os.platform() === 'win32'
const npmCommand = isWindows ? 'npm.cmd' : 'npm'
const goCommand = isWindows ? 'go.exe' : 'go'
const   httpServerBinaryName = isWindows ? 'http-server.exe' : 'http-server'
const httpServerExec = isWindows ? 'http-server.exe' : './http-server'

function runCommand(command: string, args: string[], options: { cwd?: string; env?: NodeJS.ProcessEnv } = {}) {
  execFileSync(command, args, {
    stdio: 'inherit',
    ...options,
  })
}

async function checkServerReady(port: number, maxAttempts = 15): Promise<boolean> {
  const hosts = ['127.0.0.1', 'localhost', '[::1]']
  const paths = ['/mcp', '/']

  const tryInitialize = async (url: string): Promise<boolean> =>
    new Promise((res) => {
      const body = JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'initialize' })
      const req = http.request(
        url,
        {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            'Content-Length': Buffer.byteLength(body),
          },
        },
        (response) => {
          response.on('data', () => {})
          response.on('end', () => res(Boolean(response.statusCode && response.statusCode >= 200 && response.statusCode < 300)))
        }
      )
      req.on('error', () => res(false))
      req.setTimeout(1000, () => {
        req.destroy()
        res(false)
      })
      req.write(body)
      req.end()
    })

  for (let i = 0; i < maxAttempts; i++) {
    for (const host of hosts) {
      for (const path of paths) {
        if (await tryInitialize(`http://${host}:${port}${path}`)) return true
      }
    }
    await setTimeout(1000)
  }
  return false
}

interface HttpResult {
  statusCode: number
  body: string
}

function httpRequest(
  baseUrl: string,
  method: string,
  path: string,
  options: { body?: string; headers?: Record<string, string> } = {}
): Promise<HttpResult> {
  const u = new URL(baseUrl)
  const port = u.port ? parseInt(u.port, 10) : (u.protocol === 'https:' ? 443 : 80)
  const body = options.body ?? ''
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...options.headers,
  }
  if (body && !headers['Content-Length']) {
    headers['Content-Length'] = String(Buffer.byteLength(body))
  }
  return new Promise((resolve, reject) => {
    const req = http.request(
      {
        hostname: u.hostname,
        port,
        path,
        method,
        headers,
      },
      (res) => {
        const chunks: Buffer[] = []
        res.on('data', (chunk) => chunks.push(chunk))
        res.on('end', () => resolve({ statusCode: res.statusCode ?? 0, body: Buffer.concat(chunks).toString() }))
      }
    )
    req.on('error', reject)
    req.setTimeout(15000, () => {
      req.destroy()
      reject(new Error('request timeout'))
    })
    if (body) req.write(body)
    req.end()
  })
}

async function waitForBifrostAPI(baseUrl: string, maxAttempts = 30): Promise<void> {
  for (let i = 0; i < maxAttempts; i++) {
    try {
      const r = await httpRequest(baseUrl, 'GET', '/health')
      if (r.statusCode >= 200 && r.statusCode < 300) return
    } catch {
      // ignore
    }
    await setTimeout(1000)
  }
  throw new Error(`Bifrost API at ${baseUrl} did not become ready after ${maxAttempts} attempts`)
}

interface MCPClientItem {
  config: { name: string; client_id: string }
  state: string
}

async function ensureTestClient001AndSendResponses(baseUrl: string): Promise<void> {
  const clientsRes = await httpRequest(baseUrl, 'GET', '/api/mcp/clients')
  if (clientsRes.statusCode !== 200) {
    throw new Error(`GET /api/mcp/clients failed: ${clientsRes.statusCode} ${clientsRes.body}`)
  }
  let clients: MCPClientItem[]
  try {
    clients = JSON.parse(clientsRes.body) as MCPClientItem[]
  } catch {
    throw new Error('Invalid JSON from GET /api/mcp/clients')
  }
  const existing = clients.find((c) => c.config?.name === TEST_MCP_CLIENT_NAME)
  let clientId: string

  if (!existing) {
    console.log(`Creating MCP client "${TEST_MCP_CLIENT_NAME}" via POST /api/mcp/client...`)
    const createBody = JSON.stringify({
      name: TEST_MCP_CLIENT_NAME,
      is_code_mode_client: false,
      is_ping_available: false,
      connection_type: 'http',
      connection_string: { value: 'http://localhost:3001/', env_var: '', from_env: false },
      auth_type: 'none',
      tools_to_execute: ['*'],
      tools_to_auto_execute: ['*'],
    })
    const createRes = await httpRequest(baseUrl, 'POST', '/api/mcp/client', { body: createBody })
    if (createRes.statusCode < 200 || createRes.statusCode >= 300) {
      throw new Error(`POST /api/mcp/client failed: ${createRes.statusCode} ${createRes.body}`)
    }
  }

  const listResAfter = await httpRequest(baseUrl, 'GET', '/api/mcp/clients')
  if (listResAfter.statusCode !== 200) {
    throw new Error(`GET /api/mcp/clients failed after create: ${listResAfter.statusCode} ${listResAfter.body}`)
  }
  const listAfter = JSON.parse(listResAfter.body) as MCPClientItem[]
  const clientAfter = listAfter.find((c) => c.config?.name === TEST_MCP_CLIENT_NAME)
  if (!clientAfter) {
    throw new Error(`MCP client "${TEST_MCP_CLIENT_NAME}" not found after create.`)
  }
  clientId = clientAfter.config.client_id
  if (clientAfter.state !== 'connected') {
    console.log(`MCP client "${TEST_MCP_CLIENT_NAME}" not connected; reloading via POST /api/mcp/client/${clientId}/reconnect...`)
    const reconnectRes = await httpRequest(baseUrl, 'POST', `/api/mcp/client/${encodeURIComponent(clientId)}/reconnect`)
    if (reconnectRes.statusCode < 200 || reconnectRes.statusCode >= 300) {
      throw new Error(
        `POST /api/mcp/client/.../reconnect failed: ${reconnectRes.statusCode} ${reconnectRes.body}. Ensure MCP server is running and reload Bifrost if needed.`
      )
    }
  }

  const listRes2 = await httpRequest(baseUrl, 'GET', '/api/mcp/clients')
  if (listRes2.statusCode !== 200) {
    throw new Error(`GET /api/mcp/clients failed after reconnect: ${listRes2.statusCode} ${listRes2.body}`)
  }
  const list2 = (JSON.parse(listRes2.body) as MCPClientItem[]).filter((c) => c.config?.name === TEST_MCP_CLIENT_NAME)
  const client = list2[0]
  if (!client || client.state !== 'connected') {
    throw new Error(
      `MCP client "${TEST_MCP_CLIENT_NAME}" is not connected after create/reconnect. Reload the MCP server and ensure it is running, then re-run global setup.`
    )
  }
  console.log(`✓ MCP client "${TEST_MCP_CLIENT_NAME}" is connected`)  
}

async function runPluginSetup(): Promise<void> {
  console.log('Setting up test plugin for E2E tests...')
  if (existsSync(TEST_PLUGIN_PATH)) {
    console.log(`✓ Test plugin already exists at ${TEST_PLUGIN_PATH}`)
    return
  }
  try {
    console.log('Running make build-test-plugin from repo root...')
    execSync('make build-test-plugin', { cwd: REPO_ROOT, stdio: 'inherit' })
    if (existsSync(TEST_PLUGIN_PATH)) {
      console.log(`✓ Test plugin ready at ${TEST_PLUGIN_PATH}`)
    } else {
      throw new Error(`Plugin build reported success but file not found at ${TEST_PLUGIN_PATH}`)
    }
  } catch (error: unknown) {
    const errorMsg = error instanceof Error ? error.message : String(error)
    console.error(`\n⚠️  Failed to build test plugin: ${errorMsg}`)
    console.error('\nBuild manually from repo root: make build-test-plugin\n')
    // Don't throw - allow tests to run and fail gracefully if plugin is missing
  }
}

async function runMCPSetup(): Promise<void> {
  console.log('Setting up MCP test servers...')

  const httpServerDir = join(REPO_ROOT, 'examples', 'mcps', 'http-no-ping-server')
  const httpServerBinary = join(httpServerDir, httpServerBinaryName)

  if (!existsSync(httpServerBinary)) {
    console.log('Building HTTP/SSE server...')
    runCommand(goCommand, ['build', '-o', httpServerBinaryName, 'main.go'], {
      cwd: httpServerDir,
      env: { ...process.env, CGO_ENABLED: '0' },
    })
  } else {
    console.log('✓ HTTP/SSE server binary already exists')
  }

  console.log('Starting HTTP/SSE server on port 3001...')
  if (!existsSync(httpServerBinary)) {
    throw new Error(`HTTP server binary not found at ${httpServerBinary}`)
  }

  const httpServer = spawn(httpServerExec, [], {
    cwd: httpServerDir,
    detached: true,
    stdio: ['ignore', 'pipe', 'pipe'],
  })

  let serverOutput = ''
  httpServer.stdout?.on('data', (data) => {
    const output = data.toString()
    serverOutput += output
    console.log(`[HTTP Server] ${output.trim()}`)
  })
  httpServer.stderr?.on('data', (data) => {
    const output = data.toString()
    serverOutput += output
    console.error(`[HTTP Server Error] ${output.trim()}`)
  })
  httpServer.on('exit', (code, signal) => {
    if (code !== null && code !== 0) {
      console.error(`HTTP server exited with code ${code}, signal ${signal}`)
      console.error(`Server output: ${serverOutput}`)
    }
  })
  httpServer.on('error', (err) => {
    console.error(`Failed to spawn HTTP server: ${err.message}`)
  })

  if (!httpServer.pid) {
    throw new Error('Failed to start HTTP server - no PID assigned')
  }
  console.log(`HTTP server started with PID: ${httpServer.pid}`)
  httpServer.unref()
  MCP_SERVERS.push(httpServer)

  await setTimeout(2000)
  console.log('Waiting for HTTP/SSE server to be ready...')
  const isReady = await checkServerReady(3001, 20)
  if (!isReady) {
    if (httpServer.pid) {
      try {
        process.kill(httpServer.pid, 'SIGTERM')
      } catch (e) {
        console.error(`Failed to kill server: ${e}`)
      }
    }
    throw new Error(`HTTP server failed to start on port 3001 after 20 attempts. Server output: ${serverOutput || 'No output captured'}`)
  }

  await setTimeout(1000)
  const stillReady = await checkServerReady(3001, 2)
  if (!stillReady) {
    throw new Error('HTTP server started but then stopped immediately')
  }
  console.log('✓ HTTP/SSE server is ready on http://localhost:3001/')

  const stdioServerDir = join(REPO_ROOT, 'examples', 'mcps', 'test-tools-server')
  const stdioServerDist = join(stdioServerDir, 'dist', 'index.js')
  if (!existsSync(stdioServerDist)) {
    console.log('Building STDIO server...')
    runCommand(npmCommand, ['install'], { cwd: stdioServerDir })
    runCommand(npmCommand, ['run', 'build'], { cwd: stdioServerDir })
  } else {
    console.log('✓ STDIO server already built')
  }

  console.log('✓ MCP servers ready')
  console.log('  - HTTP/SSE server: http://localhost:3001/')
  console.log('  - STDIO server: test-tools-server/dist/index.js')
}

/**
 * Seed LLM logs by sending a few chat completion requests through Bifrost.
 * This ensures the Logs and Dashboard pages have data to display during tests.
 * Uses anthropic/claude-sonnet-4-5-20250929 by default; falls back gracefully.
 */
async function seedLLMLogs(baseUrl: string, count = 5): Promise<void> {
  console.log(`Seeding ${count} LLM log entries via ${baseUrl}/v1/chat/completions...`)
  const model = process.env.SEED_MODEL ?? 'openai/gpt-4o-mini'
  // Run seed calls in parallel batches of 5 for speed
  const batchSize = 5
  let successCount = 0
  for (let batch = 0; batch < count; batch += batchSize) {
    const batchEnd = Math.min(batch + batchSize, count)
    const promises = []
    for (let i = batch; i < batchEnd; i++) {
      const body = JSON.stringify({
        model,
        messages: [{ role: 'user', content: `E2E seed message ${i + 1}: say hello in ${(i % 5) + 1} words` }],
        max_tokens: 30,
      })
      promises.push(
        httpRequest(baseUrl, 'POST', '/v1/chat/completions', { body })
          .then((res) => {
            if (res.statusCode >= 200 && res.statusCode < 300) {
              successCount++
            } else {
              console.warn(`  Seed call ${i + 1} returned ${res.statusCode}: ${res.body.slice(0, 120)}`)
            }
          })
          .catch((err) => {
            console.warn(`  Seed call ${i + 1} failed: ${err}`)
          })
      )
    }
    await Promise.all(promises)
  }
  if (successCount > 0) {
    console.log(`✓ Seeded ${successCount}/${count} LLM log entries`)
  } else {
    console.warn(`⚠️  No seed calls succeeded. LLM Logs tests may see empty state.`)
  }
}

async function runBifrostMCPAndResponsesSetup(): Promise<void> {
  if (!process.env.BIFROST_BASE_URL) {
    console.log('Skipping Bifrost MCP client and /v1/responses (BIFROST_BASE_URL not set)')
    return
  }
  console.log(`Waiting for Bifrost API at ${BIFROST_BASE_URL}...`)
  await waitForBifrostAPI(BIFROST_BASE_URL)
  console.log(`✓ Bifrost API ready`)
  await ensureTestClient001AndSendResponses(BIFROST_BASE_URL)
  await seedLLMLogs(BIFROST_BASE_URL, 30)
}

function runMCPTeardown(): void {
  console.log('Tearing down MCP test servers...')
  MCP_SERVERS.forEach((server, index) => {
    try {
      if (server.pid && !server.killed) {
        try {
          process.kill(-server.pid, 'SIGTERM')
          console.log(`✓ Stopped MCP server ${index + 1} (PID: ${server.pid})`)
        } catch {
          server.kill('SIGTERM')
        }
      } else if (!server.killed) {
        server.kill('SIGTERM')
        console.log(`✓ Stopped MCP server ${index + 1}`)
      }
    } catch (error) {
      console.error(`Failed to stop MCP server ${index + 1}:`, error)
    }
  })
}

async function globalSetup(): Promise<() => Promise<void>> {
  await runPluginSetup()
  try {
    await runMCPSetup()
  } catch (error: unknown) {
    const err = error as Error
    console.error(`\n❌ Failed to setup MCP servers: ${err?.message || String(error)}`)
    console.error('\nTo setup manually:')
    console.error('  cd examples/mcps/http-no-ping-server && go build -o http-server main.go && ./http-server &')
    console.error('  cd examples/mcps/test-tools-server && npm install && npm run build')
    runMCPTeardown()
    throw error
  }
  try {
    await runBifrostMCPAndResponsesSetup()
  } catch (error: unknown) {
    const err = error as Error
    console.error(`\n❌ Bifrost MCP client / v1/responses setup failed: ${err?.message || String(error)}`)
    console.error(`   Ensure Bifrost is running at ${BIFROST_BASE_URL} and OPENAI_API_KEY is set for /v1/responses.`)
    runMCPTeardown()
    throw error
  }
  return async () => {
    runMCPTeardown()
  }
}

export default globalSetup
