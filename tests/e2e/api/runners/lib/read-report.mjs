// Size-safe reader for newman JSON reports.
//
// A full provider-harness run produces a report larger than V8's max string length
// (0x1fffffe8, ~512MB), at which point readFileSync(path, "utf8") throws
// ERR_STRING_TOO_LONG before JSON.parse is ever reached. The bulk of that weight is
// run.failures[].parent, where newman re-embeds the entire enclosing folder in every
// single failure - see lib/newman-merge.jq for the measurements.
//
// The Makefile now slims reports as it writes them, so oversized files should be rare.
// But a report left over from an older run still sits in tmp/, and filter-collection.mjs
// reads it BEFORE newman runs (for --rerun-failed), so no write-time fix can help there.
// Every reader goes through readReport() to stay safe either way.

import { readFileSync, statSync, existsSync, openSync, closeSync, renameSync, unlinkSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// V8's hard cap on a single string. Node cannot produce a utf8 string above this.
const MAX_STRING = 0x1fffffe8;

const MERGE_PROGRAM = join(dirname(fileURLToPath(import.meta.url)), "newman-merge.jq");

const mb = (bytes) => `${(bytes / 1024 / 1024).toFixed(1)}MB`;

const hasJq = () => {
  try {
    execFileSync("jq", ["--version"], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
};

// sanitizeTo runs the shared merge/slim program over `src`, writing to `dest`. Output is
// piped straight to a file descriptor rather than captured, since the result is routinely
// over 100MB and would blow execFileSync's maxBuffer.
//
// Writes via a .partial sibling and renames, so a jq run that dies midway (or a reader
// racing a `make` invocation writing the same sidecar) can never leave a truncated file
// sitting at the final name where the next reader would treat it as complete.
const sanitizeTo = (src, dest) => {
  const partial = `${dest}.partial`;
  const fd = openSync(partial, "w");
  try {
    execFileSync("jq", ["-s", "-f", MERGE_PROGRAM, src], { stdio: ["ignore", fd, "inherit"] });
  } catch (err) {
    closeSync(fd);
    try { unlinkSync(partial); } catch { /* best effort */ }
    throw err;
  }
  closeSync(fd);
  renameSync(partial, dest);
};

// readReport parses a newman JSON report, transparently slimming it first if it exceeds
// what Node can hold in a string. Throws with an actionable message rather than letting
// an ERR_STRING_TOO_LONG stack trace escape.
export const readReport = (path) => {
  const size = statSync(path).size;
  if (size <= MAX_STRING) return JSON.parse(readFileSync(path, "utf8"));

  // Dot-prefixed on purpose: the Makefile merges per-provider reports with the glob
  // tmp/newman-report-*.json, and a sidecar named newman-report-slim.json would be swept
  // into that merge and duplicate every execution.
  const slim = join(dirname(path), `.${basename(path, ".json")}.slim.json`);

  // Reuse an earlier sanitize pass when it is still current - the jq run over a 500MB+
  // report takes ~20s and several readers hit the same report in one make invocation.
  // A newer mtime says the sidecar is current, not that it is intact, so the parse is
  // guarded: anything unreadable falls through to a fresh sanitize below.
  if (existsSync(slim) && statSync(slim).mtimeMs >= statSync(path).mtimeMs) {
    const slimSize = statSync(slim).size;
    if (slimSize <= MAX_STRING) {
      try {
        const parsed = JSON.parse(readFileSync(slim, "utf8"));
        console.error(`[read-report] ${path} is ${mb(size)} (over Node's ~512MB string limit) - reusing ${slim}`);
        return parsed;
      } catch {
        console.error(`[read-report] ${slim} is unreadable (truncated or mid-write) - regenerating`);
      }
    }
  }

  if (!hasJq()) {
    throw new Error(
      `[read-report] ${path} is ${mb(size)}, past Node's ~512MB max string length, and jq is not installed to slim it.\n` +
      `  Install jq (brew install jq) and re-run, or delete the stale report:  rm ${path}`
    );
  }

  console.error(`[read-report] ${path} is ${mb(size)} (over Node's ~512MB string limit) - slimming via jq into ${slim}...`);
  sanitizeTo(path, slim);

  const slimSize = statSync(slim).size;
  if (slimSize > MAX_STRING) {
    throw new Error(
      `[read-report] ${path} is still ${mb(slimSize)} after slimming - too large to parse.\n` +
      `  Re-run the harness with a narrower PROVIDER= or FOLDER= scope, or delete it:  rm ${path}`
    );
  }
  console.error(`[read-report] slimmed ${mb(size)} -> ${mb(slimSize)}`);
  return JSON.parse(readFileSync(slim, "utf8"));
};
