#!/usr/bin/env node
// i18n coverage check for the UI.
//
//   npm run i18n:check                 # both checks, exits 1 on any finding
//   npm run i18n:check -- --literals   # only hardcoded user-facing strings
//   npm run i18n:check -- --locales    # only locale catalogs vs English
//   npm run i18n:check -- --strict     # also fail on "review" findings (object fields)
//   npm run i18n:check -- --untranslated   # also flag values identical to English
//   npm run i18n:check -- --summary    # counts per file / locale, no line listing
//   npm run i18n:check -- --json       # machine-readable output
//   npm run i18n:check -- app/workspace/logs   # limit the literal scan to paths
//
// Literal findings come in two tiers:
//   untranslated  JSX text, {"..."} children, text props, toast messages. Always rendered.
//   review        label/title/description fields in object literals. Often rendered raw,
//                 but sometimes dead English next to a dynamic t() lookup, so a human decides.
//
// Suppress a literal on purpose with `// i18n-ignore` (or `{/* i18n-ignore */}`)
// on the same line or the line above.

import fs from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const ts = require("typescript");

const UI_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
// Enterprise catalogs arrive through the app/enterprise symlink and are checked on their own.
const LOCALE_ROOTS = [path.join(UI_ROOT, "locales"), path.join(UI_ROOT, "app", "enterprise", "locales")].filter((dir) => fs.existsSync(dir));
const SOURCE_LANG = "en";
// Every root must cover every language the app ships, so a root with no `ja` directory is reported, not skipped.
const SUPPORTED_LANGS = fs
	.readdirSync(path.join(UI_ROOT, "locales"), { withFileTypes: true })
	.filter((entry) => entry.isDirectory() && entry.name !== SOURCE_LANG)
	.map((entry) => entry.name);
const SCAN_DIRS = ["app", "components", "hooks", "lib"];
const SKIP_PATH = /(\.test\.|\.spec\.|\.d\.ts$|\/lib\/i18n\/|\/node_modules\/)/;
// Developer-only tooling (pool debug profiler), intentionally English.
const SKIP_FILES = new Set(["app/pprof/page.tsx", "components/devProfiler.tsx"]);

// Props whose string value reaches the user as text.
const TEXT_PROPS = new Set([
	"alt",
	"aria-description",
	"aria-label",
	"aria-placeholder",
	"aria-roledescription",
	"aria-valuetext",
	"cancelText",
	"confirmText",
	"description",
	"emptyMessage",
	"emptyText",
	"heading",
	"helperText",
	"label",
	"placeholder",
	"subtitle",
	"text",
	"title",
	"tooltip",
]);
// Object fields in option/tab/column arrays that end up rendered.
const TEXT_FIELDS = new Set(["label", "title", "description", "placeholder", "tooltip", "helperText", "emptyMessage", "disabledReason"]);
const TOAST_METHODS = new Set(["success", "error", "info", "warning", "message", "loading"]);
// JSX parents whose text is code or data, not prose. `title` is the SVG element (icon names).
const RAW_TEXT_TAGS = new Set(["code", "pre", "kbd", "samp", "style", "script", "title"]);
const REVIEW_CATEGORIES = new Set(["object-field"]);

const args = process.argv.slice(2);
const flag = (name) => args.includes(`--${name}`);
const onlyLiterals = flag("literals");
const onlyLocales = flag("locales");
const runLiterals = !onlyLocales || onlyLiterals;
const runLocales = !onlyLiterals || onlyLocales;
const pathFilters = args.filter((arg) => !arg.startsWith("--")).map((arg) => path.resolve(UI_ROOT, arg));

// A string is prose when it has a letter and is not an identifier, token, path, or URL.
// `maybeIdentifier` is true only where the AST allows an enum value or key (object fields);
// JSX text and text props are always rendered, so a lowercase word like "save" is prose there.
function isProse(raw, maybeIdentifier = false) {
	const text = raw.replace(/\s+/g, " ").trim();
	if (!/\p{L}/u.test(text)) return false;
	if (!text.split(/[\s,;]+/).some((word) => /^\p{L}{2,}[.!?:]?$/u.test(word))) return false; // no plain word: "├─ add.py", "v1.2"
	if (/^(https?:|mailto:|\/|\.\/|#|data:)/.test(text)) return false;
	if (!text.includes(" ")) {
		if (/[_./:@\\=<>{}[\]$]|\d/.test(text)) return false; // tokens, keys, versions, css
		if (/^[A-Z0-9-]+$/.test(text) && text.length <= 5) return false; // acronyms: API, URL, JSON
		if (maybeIdentifier && /^[a-z]/.test(text)) return false; // identifiers and enum values
		if (/^[a-z]+[A-Z]/.test(text)) return false; // camelCase
	}
	return true;
}

function hasIgnoreComment(sourceFile, node) {
	const { line } = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile));
	const lines = sourceFile.text.split("\n");
	return [lines[line], lines[line - 1]].some((l) => l !== undefined && l.includes("i18n-ignore"));
}

