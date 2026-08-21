import { randomUUID } from "crypto";
import { providersApi } from "../../core/actions/api";
import { expect, test } from "../../core/fixtures/base.fixture";

// Unique per run so concurrent runs against a shared backend cannot collide on
// key names. Cleanup deletes by name, so a collision would let one run delete
// another's key mid-test.
const runId = randomUUID().slice(0, 8);

// Track created resources for cleanup
const createdKeys: { provider: string; keyName: string }[] = [];

// Copilot is not configured by default. It is provisioned over the API rather
// than through the "Add provider" dropdown: that dropdown hides providers that
// already exist, so a UI-driven setup silently breaks on reruns and retries.
// Only torn down when this spec created it, so a pre-configured environment
// keeps its provider.
let createdCopilotProvider = false;

// The form auto-names a device-login key when the name field is left empty.
const AUTO_KEY_NAME = "GitHub Copilot";

/**
 * Covers what happens *after* a Copilot device-code login completes.
 *
 * Only the two GitHub-dependent endpoints are stubbed, because completing a
 * real device-code grant requires a human to approve a code on github.com.
 * Everything downstream of the grant — key persistence and model discovery —
 * is left to the real backend, since that chain is the part upstream changes
 * can silently break.
 *
 * The stub deliberately does not model a valid grant (no device-code
 * bookkeeping, no pending/slow_down states). Those belong to the handler unit
 * tests in transports/bifrost-http/handlers/copilot_test.go. What matters here
 * is the wiring: on a completed grant the form must persist the key and then
 * ask the backend to rediscover models.
 *
 * The stubbed token is not a real Copilot credential, so discovery itself will
 * not return models. That is intentional and does not weaken the test: the
 * regression this guards against is the *call* being dropped, renamed or
 * reordered — which is what nearly happened when upstream added
 * POST /providers/{provider}/refresh-models alongside an earlier endpoint.
 */
test.describe("Copilot device login", () => {
  test.describe.configure({ mode: "serial" });

  test.beforeAll(async ({ request }) => {
    const providers = (await providersApi.getAll(request)) as
      | { name?: string }[]
      | { providers?: { name?: string }[] };
    const list = Array.isArray(providers)
      ? providers
      : (providers.providers ?? []);

    if (!list.some((p) => p?.name === "copilot")) {
      await providersApi.create(request, { provider: "copilot" });
      createdCopilotProvider = true;
    }
  });

  test.beforeEach(async ({ providersPage }) => {
    await providersPage.goto();
  });

  test.afterEach(async ({ page, providersPage }) => {
    // The key form renders in a sheet whose overlay swallows pointer events.
    // Left open, it makes every cleanup click below time out against the
    // overlay rather than the element being targeted.
    await page.keyboard.press("Escape").catch(() => {});
    await providersPage.keyForm
      .waitFor({ state: "hidden", timeout: 5000 })
      .catch(() => {});

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

  test.afterAll(async ({ request }) => {
    if (createdCopilotProvider) {
      try {
        await providersApi.delete(request, "copilot");
        createdCopilotProvider = false;
      } catch (error) {
        const errorMsg = error instanceof Error ? error.message : String(error);
        console.error(
          `[CLEANUP ERROR] Failed to delete copilot provider: ${errorMsg}`,
        );
      }
    }

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

  test("should persist the key and rediscover models once the grant completes", async ({
    page,
    providersPage,
  }) => {
    // Stub only the GitHub handshake. Both handlers are unconditional: the
    // point is to reach the "grant completed" branch, not to emulate GitHub.
    await page.route(
      "**/api/providers/copilot/device-login/initiate",
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            device_code: `e2e-device-code-${runId}`,
            user_code: "E2E-CODE",
            verification_uri: "https://github.com/login/device",
            interval: 5,
          }),
        });
      },
    );

    await page.route(
      "**/api/providers/copilot/device-login/poll",
      async (route) => {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            status: "complete",
            access_token: `ghu_e2e-not-a-real-token-${runId}`,
          }),
        });
      },
    );

    // Record the post-grant calls in order, without intercepting them: these
    // must reach the real backend for the assertions to mean anything.
    const postGrantCalls: string[] = [];
    page.on("request", (request) => {
      if (request.method() !== "POST") {
        return;
      }
      const { pathname } = new URL(request.url());
      if (pathname === "/api/providers/copilot/keys") {
        postGrantCalls.push("create-key");
      } else if (pathname === "/api/providers/copilot/refresh-models") {
        postGrantCalls.push("refresh-models");
      }
    });

    await providersPage.selectProvider("copilot");

    createdKeys.push({ provider: "copilot", keyName: AUTO_KEY_NAME });
    await providersPage.addKeyBtn.click();
    await expect(providersPage.keyForm).toBeVisible();

    // Copilot has no plain API-key field; the device-login tab is the default
    // but is selected explicitly so a change in default tab fails loudly here
    // rather than further down.
    await providersPage.copilotDeviceLoginTab.click();
    await providersPage.copilotDeviceLoginBtn.click();

    // The user code proves the initiate response was consumed and the form
    // moved into the awaiting-authorisation state.
    await expect(providersPage.copilotCopyCodeBtn).toBeVisible();
    await expect(page.getByText("E2E-CODE")).toBeVisible();

    await providersPage.copilotConfirmAuthBtn.click();

    // The key must be persisted, and discovery must then be requested against
    // the endpoint the provider form is currently wired to.
    await expect
      .poll(() => postGrantCalls, { timeout: 20000 })
      .toEqual(["create-key", "refresh-models"]);

    // The saved key surfacing in the table confirms the grant actually
    // produced persisted state rather than only firing requests.
    expect(await providersPage.keyExists(AUTO_KEY_NAME)).toBe(true);
  });
});
