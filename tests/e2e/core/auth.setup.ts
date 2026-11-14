import { test as setup } from "@playwright/test";
import { mkdirSync } from "fs";
import { AUTH_DIR, storageStatePath } from "../plan";

setup("authenticate", async ({ request }, testInfo) => {
  const worker = testInfo.project.metadata.worker as string;
  mkdirSync(AUTH_DIR, { recursive: true });

  const username = process.env.BIFROST_ADMIN_USERNAME;
  const password = process.env.BIFROST_ADMIN_PASSWORD;
  if (username && password) {
    const res = await request.post("/api/session/login", {
      data: { username, password },
    });
    if (!res.ok() && res.status() !== 403) {
      throw new Error(
        `login for worker "${worker}" failed: ${res.status()} ${await res.text()}`,
      );
    }
  }
  // The onboarding checklist is a fixed card over the bottom-right corner, where
  // forms put their Save buttons. Hide it for everyone on this worker's server.
  const dismissed = await request.post("/api/config/metadata", {
    data: { onboarding_dismissed: true },
  });
  if (!dismissed.ok()) {
    throw new Error(
      `dismissing onboarding for worker "${worker}" failed: ${dismissed.status()} ${await dismissed.text()}`,
    );
  }
  await request.storageState({ path: storageStatePath(worker) });
});
