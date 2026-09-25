import { describe, expect, it } from "vitest";
import { isWarpCommandQuery, matchWarpCommands, resolveWarpCommand } from "./warpCommands";

describe("isWarpCommandQuery", () => {
	it("opens on a lone slash and while typing a name", () => {
		expect(isWarpCommandQuery("/")).toBe(true);
		expect(isWarpCommandQuery("/cle")).toBe(true);
	});

	// Someone asking about a route is not reaching for a command, and popping a
	// menu over their question mid-sentence is worse than having no commands.
	it("stays shut for a slash inside a question", () => {
		expect(isWarpCommandQuery("what is the p99 for /v1/chat/completions?")).toBe(false);
		expect(isWarpCommandQuery("/clear the logs table")).toBe(false);
		expect(isWarpCommandQuery("")).toBe(false);
	});
});

describe("matchWarpCommands", () => {
	it("lists everything on a bare slash", () => {
		expect(matchWarpCommands("/").map((command) => command.name)).toContain("clear");
	});

	it("filters by prefix", () => {
		expect(matchWarpCommands("/cl")).toHaveLength(1);
		expect(matchWarpCommands("/zz")).toHaveLength(0);
	});
});

describe("resolveWarpCommand", () => {
	it("resolves an exact command", () => {
		expect(resolveWarpCommand("/clear")?.id).toBe("clear");
		expect(resolveWarpCommand("  /CLEAR  ")?.id).toBe("clear");
	});

	// Treating this as a command would silently discard the rest of what was
	// written, which is worse than not offering the shortcut at all.
	it("does not resolve a command with trailing text", () => {
		expect(resolveWarpCommand("/clear the logs table")).toBeNull();
	});

	it("ignores ordinary questions", () => {
		expect(resolveWarpCommand("what did we spend?")).toBeNull();
	});
});