import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

const SCRIPT = path.resolve(__dirname, "i18nCheck.mjs");

let root: string;

function write(file: string, content: string | object) {
	const target = path.join(root, file);
	fs.mkdirSync(path.dirname(target), { recursive: true });
	fs.writeFileSync(target, typeof content === "string" ? content : `${JSON.stringify(content, null, 2)}\n`);
}

function run(...flags: string[]) {
	let stdout: string;
	let status = 0;
	try {
		stdout = execFileSync("node", [SCRIPT, "--json", ...flags], { env: { ...process.env, I18N_CHECK_ROOT: root }, encoding: "utf8" });
	} catch (error) {
		const failure = error as { stdout: string; status: number };
		stdout = failure.stdout;
		status = failure.status;
	}
	return { ...JSON.parse(stdout), status };
}

const found = (result: any, file: string) => (result.literals[file] ?? []).map((f: any) => `${f.category}:${f.text}`);

beforeAll(() => {
	root = fs.mkdtempSync(path.join(os.tmpdir(), "i18n-check-"));
	write(
		"app/literals.tsx",
		`
		import { z } from "zod";
		const schema = z.object({
			name: z.string().min(1, "Name is required"),
			quoted: z.string().min(1, { "message": "Quoted message" }),
			ok: z.string().min(1, msg("validation.required")),
		});
		export function View() {
			toast("Saved changes");
			toast.success(t("saved"));
			return <p>Hello there</p>;
		}
	`,
	);
	write(
		"app/ignored.tsx",
		`
		export function View() {
			// i18n-ignore
			return <p>Hello there</p>;
		}
	`,
	);
	write(
		"app/trans.tsx",
		`
		export function View() {
			return <Trans i18nKey="named" components={{ name: <strong /> }} />;
		}
	`,
	);
	write(
		"app/uses.tsx",
		`
		export function View() {
			return t("used");
		}
	`,
	);
	write("locales/en/common.json", {
		used: "Used",
		orphan: "Never referenced",
		named: "Use <name>{{name}}</name>",
		literalAngle: "Pass <name> as the argument",
		bold: "Click <b>here</b>",
		list: ["Enabled", "Disabled"],
		blank: "Something",
		day_one: "{{count}} day",
		day_other: "{{count}} days",
	});
	write("locales/de/common.json", {
		used: "Benutzt",
		orphan: "Nie genutzt",
		named: "Verwende {{name}}",
		literalAngle: "Übergib <name> als Argument",
		bold: "Klicke hier",
		list: ["", "Deaktiviert"],
		blank: "",
		day_one: "jeden Tag",
		day_other: "{{count}} Tage",
	});
	// JSON.parse keeps the last duplicate, so the file is written by hand.
	fs.writeFileSync(path.join(root, "locales/en/dup.json"), '{\n  "a": "One",\n  "a": "Two"\n}\n');
	write("locales/de/dup.json", { a: "Eins" });
});

afterAll(() => {
	fs.rmSync(root, { recursive: true, force: true });
});

describe("literal scan", () => {
	const result = () => run("--literals");

	it("flags hardcoded validation messages, including quoted property names", () => {
		const list = found(result(), "app/literals.tsx");
		expect(list).toContain("validation:Name is required");
		expect(list).toContain("validation:Quoted message");
		expect(list).not.toContain("validation:validation.required");
	});

	it("flags bare toast() calls and JSX text but not translated calls", () => {
		const list = found(result(), "app/literals.tsx");
		expect(list).toContain("toast:Saved changes");
		expect(list).toContain("jsx-text:Hello there");
		expect(list.some((entry: string) => entry.includes("saved") && !entry.includes("Saved"))).toBe(false);
	});

	it("honours i18n-ignore", () => {
		expect(found(result(), "app/ignored.tsx")).toEqual([]);
	});
});

describe("locale scan", () => {
	const issues = () => run("--locales").locales.de;

	it("reports a Trans tag that a translation dropped", () => {
		expect(issues().tags.some((entry: string) => entry.startsWith("common:bold"))).toBe(true);
	});

	it("checks named Trans component tags for keys rendered with <Trans components>", () => {
		expect(issues().tags.some((entry: string) => entry.startsWith("common:named"))).toBe(true);
	});

	it("leaves literal angle-bracket text alone", () => {
		expect(issues().tags.some((entry: string) => entry.startsWith("common:literalAngle"))).toBe(false);
	});

	it("reports empty translations, including blank array items", () => {
		expect(issues().empty).toEqual(expect.arrayContaining(["common:blank", "common:list"]));
	});

	it("reports keys defined twice in one file", () => {
		// Duplicates are reported under the language whose file holds them.
		expect(run("--locales").locales.en.duplicates).toContain("dup:a");
	});

	it("lets a singular plural form drop the count", () => {
		const all = Object.values(issues()).flat() as string[];
		expect(all.some((entry) => entry.startsWith("common:day_one"))).toBe(false);
	});

	it("exits non-zero when anything is reported", () => {
		expect(run("--locales").status).toBe(1);
	});
});

describe("unused keys", () => {
	it("lists English keys no source string references", () => {
		const { unused } = run("--unused");
		expect(unused.common).toContain("orphan");
		expect(unused.common).not.toContain("used");
		expect(unused.common).not.toContain("named");
	});
});