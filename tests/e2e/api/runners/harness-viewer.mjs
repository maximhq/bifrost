#!/usr/bin/env node
// Interactive viewer for the provider-harness newman run.
// Reads tmp/newman-report.json, serves a single-page UI on http://localhost:8090,
// and proxies "Resend" clicks to the original request URL so users can iterate
// against the still-running Bifrost. The make recipe blocks until the user
// clicks "Close" (or hits Ctrl-C), at which point this process exits 0.
//
// Usage:
//   node harness-viewer.mjs --report tmp/newman-report.json [--port 8090]

import { readFileSync, existsSync } from "node:fs";
import { createServer } from "node:http";
import { URL } from "node:url";

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith("--")) acc.push([cur.slice(2), arr[i + 1]]);
    return acc;
  }, [])
);
const REPORT = args.report || "tmp/newman-report.json";
const FAILURES_MD = args["failures-md"] || "tmp/harness-failures.md";
const PORT = parseInt(args.port || "8090", 10);

if (!existsSync(REPORT)) {
  console.error(`[harness-viewer] report not found: ${REPORT}`);
  process.exit(1);
}

const bufToString = (b) => {
  if (!b) return "";
  if (typeof b === "string") return b;
  if (b.type === "Buffer" && Array.isArray(b.data)) return Buffer.from(b.data).toString("utf8");
  return String(b);
};

// Newman strips `url.raw` and stores resolved components in `protocol/host/port/path/query`.
// Reconstruct the full URL so the viewer + Copy curl get the actual hostname.
const reconstructUrl = (urlObj) => {
  if (!urlObj) return "";
  if (typeof urlObj === "string") return urlObj;
  if (urlObj.raw) return urlObj.raw;
  const protocol = urlObj.protocol || "http";
  const host = Array.isArray(urlObj.host) ? urlObj.host.join(".") : urlObj.host || "";
  const port = urlObj.port ? `:${urlObj.port}` : "";
  const path = Array.isArray(urlObj.path) ? "/" + urlObj.path.join("/") : urlObj.path || "";
  const query = Array.isArray(urlObj.query) && urlObj.query.length
    ? "?" + urlObj.query.filter((q) => !q.disabled).map((q) => `${q.key}=${q.value}`).join("&")
    : "";
  return `${protocol}://${host}${port}${path}${query}`;
};

const summarize = () => {
  const raw = JSON.parse(readFileSync(REPORT, "utf8"));
  const execs = raw.run?.executions || [];
  return execs.map((e, idx) => {
    const folderPath = (e.item?.path || []).join(" / ");
    const headers = (e.request?.header || []).map((h) => ({ key: h.key, value: h.value, disabled: !!h.disabled }));
    const reqBody = e.request?.body?.raw || bufToString(e.request?.body) || "";
    const respBody = bufToString(e.response?.stream);
    const respHeaders = (e.response?.header || []).map((h) => ({ key: h.key, value: h.value }));
    const assertions = (e.assertions || []).map((a) => ({
      name: a.assertion,
      passed: !a.error,
      error: a.error?.message || null,
    }));
    const failed = assertions.some((a) => !a.passed) || (e.response?.code ?? 0) >= 400 || !e.response;
    return {
      idx,
      name: e.item?.name || `request-${idx}`,
      folder: folderPath,
      method: e.request?.method || "GET",
      url: reconstructUrl(e.request?.url),
      reqHeaders: headers,
      reqBody,
      respCode: e.response?.code ?? 0,
      respStatus: e.response?.status || "",
      respTimeMs: e.response?.responseTime || 0,
      respHeaders,
      respBody,
      assertions,
      failed,
    };
  });
};

// readCoverageMarkdown extracts the "## Coverage matrices" section through to
// the next top-level heading, returning the raw markdown. Empty string if not found.
const readCoverageMarkdown = () => {
  if (!existsSync(FAILURES_MD)) return "";
  const md = readFileSync(FAILURES_MD, "utf8");
  const start = md.indexOf("## Coverage matrices");
  if (start === -1) return "";
  // Find the next "## " heading after the coverage section
  let cursor = start + "## Coverage matrices".length;
  const next = md.indexOf("\n## ", cursor);
  return next === -1 ? md.slice(start) : md.slice(start, next);
};

