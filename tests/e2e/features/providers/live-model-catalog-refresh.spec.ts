import { execSync } from "node:child_process";
import { expect, test } from "../../core/fixtures/base.fixture";

/**
 * E2E coverage for the periodic live list-models refresh feature (fixes
 * maximhq/bifrost#2964 — "Model Catalog refresh"). Exercises the real
 * `ollama` binary running on the host (assumed present at
 * http://localhost:11434) so the ticker's fetch actually hits a live
 * provider, not a mock.
 *
 * Skips entirely if `ollama` is not on PATH or not reachable — this suite
 * only runs where a real Ollama instance is available (see README).
 */

const OLLAMA_URL = "http://localhost:11434";
// A small, already-pulled model to `ollama cp` from — avoids downloading
// anything during the test. Adjust if the CI/dev machine's Ollama library differs.
const SOURCE_MODEL = "qwen2.5:0.5b";
const REFRESH_INTERVAL_SEC = 30; // minimum accepted by validateListModelsRefreshInterval

function ollamaAvailable(): boolean {
  try {
    execSync("ollama --version", { stdio: "ignore" });
    execSync(`curl -sf --max-time 2 ${OLLAMA_URL}/api/tags`, { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

function ollamaHasSourceModel(): boolean {
  try {
    const out = execSync("ollama list", { encoding: "utf-8" });
    return out.includes(SOURCE_MODEL.split(":")[0]);
  } catch {
    return false;
  }
}

test.describe("Live model catalog periodic refresh (Ollama)", () => {
  test.describe.configure({ mode: "serial" });
  test.skip(!ollamaAvailable(), "ollama is not installed/reachable at " + OLLAMA_URL);
  test.skip(ollamaAvailable() && !ollamaHasSourceModel(), `Ollama has no local model matching "${SOURCE_MODEL}" to alias from`);

  const keyName = `E2E-Ollama-Key-${Date.now()}`;
  let providerAddedByTest = false;

  test.beforeAll(async () => {
    // Sanity: fail fast with a clear message rather than a mysterious timeout
    // later if the assumed source model isn't present.
    execSync(`ollama show ${SOURCE_MODEL}`, { stdio: "ignore" });
  });

  test.afterAll(() => {
    // Best-effort cleanup of any model aliases this suite created, in case a
    // test failed before reaching its own cleanup step.
    for (const tag of createdOllamaTags) {
      try {
        execSync(`ollama rm ${tag}`, { stdio: "ignore" });
      } catch {
        // already removed / never created — fine
      }
    }
  });

  const createdOllamaTags: string[] = [];

  test("adds the Ollama provider with a periodic refresh interval", async ({ providersPage }) => {
    await providersPage.goto();

    if (!(await providersPage.providerExists("ollama"))) {
      await providersPage.addKnownProviderFromDropdown("ollama");
      providerAddedByTest = true;
    }
    await providersPage.selectProvider("ollama");

    if (!(await providersPage.keyExists(keyName, 2000))) {
      await providersPage.addOllamaKey({ name: keyName, url: OLLAMA_URL });
    }

    await providersPage.setListModelsRefreshInterval(REFRESH_INTERVAL_SEC);
  });

  test("periodically picks up a model added directly in Ollama, without resaving the provider", async ({ providersPage }) => {
    const tag = `e2e-refresh-added-${Date.now()}`;
    execSync(`ollama cp ${SOURCE_MODEL} ${tag}`);
    createdOllamaTags.push(tag);

    // No UI interaction with the provider from here on — this is the whole
    // point of the feature: bifrost must notice the new model on its own via
    // the periodic ticker, not because we touched the provider/key config.
    await expect
      .poll(
        async () => {
          const res = await providersPage.page.request.get(`/api/models?provider=ollama&query=${tag}&limit=10`);
          if (!res.ok()) return 0;
          const body = (await res.json()) as { total: number };
          return body.total;
        },
        {
          message: `expected model "${tag}" to appear in bifrost's catalog for provider ollama within one refresh cycle`,
          timeout: (REFRESH_INTERVAL_SEC + 60) * 1000,
          intervals: [2000],
        },
      )
      .toBeGreaterThan(0);
  });

  test("periodically drops a model removed directly in Ollama", async ({ providersPage }) => {
    const tag = `e2e-refresh-removed-${Date.now()}`;
    execSync(`ollama cp ${SOURCE_MODEL} ${tag}`);
    createdOllamaTags.push(tag);

    // Wait for it to show up first (same as the previous test), then remove
    // it and confirm the next tick prunes it from bifrost's catalog too.
    await expect
      .poll(
        async () => {
          const res = await providersPage.page.request.get(`/api/models?provider=ollama&query=${tag}&limit=10`);
          if (!res.ok()) return 0;
          const body = (await res.json()) as { total: number };
          return body.total;
        },
        { timeout: (REFRESH_INTERVAL_SEC + 60) * 1000, intervals: [2000] },
      )
      .toBeGreaterThan(0);

    execSync(`ollama rm ${tag}`);
    createdOllamaTags.splice(createdOllamaTags.indexOf(tag), 1);

    await expect
      .poll(
        async () => {
          const res = await providersPage.page.request.get(`/api/models?provider=ollama&query=${tag}&limit=10`);
          if (!res.ok()) return -1;
          const body = (await res.json()) as { total: number };
          return body.total;
        },
        {
          message: `expected model "${tag}" to disappear from bifrost's catalog for provider ollama within one refresh cycle`,
          timeout: (REFRESH_INTERVAL_SEC + 60) * 1000,
          intervals: [2000],
        },
      )
      .toBe(0);
  });

  test("skips the periodic refresh for a disabled key", async ({ providersPage }) => {
    await providersPage.goto();
    await providersPage.selectProvider("ollama");
    const wasEnabled = await providersPage.getKeyEnabledState(keyName);
    expect(wasEnabled).toBe(true); // sanity: key starts enabled from the setup test

    await providersPage.toggleKeyEnabled(keyName); // disable

    const tag = `e2e-refresh-disabled-${Date.now()}`;
    execSync(`ollama cp ${SOURCE_MODEL} ${tag}`);
    createdOllamaTags.push(tag);

    try {
      // Give at least one full tick past the interval a chance to run, then
      // assert it did NOT pick up the new model — the ticker must skip a
      // disabled key entirely (RefreshLiveModelsForProvider's enabled-key filter).
      await providersPage.page.waitForTimeout((REFRESH_INTERVAL_SEC + 20) * 1000);
      const res = await providersPage.page.request.get(`/api/models?provider=ollama&query=${tag}&limit=10`);
      const body = (await res.json()) as { total: number };
      expect(body.total).toBe(0);
    } finally {
      // Re-enable so cleanup (deleteKey/deleteProvider) below isn't fighting a disabled key,
      // and remove the alias.
      await providersPage.toggleKeyEnabled(keyName);
      execSync(`ollama rm ${tag}`, { stdio: "ignore" });
      createdOllamaTags.splice(createdOllamaTags.indexOf(tag), 1);
    }
  });

  test("cleanup: remove the test key and provider (if created by this suite)", async ({ providersPage }) => {
    await providersPage.goto();
    await providersPage.selectProvider("ollama");
    if (await providersPage.keyExists(keyName, 2000)) {
      await providersPage.deleteKey(keyName);
    }
    if (providerAddedByTest) {
      await providersPage.deleteProvider("ollama", { skipToastWait: true });
    }
  });
});
