import { describe, expect, it } from "vitest";
import { isOdinCommandQuery, matchOdinCommands, resolveOdinCommand } from "./odinCommands";

describe("isOdinCommandQuery", () => {
	it("opens on a lone slash and while typing a name", () => {
		expect(isOdinCommandQuery("/")).toBe(true);
		expect(isOdinCommandQuery("/cle")).toBe(true);
	});

	// Someone asking about a route is not reaching for a command, and popping a
	// menu over their question mid-sentence is worse than having no commands.
	it("stays shut for a slash inside a question", () => {
		expect(isOdinCommandQuery("what is the p99 for /v1/chat/completions?")).toBe(false);
		expect(isOdinCommandQuery("/clear the logs table")).toBe(false);
		expect(isOdinCommandQuery("")).toBe(false);
	});
});

describe("matchOdinCommands", () => {
	it("lists everything on a bare slash", () => {
		expect(matchOdinCommands("/").map((command) => command.name)).toContain("clear");
	});

	it("filters by prefix", () => {
		expect(matchOdinCommands("/cl")).toHaveLength(1);
		expect(matchOdinCommands("/zz")).toHaveLength(0);
	});
});

describe("resolveOdinCommand", () => {
	it("resolves an exact command", () => {
		expect(resolveOdinCommand("/clear")?.id).toBe("clear");
		expect(resolveOdinCommand("  /CLEAR  ")?.id).toBe("clear");
	});

	// Treating this as a command would silently discard the rest of what was
	// written, which is worse than not offering the shortcut at all.
	it("does not resolve a command with trailing text", () => {
		expect(resolveOdinCommand("/clear the logs table")).toBeNull();
	});

	it("ignores ordinary questions", () => {
		expect(resolveOdinCommand("what did we spend?")).toBeNull();
	});
});