// Peels JSX expression braces and parentheses to the expression inside.
function unwrap(node) {
	let current = node;
	while (current && (ts.isJsxExpression(current) || ts.isParenthesizedExpression(current))) current = current.expression;
	return current;
}

// The string-valued nodes a text position can render: literals, templates with prose, and both
// branches of a conditional.
function stringLeaves(node) {
	const inner = unwrap(node);
	if (!inner) return [];
	if (ts.isStringLiteral(inner) || ts.isNoSubstitutionTemplateLiteral(inner)) return [inner];
	if (ts.isTemplateExpression(inner)) return /\p{L}{2,}/u.test(literalText(inner)) ? [inner] : [];
	if (ts.isConditionalExpression(inner)) return [...stringLeaves(inner.whenTrue), ...stringLeaves(inner.whenFalse)];
	return [];
}

// Prose detection reads only the literal parts of a template; the `${...}` expressions are not text.
function literalText(node) {
	if (ts.isTemplateExpression(node)) return [node.head.text, ...node.templateSpans.map((span) => span.literal.text)].join(" ");
	return node.text;
}

function jsxTagName(node) {
	const opening = ts.isJsxElement(node) ? node.openingElement : ts.isJsxSelfClosingElement(node) ? node : undefined;
	return opening ? opening.tagName.getText() : undefined;
}

// A string that is the argument of t()/i18n.t()/tc() etc. is already translated (it is a key).
function isTranslationCall(node) {
	if (!ts.isCallExpression(node)) return false;
	const callee = node.expression.getText();
	return /(^|\.)(t|tc|tCommon|tcpx|complexityT|i18nT)$/.test(callee) || /^t[A-Z]\w*$/.test(callee);
}

function insideTranslationOrType(node) {
	for (let current = node.parent; current; current = current.parent) {
		if (isTranslationCall(current)) return true;
		if (ts.isTypeNode(current) || ts.isImportDeclaration(current) || ts.isExportDeclaration(current)) return true;
		if (ts.isFunctionLike(current) || ts.isSourceFile(current)) return false;
	}
	return false;
}

// { label: "Last hour", labelKey: "timePeriods.1h" }: the literal is only a fallback.
function hasKeySibling(property) {
	const object = property.parent;
	if (!object || !ts.isObjectLiteralExpression(object)) return false;
	return object.properties.some((other) => other !== property && other.name && /Key$/.test(other.name.getText()));
}

function scanFile(filePath) {
	const text = fs.readFileSync(filePath, "utf8");
	const kind = filePath.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
	const sourceFile = ts.createSourceFile(filePath, text, ts.ScriptTarget.Latest, true, kind);
	const findings = [];

	// `node` is a string leaf (see stringLeaves) or JSX text.
	const report = (node, category) => {
		const value = ts.isJsxText(node) ? node.text : literalText(node);
		if (!isProse(value, category === "object-field") || hasIgnoreComment(sourceFile, node)) return;
		const { line, character } = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile));
		const tier = REVIEW_CATEGORIES.has(category) ? "review" : "untranslated";
		const shown = ts.isTemplateExpression(node) ? node.getText(sourceFile) : value;
		findings.push({ line: line + 1, column: character + 1, tier, category, text: shown.replace(/\s+/g, " ").trim() });
	};

	const visit = (node) => {
		// <p>Some text</p>
		if (ts.isJsxText(node)) {
			const parentTag = node.parent ? jsxTagName(node.parent) : undefined;
			if (!parentTag || !RAW_TEXT_TAGS.has(parentTag)) report(node, "jsx-text");
		}
		// <p>{"Some text"}</p>
		else if (ts.isJsxExpression(node) && node.parent && (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent))) {
			const parentTag = jsxTagName(node.parent);
			if (!parentTag || !RAW_TEXT_TAGS.has(parentTag)) for (const leaf of stringLeaves(node.expression)) report(leaf, "jsx-expression");
		}
		// <Input placeholder="Search..." />
		else if (ts.isJsxAttribute(node) && TEXT_PROPS.has(node.name.getText())) {
			for (const leaf of stringLeaves(node.initializer)) report(leaf, "jsx-prop");
		}
		// toast.success("Saved")
		else if (
			ts.isCallExpression(node) &&
			ts.isPropertyAccessExpression(node.expression) &&
			node.expression.expression.getText() === "toast" &&
			TOAST_METHODS.has(node.expression.name.text)
		) {
			for (const leaf of stringLeaves(node.arguments[0])) report(leaf, "toast");
		}
		// { label: "Network" } in option, tab, and column definitions
		else if (ts.isPropertyAssignment(node) && TEXT_FIELDS.has(node.name.getText().replace(/["']/g, ""))) {
			if (!insideTranslationOrType(node) && !hasKeySibling(node)) for (const leaf of stringLeaves(node.initializer)) report(leaf, "object-field");
		}
		ts.forEachChild(node, visit);
	};
	visit(sourceFile);
	return findings;
}

function isDirectory(target) {
	try {
		return fs.statSync(target).isDirectory();
	} catch {
		return false;
	}
}

function walk(dir, out = []) {
	if (!fs.existsSync(dir)) return out;
	for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
		const full = path.join(dir, entry.name);
		// app/enterprise is a symlink, which Dirent does not report as a directory.
		// A link whose checkout was removed dangles; the build treats that as an OSS build, so skip it.
		const isDir = entry.isDirectory() || (entry.isSymbolicLink() && isDirectory(full));
		if (isDir) walk(full, out);
		else if (/\.(tsx?|jsx?)$/.test(entry.name) && !SKIP_PATH.test(full) && !SKIP_FILES.has(path.relative(UI_ROOT, full))) out.push(full);
	}
	return out;
}

