import { existsSync, readFileSync } from "fs";
import { join, resolve } from "path";

/**
 * A run plan is written by scripts/run-e2e.mjs: one entry per worker, each with its
 * own Bifrost (port + database) and the spec files scheduled onto it. Playwright
 * turns every worker into a project that runs its files one at a time.
 */
export interface PlannedWorker {
  name: string;
  port: number;
  files: string[];
}

export const E2E_ROOT = __dirname;
export const AUTH_DIR = resolve(E2E_ROOT, ".auth");
export const REPORTS_DIR = resolve(E2E_ROOT, "reports");
export const TIMINGS_FILE = join(REPORTS_DIR, "e2e-timings.json");

export function loadPlan(): PlannedWorker[] | null {
  const path = process.env.E2E_PLAN;
  if (!path) return null;
  if (!existsSync(path))
    throw new Error(`E2E_PLAN is set but ${path} does not exist`);
  return JSON.parse(readFileSync(path, "utf8")).workers;
}

export function storageStatePath(workerName: string): string {
  return join(AUTH_DIR, `${workerName}.json`);
}