// mdToHtml converts a small subset of markdown (headings, tables, paragraphs,
// code spans, inline emphasis, line breaks) to HTML — just enough to render
// the coverage section the analyzer emits. Not a general-purpose converter.
const mdToHtml = (md) => {
  if (!md) return "<p><em>No coverage data found. Run <code>make run-provider-harness-test</code> first to generate <code>tmp/harness-failures.md</code>.</em></p>";
  const escapeHtml = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  const inline = (s) => escapeHtml(s)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/~~([^~]+)~~/g, "<del>$1</del>");
  const lines = md.split(/\r?\n/);
  const out = [];
  let inTable = false;
  let tableRows = [];
  const flushTable = () => {
    if (!inTable) return;
    out.push("<table class=\"cov\">");
    tableRows.forEach((row, idx) => {
      // skip the header-separator row (---|---|...)
      if (/^\s*\|?[-:\s|]+\|?\s*$/.test(row)) return;
      const cells = row.split("|").slice(1, -1).map((c) => c.trim());
      const tag = idx === 0 ? "th" : "td";
      out.push("<tr>" + cells.map((c) => `<${tag}>${inline(c)}</${tag}>`).join("") + "</tr>");
    });
    out.push("</table>");
    tableRows = [];
    inTable = false;
  };
  for (const line of lines) {
    // Any line starting with `|` is a table row (header, separator, or data).
    // The separator row (|---|---|) is filtered out inside flushTable.
    if (line.startsWith("|")) {
      inTable = true;
      tableRows.push(line);
      continue;
    }
    if (inTable) flushTable();
    if (line.startsWith("### ")) out.push(`<h3>${inline(line.slice(4))}</h3>`);
    else if (line.startsWith("## ")) out.push(`<h2>${inline(line.slice(3))}</h2>`);
    else if (line.startsWith("- ")) out.push(`<li>${inline(line.slice(2))}</li>`);
    else if (line.trim() === "") out.push("");
    else out.push(`<p>${inline(line)}</p>`);
  }
  flushTable();
  return out.join("\n").replace(/(<li>[\s\S]*?<\/li>(\s*<li>[\s\S]*?<\/li>)*)/g, "<ul>$1</ul>");
};

const COVERAGE_HTML = mdToHtml(readCoverageMarkdown());

