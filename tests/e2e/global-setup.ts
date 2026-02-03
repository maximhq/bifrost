/**
 * Single global setup for all E2E tests.
 * 1. Builds test plugin (plugins) and copies to /tmp.
 * 2. Builds and starts MCP test servers (HTTP/SSE on 3001, STDIO test-tools-server).
 * Returns a teardown function that stops MCP servers.
 */
import { execFileSync, execSync, spawn, type ChildProcess } from 'child_process'
import { existsSync } from 'fs'
import { resolve, join } from 'path'
import { setTimeout } from 'timers/promises'
import * as http from 'http'
import * as os from 'os'

const REPO_ROOT = resolve(__dirname, '../..')
const TEST_PLUGIN_PATH = 'tmp/bifrost-test-plugin.so'

const MCP_SERVERS: ChildProcess[] = []
const isWindows = os.platform() === 'win32'
const npmCommand = isWindows ? 'npm.cmd' : 'npm'
const goCommand = isWindows ? 'go.exe' : 'go'
const httpServerBinaryName = isWindows ? 'http-server.exe' : 'http-server'
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
    throw err
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

function runMCPTeardown(): void {
  console.log('Tearing down MCP test servers...')
  MCP_SERVERS.forEach((server, index) => {
    try {
      if (server.pid && !server.killed) {
        try {
          process.kill(server.pid, 'SIGTERM')
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
    throw error
  }
  return async () => {
    runMCPTeardown()
  }
}

export default globalSetup
