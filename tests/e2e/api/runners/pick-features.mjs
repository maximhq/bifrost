#!/usr/bin/env node
// Interactive multi-select picker for harness modalities (criss-cross matrix).
//
// Designed to be invoked via $(node pick-features.mjs) - we read keys from
// stdin (raw mode), render the menu to stderr, and write only the final
// selection (comma-separated, lowercase) to stdout so the Makefile can
// capture it cleanly.
//
// Exit codes:
//   0 - selection confirmed (stdout = comma-separated keywords; empty when all selected)
//   1 - user cancelled (Esc / Ctrl+C / q)
//   2 - not running on an interactive TTY (no menu shown; stdout empty)

const FEATURES = [
  { key: "chat", label: "Chat completions (text-only)" },
  { key: "streaming", label: "Streaming responses (SSE)" },
  { key: "embeddings", label: "Embeddings" },
  { key: "audio", label: "Audio (speech / transcription)" },
  { key: "image-gen", label: "Image generation" },
  { key: "tools", label: "Tool / function calling" },
  { key: "vision", label: "Vision (image input)" },
  { key: "json", label: "Structured output (JSON mode / schema)" },
  { key: "reasoning", label: "Reasoning / thinking" },
];

// Need a TTY on both stdin (for keys) and stderr (for menu render). stdout is
// intentionally NOT required - it's the result channel, often piped via $(...).
if (!process.stdin.isTTY || !process.stderr.isTTY) {
  process.exit(2);
}

const writeUI = (s) => process.stderr.write(s);

const selected = new Set(FEATURES.map((f) => f.key));
let cursor = 0;
let firstFrame = true;
let lastLines = 0;

const C = {
  reset: "\x1b[0m",
  bold: "\x1b[1m",
  dim: "\x1b[2m",
  cyan: "\x1b[36m",
  green: "\x1b[32m",
  yellow: "\x1b[33m",
  gray: "\x1b[90m",
};

function render() {
  const lines = [];
  lines.push(`${C.bold}Bifrost harness - pick modalities${C.reset}  ${C.dim}(space toggles, a=all, n=none, enter runs, q=cancel)${C.reset}`);
  lines.push("");
  for (let i = 0; i < FEATURES.length; i++) {
    const f = FEATURES[i];
    const box = selected.has(f.key) ? `${C.green}[x]${C.reset}` : "[ ]";
    const arrow = i === cursor ? `${C.cyan}>${C.reset}` : " ";
    const label = i === cursor ? `${C.bold}${f.label}${C.reset}` : f.label;
    lines.push(`  ${arrow} ${box} ${label}  ${C.gray}${f.key}${C.reset}`);
  }
  lines.push("");
  const n = selected.size;
  const summary = n === FEATURES.length
    ? `${C.dim}All modalities selected (no filter) - all providers will run${C.reset}`
    : n === 0
      ? `${C.yellow}No modalities selected - press space or 'a' to choose at least one${C.reset}`
      : `${C.dim}${n} of ${FEATURES.length} selected: ${[...selected].join(", ")}${C.reset}`;
  lines.push(summary);

  let out = "";
  if (firstFrame) {
    writeUI("\x1b[?25l");
    firstFrame = false;
  } else if (lastLines > 0) {
    out += `\x1b[${lastLines}A\x1b[0J`;
  }
  out += lines.join("\n") + "\n";
  writeUI(out);
  lastLines = lines.length;
}

function restoreTty() {
  try { process.stdin.setRawMode(false); } catch {}
  writeUI("\x1b[?25h");
}

function commit() {
  restoreTty();
  process.stdin.pause();
  if (selected.size === 0) process.exit(1);
  // Emit empty when all are selected so the Makefile takes the no-filter path.
  if (selected.size < FEATURES.length) {
    process.stdout.write([...selected].join(","));
  }
  process.exit(0);
}

function cancel() {
  restoreTty();
  process.stdin.pause();
  process.stderr.write("\n[pick-features] cancelled\n");
  process.exit(1);
}

process.on("SIGINT", cancel);
process.on("SIGTERM", cancel);

process.stdin.setRawMode(true);
process.stdin.resume();
process.stdin.setEncoding("utf8");

process.stdin.on("data", (chunk) => {
  for (const key of splitKeys(chunk)) handleKey(key);
});

function handleKey(key) {
  if (key === "\x1b[A" || key === "k") {
    cursor = (cursor - 1 + FEATURES.length) % FEATURES.length;
    render();
    return;
  }
  if (key === "\x1b[B" || key === "j") {
    cursor = (cursor + 1) % FEATURES.length;
    render();
    return;
  }
  if (key === " ") {
    const k = FEATURES[cursor].key;
    if (selected.has(k)) selected.delete(k);
    else selected.add(k);
    render();
    return;
  }
  if (key === "a") {
    for (const f of FEATURES) selected.add(f.key);
    render();
    return;
  }
  if (key === "n") {
    selected.clear();
    render();
    return;
  }
  if (key === "\r" || key === "\n") {
    if (selected.size === 0) return;
    commit();
    return;
  }
  if (key === "\x1b" || key === "q" || key === "\x03") {
    cancel();
    return;
  }
}

// Terminals batch fast keypresses into a single data chunk. Split on ESC
// boundaries so an arrow-key escape sequence stays atomic.
function splitKeys(chunk) {
  const out = [];
  let i = 0;
  while (i < chunk.length) {
    const ch = chunk[i];
    if (ch === "\x1b") {
      // CSI sequence: ESC [ <bytes> <final-letter>
      if (chunk[i + 1] === "[") {
        let j = i + 2;
        while (j < chunk.length && !/[A-Za-z~]/.test(chunk[j])) j++;
        out.push(chunk.slice(i, j + 1));
        i = j + 1;
        continue;
      }
      // bare ESC
      out.push("\x1b");
      i++;
      continue;
    }
    out.push(ch);
    i++;
  }
  return out;
}

render();