function checkLiterals() {
	const roots = pathFilters.length > 0 ? pathFilters : SCAN_DIRS.map((dir) => path.join(UI_ROOT, dir));
	const files = roots.flatMap((root) => (fs.statSync(root).isFile() ? [root] : walk(root)));
	const byFile = {};
	for (const file of files) {
		const findings = scanFile(file);
		if (findings.length > 0) byFile[path.relative(UI_ROOT, file)] = findings;
	}
	return byFile;
}

// ---------------------------------------------------------------------------
// Locale catalogs
// ---------------------------------------------------------------------------

function flatten(value, prefix = "", out = new Map()) {
	if (value && typeof value === "object" && !Array.isArray(value)) {
		for (const [key, child] of Object.entries(value)) flatten(child, prefix ? `${prefix}.${key}` : key, out);
	} else {
		out.set(prefix, value);
	}
	return out;
}

// Interpolation names in a catalog value. Arrays and objects compare element by element.
function placeholders(value) {
	if (typeof value === "string") return [...new Set([...value.matchAll(/\{\{\s*([\w.]+)[^}]*\}\}/g)].map((m) => m[1]))].sort().join(",");
	if (Array.isArray(value)) return value.map(placeholders).join("|");
	if (value && typeof value === "object")
		return Object.keys(value)
			.sort()
			.map((key) => `${key}:${placeholders(value[key])}`)
			.join("|");
	return "";
}
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/;
const isPluralKey = (key) => PLURAL_SUFFIX.test(key);
const baseKey = (key) => key.replace(PLURAL_SUFFIX, "");
const suffixOf = (key) => PLURAL_SUFFIX.exec(key)?.[1];

// i18next resolves a counted key to the form its language's plural rules pick, so a translation
// needs those forms (ja: other; ru: one, few, many, other), whatever English has. Languages whose
// only extra form is `many` (es, fr, it, pt) use it for millions, which UI counts never reach.
function pluralCategories(lang) {
	let categories;
	try {
		categories = new Intl.PluralRules(lang).resolvedOptions().pluralCategories;
	} catch {
		return ["one", "other"];
	}
	return categories.includes("few") ? categories : categories.filter((category) => category !== "many");
}

function checkLocales() {
	const report = {};
	for (const root of LOCALE_ROOTS) checkLocaleRoot(root, report);
	return report;
}

