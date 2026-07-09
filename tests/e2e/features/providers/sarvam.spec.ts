import { expect, test } from "../../core/fixtures/base.fixture";
import type { ProvidersPage } from "./pages/providers.page";
import { createProviderKeyData } from "./providers.data";

// Track created resources for cleanup
const createdKeys: { provider: string; keyName: string }[] = [];
// True only if this file created the sarvam provider itself - if it was
// already configured (e.g. a real user's config on a shared dev instance),
// we must never delete it.
let createdSarvamProvider = false;

test.describe("Sarvam Provider - Add Provider + API Key End-to-End", () => {
  test.describe.configure({ mode: "serial" });

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto();
  });

  test.afterEach(async ({ providersPage }) => {
    for (const { provider, keyName } of [...createdKeys]) {
      try {
        await providersPage.selectProvider(provider);
        const exists = await providersPage.keyExists(keyName, 2000);
        if (exists) {
          await providersPage.deleteKey(keyName);
        }
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error);
        console.error(
          `[CLEANUP ERROR] Failed to delete provider key ${provider}/${keyName}: ${errorMsg}`,
        );
      }
    }
    createdKeys.length = 0;
  });

  // `page`/`context`/`providersPage` fixtures are test-scoped and unavailable
  // in afterAll - use the API directly instead of going through the UI.
  test.afterAll(async ({ request }) => {
    if (!createdSarvamProvider) return;
    try {
      await request.delete("/api/providers/sarvam");
    } catch (error) {
      const errorMsg = error instanceof Error ? error.message : String(error);
      console.error(`[CLEANUP ERROR] Failed to delete provider sarvam: ${errorMsg}`);
    }
  });

  // Adds the provider only if it isn't already configured, and records
  // whether we're the ones who created it (see createdSarvamProvider above).
  async function ensureSarvamConfigured(providersPage: ProvidersPage) {
    if (await providersPage.providerExists("sarvam")) return;
    await providersPage.addKnownProviderFromDropdown("sarvam");
    createdSarvamProvider = true;
  }

  test("should add Sarvam as a known provider from the dropdown", async ({
    providersPage,
  }) => {
    await ensureSarvamConfigured(providersPage);

    await providersPage.selectProvider("sarvam");
    await expect(providersPage.page).toHaveURL(/provider=sarvam/);
  });

  test("should add a real Sarvam API key and verify it works end-to-end through Bifrost", async ({
    providersPage,
    request,
  }) => {
    const apiKey = process.env.SARVAM_API_KEY;
    test.skip(!apiKey, "SARVAM_API_KEY not set - skipping live key verification");

    // Previous test in this serial file already added the provider; select
    // it, adding it fresh only if it's genuinely missing.
    await ensureSarvamConfigured(providersPage);
    await providersPage.selectProvider("sarvam");

    const keyData = createProviderKeyData({
      name: `E2E-Sarvam-Key-${Date.now()}`,
      value: apiKey!,
      weight: 1.0,
    });
    createdKeys.push({ provider: "sarvam", keyName: keyData.name });

    // Add the key through the real UI, against the real running backend.
    await providersPage.addKey(keyData);

    // Confirm the key was persisted and shows in the table.
    const keyExists = await providersPage.keyExists(keyData.name);
    expect(keyExists).toBe(true);

    // Full-stack verification: the key just added through the UI must
    // actually work for a real chat completion through Bifrost's own API
    // (not a direct call to Sarvam) - proves UI -> backend -> Sarvam works
    // end-to-end, not just that the UI accepted and stored the string.
    // Vite dev server only proxies /api, not /v1 - hit the backend directly.
    const bifrostBaseUrl = process.env.BIFROST_BASE_URL || "http://localhost:8080";
    const response = await request.post(`${bifrostBaseUrl}/v1/chat/completions`, {
      data: {
        model: "sarvam/sarvam-105b",
        messages: [{ role: "user", content: "Say the word 'pong' and nothing else." }],
        max_tokens: 20,
      },
    });
    expect(response.ok()).toBe(true);
    const body = await response.json();
    expect(body.choices?.[0]?.message).toBeTruthy();
  });
});
