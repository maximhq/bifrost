import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// Charts are memoized, so one that formats axis ticks with the active language must subscribe to language changes itself.
const chartFiles = fs.readdirSync(__dirname).filter((name) => name.endsWith(".tsx"));

describe("memoized charts", () => {
	it("finds the chart implementations", () => {
		expect(
			chartFiles.filter((name) => /^function \w+Impl\(/m.test(fs.readFileSync(path.join(__dirname, name), "utf8"))).length,
		).toBeGreaterThan(10);
	});

	for (const name of chartFiles) {
		const source = fs.readFileSync(path.join(__dirname, name), "utf8");
		const impl = source.match(/^function \w+Impl\([\s\S]*?\n\}\n/m);
		if (!impl) continue;
		it(`${name} re-renders on a language change`, () => {
			expect(impl[0]).toMatch(/\buseLocale\(\)/);
		});
	}
});