function checkLocaleRoot(localesDir, report) {
	const namespaces = fs.readdirSync(path.join(localesDir, SOURCE_LANG)).filter((name) => name.endsWith(".json"));
	for (const lang of SUPPORTED_LANGS) {
		const issues = (report[lang] ??= { missing: [], extra: [], shape: [], placeholders: [], untranslated: [] });
		for (const ns of namespaces) {
			const nsName = ns.replace(/\.json$/, "");
			const source = flatten(JSON.parse(fs.readFileSync(path.join(localesDir, SOURCE_LANG, ns), "utf8")));
			const targetPath = path.join(localesDir, lang, ns);
			if (!fs.existsSync(targetPath)) {
				issues.missing.push(`${nsName}:* (file missing)`);
				continue;
			}
			const target = flatten(JSON.parse(fs.readFileSync(targetPath, "utf8")));
			const sourcePluralBases = new Set([...source.keys()].filter(isPluralKey).map(baseKey));
			const categories = pluralCategories(lang);
			const compare = (qualified, value, translated) => {
				if (Array.isArray(value) !== Array.isArray(translated) || typeof value !== typeof translated) issues.shape.push(qualified);
				else if (Array.isArray(value) && value.length !== translated.length)
					issues.shape.push(`${qualified} (en: ${value.length} items vs ${lang}: ${translated.length})`);
				else if (placeholders(value) !== placeholders(translated))
					issues.placeholders.push(`${qualified} (en: {${placeholders(value)}} vs ${lang}: {${placeholders(translated)}})`);
				else if (flag("untranslated") && typeof value === "string" && value === translated && isProse(value))
					issues.untranslated.push(qualified);
			};
			// Ordinary keys must exist exactly; a plural sibling (item_one) does not stand in for `item`.
			for (const [key, value] of source) {
				if (isPluralKey(key)) continue;
				const qualified = `${nsName}:${key}`;
				if (!target.has(key)) issues.missing.push(qualified);
				else compare(qualified, value, target.get(key));
			}
			// Plural groups are checked against the forms the target language needs. The catalogs use the
			// plain key as the singular (`x` + `x_other`), which i18next falls back to, so it covers `one`.
			// A form English lacks (ru _few) compares with English _other; a shared form with its English twin.
			for (const base of sourcePluralBases) {
				for (const category of categories) {
					const key = `${base}_${category}`;
					if (!target.has(key)) {
						if (!(category === "one" && target.has(base))) issues.missing.push(`${nsName}:${key}`);
						continue;
					}
					const reference = source.has(key) ? source.get(key) : (source.get(`${base}_other`) ?? source.get(base));
					if (reference !== undefined) compare(`${nsName}:${key}`, reference, target.get(key));
				}
			}
			for (const key of target.keys()) {
				if (source.has(key)) continue;
				if (isPluralKey(key) && sourcePluralBases.has(baseKey(key)) && categories.includes(suffixOf(key))) continue;
				issues.extra.push(`${nsName}:${key}`);
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

const literals = runLiterals ? checkLiterals() : {};
const locales = runLocales ? checkLocales() : {};

if (flag("json")) {
	process.stdout.write(`${JSON.stringify({ literals, locales }, null, 2)}\n`);
} else {
	const summaryOnly = flag("summary");
	if (runLiterals) {
		for (const tier of ["untranslated", "review"]) {
			const entries = Object.entries(literals)
				.map(([file, findings]) => [file, findings.filter((f) => f.tier === tier)])
				.filter(([, findings]) => findings.length > 0)
				.sort(([, a], [, b]) => b.length - a.length);
			const total = entries.reduce((n, [, f]) => n + f.length, 0);
			const heading =
				tier === "untranslated"
					? "Hardcoded user-facing strings"
					: "Review: literal label/title/description fields (rendered raw, or dead English beside a t() lookup?)";
			console.log(`\n${heading}: ${total} in ${entries.length} files`);
			for (const [file, findings] of entries) {
				if (summaryOnly) {
					console.log(`  ${String(findings.length).padStart(4)}  ${file}`);
					continue;
				}
				console.log(`\n  ${file}`);
				for (const f of findings) console.log(`    ${file}:${f.line}:${f.column}  [${f.category}]  ${JSON.stringify(f.text)}`);
			}
		}
	}
	if (runLocales) {
		console.log(`\nLocale catalogs vs ${SOURCE_LANG}:`);
		for (const [lang, issues] of Object.entries(locales)) {
			const counts = Object.entries(issues)
				.filter(([, list]) => list.length > 0)
				.map(([kind, list]) => `${kind} ${list.length}`);
			console.log(`  ${lang.padEnd(6)} ${counts.length ? counts.join(", ") : "ok"}`);
			if (summaryOnly) continue;
			for (const [kind, list] of Object.entries(issues)) for (const item of list) console.log(`         ${kind}: ${item}`);
		}
	}
	console.log("");
}

const literalCount = Object.values(literals).reduce(
	(n, findings) => n + findings.filter((f) => flag("strict") || f.tier === "untranslated").length,
	0,
);
const localeCount = Object.values(locales).reduce((n, issues) => n + Object.values(issues).reduce((m, list) => m + list.length, 0), 0);
process.exitCode = literalCount + localeCount > 0 ? 1 : 0;