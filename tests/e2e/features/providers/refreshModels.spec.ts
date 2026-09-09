import { randomUUID } from "crypto";
import { expect, test } from "../../core/fixtures/base.fixture";
import { createProviderKeyData } from "./providers.data";

// Unique per run so concurrent runs against a shared backend cannot collide on
// key names. Cleanup deletes by name, so a collision would let one run delete
// another's key mid-test.
const runId = randomUUID().slice(0, 8);

// Track created resources for cleanup
const createdKeys: { provider: string; keyName: string }[] = [];

/**
 * Covers the on-demand model discovery buttons.
 *
 * The model catalog otherwise refreshes only at startup, on key edits, and on
 * the configured background interval, so these buttons are how an operator
 * picks up a model a provider just started serving. They are also the only
 * discovery path available when the interval is set to 0.
 */
test.describe("Provider model refresh", () => {
  test.describe.configure({ mode: "serial" });

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto();
  });

  test.afterEach(async ({ providersPage }) => {
    const failedKeys: { provider: string; keyName: string }[] = [];
    for (const { provider, keyName } of [...createdKeys]) {
      try {
        await providersPage.selectProvider(provider);
        const exists = await providersPage.keyExists(keyName, 2000);
        if (exists) {
          await providersPage.deleteKey(keyName);
        }
      } catch (error) {
        // Retained rather than dropped so the next afterEach retries it. The
        // throw is deferred to afterAll on purpose: this describe runs in
        // serial mode, where a failure here skips every remaining test, and
        // their afterEach hooks would never run the retry.
        failedKeys.push({ provider, keyName });
        const errorMsg = error instanceof Error ? error.message : String(error);
        console.error(
          `[CLEANUP ERROR] Failed to delete provider key ${provider}/${keyName}: ${errorMsg}`,
        );
      }
    }
    createdKeys.splice(0, createdKeys.length, ...failedKeys);
  });

  test.afterAll(() => {
    if (createdKeys.length === 0) {
      return;
    }
    const leaked = createdKeys
      .map(({ provider, keyName }) => `${provider}/${keyName}`)
      .join(", ");
    // No need to reset createdKeys: a failure discards the worker process, so a
    // retry re-imports this module with fresh module state and a fresh runId.
    throw new Error(
      `Leaked ${createdKeys.length} provider key(s) after cleanup retries: ${leaked}`,
    );
  });

  test("should show the provider-level refresh button", async ({
    providersPage,
  }) => {
    await providersPage.selectProvider("openai");

    await expect(providersPage.refreshProviderModelsBtn).toBeVisible();
    await expect(providersPage.refreshProviderModelsBtn).toBeEnabled();
  });

  test("should refresh models for the whole provider", async ({
    providersPage,
  }) => {
    await providersPage.selectProvider("openai");

    await providersPage.refreshProviderModels();

    // The button re-enables once the request settles; the server serialises
    // refreshes per provider, so a stuck button would mean the in-flight guard
    // never released.
    await expect(providersPage.refreshProviderModelsBtn).toBeEnabled({
      timeout: 15000,
    });
  });

  test("should refresh models for a single key", async ({ providersPage }) => {
    await providersPage.selectProvider("openai");

    const keyData = createProviderKeyData({
      name: `Refresh-Key-${runId}`,
      value: "sk-test-refresh-key-12345",
      weight: 1.0,
    });
    createdKeys.push({ provider: "openai", keyName: keyData.name });
    await providersPage.addKey(keyData);
    expect(await providersPage.keyExists(keyData.name)).toBe(true);

    const refreshBtn = providersPage.getKeyRefreshModelsBtn(keyData.name);
    await expect(refreshBtn).toBeVisible();

    await providersPage.refreshKeyModels(keyData.name);

    await expect(refreshBtn).toBeEnabled({ timeout: 15000 });
  });

  test("should disable the refresh button for a disabled key", async ({
    providersPage,
  }) => {
    await providersPage.selectProvider("openai");

    const keyData = createProviderKeyData({
      name: `Refresh-Disabled-${runId}`,
      value: "sk-test-refresh-disabled-12345",
      weight: 1.0,
    });
    createdKeys.push({ provider: "openai", keyName: keyData.name });
    await providersPage.addKey(keyData);
    expect(await providersPage.keyExists(keyData.name)).toBe(true);

    // A disabled key is never fetched by discovery, so offering to refresh it
    // would only ever report a failure the user cannot act on.
    await providersPage.toggleKeyEnabled(keyData.name);
    expect(await providersPage.getKeyEnabledState(keyData.name)).toBe(false);

    await expect(
      providersPage.getKeyRefreshModelsBtn(keyData.name),
    ).toBeDisabled();
  });

  test("should keep the edit menu reachable alongside the refresh button", async ({
    providersPage,
  }) => {
    // The refresh button adds a second icon button to the actions cell. The
    // page object finds the dropdown trigger via `.last()`, so a regression in
    // button ordering would silently break every edit and delete test.
    await providersPage.selectProvider("openai");

    const keyData = createProviderKeyData({
      name: `Refresh-Ordering-${runId}`,
      value: "sk-test-refresh-ordering-12345",
      weight: 1.0,
    });
    createdKeys.push({ provider: "openai", keyName: keyData.name });
    await providersPage.addKey(keyData);
    expect(await providersPage.keyExists(keyData.name)).toBe(true);

    await providersPage.editKey(keyData.name, { weight: 0.5 });

    expect(await providersPage.getKeyWeight(keyData.name)).toContain("0.5");
  });
});
