import { test as base, expect } from '@playwright/test'
import { SidebarPage } from '../pages/sidebar.page'
import { ProvidersPage } from '../../features/providers/pages/providers.page'
import { VirtualKeysPage } from '../../features/virtual-keys/pages/virtual-keys.page'

/**
 * Custom test fixtures type
 */
type BifrostFixtures = {
  sidebarPage: SidebarPage
  providersPage: ProvidersPage
  virtualKeysPage: VirtualKeysPage
}

/**
 * Extended test with Bifrost-specific fixtures
 */
export const test = base.extend<BifrostFixtures>({
  sidebarPage: async ({ page }, use) => {
    await use(new SidebarPage(page))
  },

  providersPage: async ({ page }, use) => {
    await use(new ProvidersPage(page))
  },

  virtualKeysPage: async ({ page }, use) => {
    await use(new VirtualKeysPage(page))
  },
})

export { expect }
