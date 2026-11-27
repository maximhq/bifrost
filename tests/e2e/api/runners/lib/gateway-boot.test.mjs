// Unit tests for gateway-boot.mjs. Run directly: `node gateway-boot.test.mjs`. A fake bifrost-http
// stands in for the server: it exits with a scripted startup error until it has been started a
// given number of times, then says it started and waits to be stopped.
import assert from "node:assert";
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { bootGateway, isDatasheetFetchFailure } from "./gateway-boot.mjs";

let passed = 0;
async function test(name, fn) {
	await fn();
	passed++;
	console.log(`  ok - ${name}`);
}

const WORK = mkdtempSync(join(tmpdir(), "gateway-boot-"));
process.on("exit", () => rmSync(WORK, { recursive: true, force: true }));

const DATASHEET_503 = '{"level":"fatal","message":"failed to initialize pricing manager: failed to sync pricing data: failed to download pricing data: HTTP 503"}';
const PARAMS_503 = '{"level":"fatal","message":"failed to sync model parameters data: failed to download model parameters data: HTTP 503"}';

function flakyFake(name, failBoots, failMessage) {
	const bin = join(WORK, name);
	const counter = join(WORK, `${name}.boots`);
	writeFileSync(counter, "0");
	writeFileSync(
		bin,
		`#!/bin/bash
n=$(($(cat ${JSON.stringify(counter)}) + 1)); echo "$n" > ${JSON.stringify(counter)}
if [ "$n" -le ${failBoots} ]; then echo '${failMessage}'; exit 1; fi
echo "successfully started bifrost"
while :; do sleep 0.2; done
`,
	);
	chmodSync(bin, 0o755);
	return { bin, boots: () => Number(readFileSync(counter, "utf8").trim()) };
}

const boot = (fake, attempts = 4) => bootGateway({ binary: fake.bin, logsStore: { type: "sqlite", config: {} }, attempts, retryDelayMs: 0, timeoutMs: 10000 });

await test("isDatasheetFetchFailure matches pricing and model-parameters download failures only", () => {
	assert.ok(isDatasheetFetchFailure(DATASHEET_503));
	assert.ok(isDatasheetFetchFailure(PARAMS_503));
	assert.ok(!isDatasheetFetchFailure('{"level":"fatal","message":"failed to connect to vector store"}'));
	assert.ok(!isDatasheetFetchFailure(undefined));
});

await test("a boot stopped by a failed datasheet download is retried until the server starts", async () => {
	const fake = flakyFake("flaky", 2, DATASHEET_503);
	const gw = await boot(fake);
	await gw.stop();
	assert.equal(fake.boots(), 3);
});

await test("datasheet retries stop after the configured number of boots", async () => {
	const fake = flakyFake("always-503", 99, PARAMS_503);
	await assert.rejects(boot(fake, 3), /exited \(1\) before it was ready/);
	assert.equal(fake.boots(), 3);
});

await test("any other startup failure is thrown at once", async () => {
	const fake = flakyFake("other-fatal", 99, '{"level":"fatal","message":"failed to connect to vector store"}');
	await assert.rejects(boot(fake), /failed to connect to vector store/);
	assert.equal(fake.boots(), 1);
});

console.log(`\n${passed} passed`);
