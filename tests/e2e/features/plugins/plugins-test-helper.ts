import { existsSync } from 'fs'
import { join, resolve } from 'path'

// Same location as Makefile build-test-plugin and global setup (repo root tmp/)
const REPO_ROOT = resolve(__dirname, '..', '..', '..', '..')
const TEST_PLUGIN_PATH = join(REPO_ROOT, 'tmp', 'bifrost-test-plugin.so')

/**
 * Gets the test plugin path.
 * The plugin is built by global setup / make build-test-plugin at repo_root/tmp/bifrost-test-plugin.so.
 */
export function ensureTestPluginExists(): string {
  if (!existsSync(TEST_PLUGIN_PATH)) {
    throw new Error(
      `Test plugin not found at ${TEST_PLUGIN_PATH}. ` +
        `Please build it first: make build-test-plugin (from repo root)`
    )
  }
  return TEST_PLUGIN_PATH
}