const VIEWER_HTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8" />
<title>Bifrost Provider Harness Report</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; margin: 0; background: #0d1117; color: #e6edf3; }
  header { display: flex; align-items: center; justify-content: space-between; padding: 12px 24px; border-bottom: 1px solid #30363d; background: #161b22; position: sticky; top: 0; z-index: 10; }
  header h1 { font-size: 16px; margin: 0; font-weight: 600; }
  header .meta { font-size: 12px; color: #8b949e; margin-left: 12px; }
  header .controls { display: flex; gap: 8px; align-items: center; }
  button { font: inherit; padding: 6px 12px; border-radius: 6px; border: 1px solid #30363d; background: #21262d; color: #e6edf3; cursor: pointer; }
  button:hover { background: #30363d; }
  button.danger { background: #da3633; border-color: #da3633; }
  button.danger:hover { background: #f85149; }
  button.primary { background: #238636; border-color: #238636; }
  button.primary:hover { background: #2ea043; }
  .filters { padding: 8px 24px; border-bottom: 1px solid #30363d; display: flex; gap: 12px; align-items: center; }
  .filters input { background: #0d1117; border: 1px solid #30363d; color: #e6edf3; padding: 6px 10px; border-radius: 6px; font: inherit; flex: 1; max-width: 360px; }
  .coverage-block { margin: 8px 24px 0; border: 1px solid #30363d; border-radius: 8px; background: #0d1117; }
  .coverage-block summary { padding: 10px 14px; cursor: pointer; font-weight: 600; color: #c9d1d9; user-select: none; }
  .coverage-block summary:hover { background: #161b22; }
  .coverage-block .coverage-body { padding: 4px 18px 14px; max-height: 70vh; overflow: auto; font-size: 12px; }
  .coverage-block h2 { font-size: 14px; margin: 14px 0 6px; color: #c9d1d9; }
  .coverage-block h3 { font-size: 13px; margin: 10px 0 4px; color: #79c0ff; font-weight: 600; }
  .coverage-block p { color: #8b949e; margin: 4px 0; line-height: 1.5; }
  .coverage-block ul { margin: 4px 0; padding-left: 20px; color: #c9d1d9; }
  .coverage-block li { margin: 2px 0; }
  .coverage-block code { background: #161b22; padding: 1px 4px; border-radius: 3px; font-size: 11px; }
  .coverage-block del { color: #f87171; }
  table.cov { border-collapse: collapse; margin: 6px 0; font-size: 11px; }
  table.cov th, table.cov td { border: 1px solid #30363d; padding: 4px 8px; text-align: left; }
  table.cov th { background: #161b22; color: #c9d1d9; font-weight: 600; }
  table.cov td:first-child { color: #79c0ff; white-space: nowrap; }
  .summary { padding: 12px 24px; display: flex; gap: 24px; font-size: 13px; border-bottom: 1px solid #30363d; }
  .summary .pill { padding: 4px 10px; border-radius: 999px; }
  .pill.pass { background: #1a4731; color: #4ade80; }
  .pill.fail { background: #5b1a1a; color: #f87171; }
  .pill.total { background: #1f2933; color: #93c5fd; }
  main { padding: 16px 24px 64px; }
  .req { border: 1px solid #30363d; border-radius: 8px; margin-bottom: 12px; overflow: hidden; }
  .req-head { padding: 10px 14px; display: grid; grid-template-columns: 60px 1fr auto auto; gap: 12px; align-items: center; cursor: pointer; }
  .req-head:hover { background: #161b22; }
  .req.failed .req-head { background: #2d0f12; }
  .req.failed .req-head:hover { background: #3a1418; }
  .method { font-family: ui-monospace, monospace; font-weight: 700; font-size: 11px; padding: 3px 8px; border-radius: 4px; text-align: center; }
  .method.GET { background: #0d3a2a; color: #4ade80; }
  .method.POST { background: #14305b; color: #93c5fd; }
  .method.DELETE { background: #5b1a1a; color: #f87171; }
  .name { font-weight: 600; font-size: 14px; }
  .name .folder { font-weight: 400; color: #8b949e; font-size: 12px; }
  .url { color: #8b949e; font-family: ui-monospace, monospace; font-size: 12px; word-break: break-all; }
  .status { font-family: ui-monospace, monospace; font-size: 12px; padding: 3px 8px; border-radius: 4px; min-width: 48px; text-align: center; }
  .status.ok { background: #0d3a2a; color: #4ade80; }
  .status.err { background: #5b1a1a; color: #f87171; }
  .status.zero { background: #3a3a1a; color: #facc15; }
  .req-body { padding: 14px; border-top: 1px solid #30363d; display: none; background: #0d1117; }
  .req.open .req-body { display: block; }
  .panel { margin-bottom: 14px; }
  .panel h3 { font-size: 12px; color: #8b949e; text-transform: uppercase; letter-spacing: 0.5px; margin: 0 0 6px; }
  pre { background: #161b22; border: 1px solid #30363d; padding: 10px; border-radius: 6px; overflow-x: auto; font-size: 12px; line-height: 1.5; max-height: 480px; white-space: pre-wrap; word-break: break-word; }
  .assertion { font-size: 13px; padding: 4px 0; }
  .assertion .ok { color: #4ade80; }
  .assertion .fail { color: #f87171; }
  .row { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; }
  @media (max-width: 900px) { .row { grid-template-columns: 1fr; } }
  .resend-row { display: flex; gap: 8px; align-items: center; }
  .resend-row .resend-status { font-size: 12px; color: #8b949e; }
  .header-line { font-family: ui-monospace, monospace; font-size: 12px; color: #c9d1d9; padding: 1px 0; }
  .header-line .k { color: #79c0ff; }
</style>
</head>
<body>
<header>
  <div style="display:flex;align-items:center;">
    <h1>Bifrost Provider Harness</h1>
    <span class="meta" id="meta"></span>
  </div>
  <div class="controls">
    <button id="expand-all">Expand All</button>
    <button id="collapse-all">Collapse All</button>
    <button id="close-btn" class="danger">Close</button>
  </div>
</header>
<div class="filters">
  <input id="filter" placeholder="filter by name, folder, url, or status..." />
  <label style="font-size:12px;color:#8b949e;"><input id="only-failed" type="checkbox" /> Only failed</label>
</div>
<div class="summary" id="summary"></div>
<details class="coverage-block" open>
  <summary>Coverage matrices &amp; gaps</summary>
  <div class="coverage-body">${COVERAGE_HTML}</div>
</details>
<main id="list"></main>
<script>
let items = [];
const list = document.getElementById('list');
const filterInput = document.getElementById('filter');
const onlyFailed = document.getElementById('only-failed');

async function load() {
  const r = await fetch('/api/report');
  items = await r.json();
  document.getElementById('meta').textContent = items.length + ' requests';
  renderSummary();
  render();
}

function renderSummary() {
  const total = items.length;
  const failed = items.filter(i => i.failed).length;
  const passed = total - failed;
  document.getElementById('summary').innerHTML =
    '<span class="pill total">Total ' + total + '</span>' +
    '<span class="pill pass">Passed ' + passed + '</span>' +
    '<span class="pill fail">Failed ' + failed + '</span>';
}

function statusClass(code) {
  if (code === 0) return 'zero';
  return code >= 400 ? 'err' : 'ok';
}

function escape(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

// Pretty-print JSON bodies for readability; fall back to raw text for non-JSON.
function prettyBody(s) {
  if (!s) return '(empty)';
  const trimmed = String(s).trim();
  if (!trimmed) return '(empty)';
  // Try JSON
  if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
    try { return JSON.stringify(JSON.parse(trimmed), null, 2); } catch (e) { /* fall through */ }
  }
  // Try multi-line SSE: split on newlines for readability
  if (trimmed.indexOf('\\ndata:') !== -1 || trimmed.indexOf('event:') === 0) return trimmed;
  return trimmed;
}

function render() {
  const q = filterInput.value.toLowerCase();
  const ff = onlyFailed.checked;
  const shown = items.filter(i =>
    (!ff || i.failed) &&
    (!q || (i.name + ' ' + i.folder + ' ' + i.url + ' ' + i.respCode).toLowerCase().includes(q))
  );
  list.innerHTML = shown.map((i) => {
    const assertions = (i.assertions || []).map(a =>
      '<div class="assertion"><span class="' + (a.passed ? 'ok' : 'fail') + '">' + (a.passed ? '✓' : '✗') + '</span> ' + escape(a.name) +
      (a.error ? ' <span class="fail">- ' + escape(a.error) + '</span>' : '') + '</div>').join('');
    return (
      '<div class="req ' + (i.failed ? 'failed' : '') + '" data-idx="' + i.idx + '">' +
      '<div class="req-head" onclick="this.parentElement.classList.toggle(\\'open\\')">' +
        '<div class="method ' + escape(i.method) + '">' + escape(i.method) + '</div>' +
        '<div><div class="name">' + escape(i.name) + ' <span class="folder">- ' + escape(i.folder) + '</span></div><div class="url">' + escape(i.url) + '</div></div>' +
        '<div class="status ' + statusClass(i.respCode) + '">' + (i.respCode || 'ERR') + '</div>' +
        '<div style="font-size:11px;color:#8b949e;">' + (i.respTimeMs || 0) + 'ms</div>' +
      '</div>' +
      '<div class="req-body">' +
        '<div class="panel"><h3>Assertions</h3>' + (assertions || '<em>(none)</em>') + '</div>' +
        '<div class="panel resend-row">' +
          '<button class="primary" onclick="resend(' + i.idx + ', this)">Resend</button>' +
          '<button onclick="copyCurl(' + i.idx + ', this)">Copy curl</button>' +
          '<span class="resend-status" id="resend-status-' + i.idx + '"></span>' +
        '</div>' +
        '<div class="row">' +
          '<div class="panel"><h3>Request Body</h3><pre>' + escape(prettyBody(i.reqBody)) + '</pre></div>' +
          '<div class="panel" id="resp-panel-' + i.idx + '"><h3>Response Body</h3><pre>' + escape(prettyBody(i.respBody)) + '</pre></div>' +
        '</div>' +
      '</div>' +
      '</div>'
    );
  }).join('');
}

async function resend(idx, btn) {
  const item = items[idx];
  const statusEl = document.getElementById('resend-status-' + idx);
  btn.disabled = true;
  statusEl.textContent = 'sending...';
  try {
    const r = await fetch('/api/resend', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        method: item.method,
        url: item.url,
        headers: item.reqHeaders,
        body: item.reqBody,
      })
    });
    const data = await r.json();
    statusEl.textContent = 'status ' + data.status + ' - ' + data.elapsedMs + 'ms';
    document.getElementById('resp-panel-' + idx).innerHTML =
      '<h3>Response Body (re-sent at ' + new Date().toLocaleTimeString() + ')</h3><pre>' + escape(prettyBody(data.body)) + '</pre>';
  } catch (err) {
    statusEl.textContent = 'error: ' + err.message;
  } finally {
    btn.disabled = false;
  }
}

// shell-quote a string by wrapping in single quotes and escaping any single quotes inside
function shq(s) {
  if (s == null) return "''";
  return "'" + String(s).replace(/'/g, "'\\\\''") + "'";
}

function buildCurl(item) {
  const parts = ['curl', '-i', '-X', item.method, shq(item.url)];
  for (const h of (item.reqHeaders || [])) {
    if (h.disabled) continue;
    parts.push('\\\\\\n  -H', shq(h.key + ': ' + h.value));
  }
  if (item.reqBody && !['GET', 'HEAD'].includes((item.method || '').toUpperCase())) {
    parts.push('\\\\\\n  --data-raw', shq(item.reqBody));
  }
  return parts.join(' ');
}

async function copyCurl(idx, btn) {
  const item = items[idx];
  const cmd = buildCurl(item);
  const original = btn.textContent;
  try {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      await navigator.clipboard.writeText(cmd);
    } else {
      const ta = document.createElement('textarea');
      ta.value = cmd;
      ta.style.position = 'fixed'; ta.style.opacity = '0';
      document.body.appendChild(ta); ta.select(); document.execCommand('copy'); ta.remove();
    }
    btn.textContent = 'Copied!';
  } catch (err) {
    btn.textContent = 'Copy failed';
    console.error(err);
  }
  setTimeout(() => { btn.textContent = original; }, 1500);
}

document.getElementById('expand-all').onclick = () => document.querySelectorAll('.req').forEach(r => r.classList.add('open'));
document.getElementById('collapse-all').onclick = () => document.querySelectorAll('.req').forEach(r => r.classList.remove('open'));
document.getElementById('close-btn').onclick = async () => {
  if (!confirm('Close the viewer? This will release the make recipe to clean up.')) return;
  await fetch('/api/close', { method: 'POST' });
  document.body.innerHTML = '<div style="padding:48px;text-align:center;color:#8b949e;">Viewer closed. You can close this tab.</div>';
};
filterInput.oninput = render;
onlyFailed.onchange = render;
load();
</script>
</body>
</html>`;

const items = summarize();
const allowedTargets = new Set(
  items.map((i) => `${String(i.method || "GET").toUpperCase()} ${i.url}`)
);

const server = createServer(async (req, res) => {
  const u = new URL(req.url, `http://localhost:${PORT}`);
  if (req.method === "GET" && u.pathname === "/") {
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end(VIEWER_HTML);
    return;
  }
  if (req.method === "GET" && u.pathname === "/api/report") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(items));
    return;
  }
  if (req.method === "POST" && u.pathname === "/api/resend") {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    try {
      const { method, url, headers, body } = JSON.parse(raw);
      const normalizedMethod = String(method || "GET").toUpperCase();
      if (!/^https?:\/\//i.test(String(url || ""))) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: "invalid url scheme" }));
        return;
      }
      if (!allowedTargets.has(`${normalizedMethod} ${url}`)) {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: "request target not in harness report" }));
        return;
      }
      const headerObj = {};
      for (const h of headers || []) {
        if (!h.disabled) headerObj[h.key] = h.value;
      }
      const start = Date.now();
      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), 120000);
      try {
        const r = await fetch(url, {
          method: normalizedMethod,
          headers: headerObj,
          body: ["GET", "HEAD"].includes(normalizedMethod) ? undefined : body,
          signal: controller.signal,
        });
        const text = await r.text();
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ status: r.status, body: text, elapsedMs: Date.now() - start }));
      } finally {
        clearTimeout(timer);
      }
    } catch (e) {
      if (e.name === "AbortError") {
        res.writeHead(504, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: "resend timed out after 120s" }));
      } else {
        res.writeHead(500, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: e.message }));
      }
    }
    return;
  }
  if (req.method === "POST" && u.pathname === "/api/close") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ ok: true }));
    setTimeout(() => process.exit(0), 100);
    return;
  }
  res.writeHead(404);
  res.end("not found");
});

server.on("error", (err) => {
  if (err.code === "EADDRINUSE") {
    console.error(`[harness-viewer] port ${PORT} is already in use.`);
    console.error(`[harness-viewer] free it with: lsof -ti tcp:${PORT} | xargs kill`);
    console.error(`[harness-viewer] or rerun with: make run-provider-harness-test VIEWER_PORT=<other>`);
  } else {
    console.error(`[harness-viewer] server error:`, err.message);
  }
  process.exit(1);
});

server.listen(PORT, () => {
  console.log(`[harness-viewer] open  http://localhost:${PORT}  to inspect ${items.length} requests`);
  console.log(`[harness-viewer] click "Close" in the UI (or Ctrl-C) when done.`);
});

const shutdown = () => {
  server.close(() => process.exit(0));
  setTimeout(() => process.exit(0), 500).unref();
};
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
process.on("SIGHUP", shutdown);
