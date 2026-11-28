import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// Anthropic retired the Claude 3 models, so a copied example that still names one fails at the first request.
const RETIRED_CLAUDE_MODEL = /claude-3(?:-5|-7)?-(?:opus|sonnet|haiku)/;

describe("logs empty state examples", () => {
	it("do not name retired Claude model IDs", () => {
		const source = readFileSync(new URL("./emptyState.tsx", import.meta.url), "utf8");
		expect(source.match(RETIRED_CLAUDE_MODEL)).toBeNull();
	});
});
