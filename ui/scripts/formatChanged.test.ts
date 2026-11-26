import { execFileSync, spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

const SCRIPT = path.resolve(__dirname, "formatChanged.sh");
const BIN = path.resolve(__dirname, "../node_modules/.bin");
const UNFORMATTED = "const   value=1\n";
const FORMATTED = "const value = 1;\n";

let repo: string;

const git = (...args: string[]) =>
	execFileSync("git", ["-c", "user.name=test", "-c", "user.email=test@example.com", ...args], { cwd: repo });
const read = (file: string) => fs.readFileSync(path.join(repo, file), "utf8");
const format = (...flags: string[]) =>
	spawnSync("sh", [SCRIPT, ...flags], { cwd: repo, encoding: "utf8", env: { ...process.env, PATH: `${BIN}:${process.env.PATH}` } });

beforeAll(() => {
	repo = fs.mkdtempSync(path.join(os.tmpdir(), "format-changed-"));
	git("init", "-q");
	fs.writeFileSync(path.join(repo, "untouched.ts"), UNFORMATTED);
	fs.writeFileSync(path.join(repo, "edited.ts"), FORMATTED);
	git("add", ".");
	git("commit", "-q", "-m", "init");
	fs.writeFileSync(path.join(repo, "edited.ts"), UNFORMATTED);
	fs.writeFileSync(path.join(repo, "my component.ts"), UNFORMATTED);
	fs.writeFileSync(path.join(repo, "notes.md"), "# keep   as is\n");
});

afterAll(() => {
	fs.rmSync(repo, { recursive: true, force: true });
});

describe("formatChanged.sh", () => {
	it("fails the check for changed files without rewriting them", () => {
		const result = format("--check");
		expect(result.status).not.toBe(0);
		expect(read("edited.ts")).toBe(UNFORMATTED);
	});

	it("formats edited and untracked files, including names with spaces, and leaves the rest alone", () => {
		expect(format().status).toBe(0);
		expect(read("edited.ts")).toBe(FORMATTED);
		expect(read("my component.ts")).toBe(FORMATTED);
		expect(read("untouched.ts")).toBe(UNFORMATTED);
		expect(read("notes.md")).toBe("# keep   as is\n");
	});

	it("passes the check once the changed files are formatted", () => {
		expect(format("--check").status).toBe(0);
	});

	it("does nothing when no file changed", () => {
		git("add", ".");
		git("commit", "-q", "-m", "format");
		fs.writeFileSync(path.join(repo, "untouched.ts"), UNFORMATTED);
		git("checkout", "--", "untouched.ts");
		expect(format().status).toBe(0);
		expect(read("untouched.ts")).toBe(UNFORMATTED);
	});
});