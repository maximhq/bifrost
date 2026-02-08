import { existsSync } from 'fs'

const TEST_PLUGIN_PATH = '/tmp/bifrost-test-plugin.so'

/**
 * Gets the test plugin path
 * The plugin should be built by globalSetup before tests run
 */
export function ensureTestPluginExists(): string {
  if (!existsSync(TEST_PLUGIN_PATH)) {
    throw new Error(
      `Test plugin not found at ${TEST_PLUGIN_PATH}. ` +
      `Please build it first: cd examples/plugins/hello-world && make dev && cp build/hello-world.so ${TEST_PLUGIN_PATH}`
    )
  }
  return TEST_PLUGIN_PATH
}
