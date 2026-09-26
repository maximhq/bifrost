import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const collection = JSON.parse(
  readFileSync(fileURLToPath(new URL("provider-harness.json", import.meta.url)), "utf8"),
);
const folder = collection.item.find((item) => item.name.includes("stdio-env-regression"));
const request = folder.item[0];
const prerequest = request.event
  .find((event) => event.listen === "prerequest")
  .script.exec.join("\n");

function commandInBody(override) {
  const collectionVariables = new Map();
  const pm = {
    collectionVariables: { set: (name, value) => collectionVariables.set(name, value) },
    variables: {
      get: (name) =>
        name === "mcpAdminSession" ? "test-session" : override?.[name] ?? collectionVariables.get(name),
      replaceIn: () => "test-guid",
    },
  };
  new Function("pm", prerequest)(pm);
  const body = request.request.body.raw.replace(/\{\{([^{}]+)\}\}/g, (_, name) =>
    String(pm.variables.get(name)),
  );
  return JSON.parse(body).stdio_config.command;
}

assert.equal(commandInBody(), "node");
const windowsNodePath = "C:\\Program Files\\nodejs\\node.exe";
assert.equal(commandInBody({ mcpStdioNodeCommand: windowsNodePath }), windowsNodePath);
console.log("stdio env command JSON interpolation passed");
