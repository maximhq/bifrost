#!/usr/bin/env node
// i18n coverage check for the UI.
//
//   npm run i18n:check                 # both checks, exits 1 on any finding
//   npm run i18n:check -- --literals   # only hardcoded user-facing strings
//   npm run i18n:check -- --locales    # only locale catalogs vs English
//   npm run i18n:check -- --strict     # also fail on "review" findings (object fields)
//   npm run i18n:check -- --untranslated   # also flag multi-word values identical to English
//   npm run i18n:check -- --unused     # also list English keys no source file references
//   npm run i18n:check -- --summary    # counts per file / locale, no line listing
//   npm run i18n:check -- --json       # machine-readable output
//   npm run i18n:check -- app/workspace/logs   # limit the literal scan to paths
//
// Literal findings come in two tiers:
//   untranslated  JSX text, {"..."} children, text props, toast messages, zod validation messages.
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

// I18N_CHECK_ROOT points the scan at a fixture tree; scripts/i18nCheck.test.ts uses it.
const UI_ROOT = process.env.I18N_CHECK_ROOT ?? path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
// Enterprise catalogs arrive through the app/enterprise symlink and are checked on their own.
const LOCALE_ROOTS = [path.join(UI_ROOT, "locales"), path.join(UI_ROOT, "app", "enterprise", "locales")].filter((dir) =>
	fs.existsSync(dir),
);
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
// Props whose value is usually an example input or a brand name, so a single lowercase word is not prose.
const EXAMPLE_PROPS = new Set(["placeholder", "alt"]);
// Zod checks whose string argument becomes the message a form shows.
const VALIDATION_METHODS = new Set([
	"min",
	"max",
	"length",
	"regex",
	"email",
	"url",
	"refine",
	"superRefine",
	"nonempty",
	"gt",
	"gte",
	"lt",
	"lte",
	"int",
	"positive",
	"nonnegative",
	"negative",
	"nonpositive",
	"multipleOf",
]);
// Units and literal keywords rendered next to values ("12 ms", "true").
const NOT_PROSE = /^(ms|s|px|kb|mb|gb|true|false|null|undefined)$/i;
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
	if (!/\p{L}/u.test(text) || NOT_PROSE.test(text)) return false;
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

// A property name without quotes: `message` and `"message"` are the same property.
function propertyName(name) {
	return ts.isIdentifier(name) || ts.isStringLiteral(name) ? name.text : name.getText();
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

function isValidationCall(node) {
	return ts.isCallExpression(node) && ts.isPropertyAccessExpression(node.expression) && VALIDATION_METHODS.has(node.expression.name.text);
}

// An object literal passed to addIssue or to a zod check carries the issue message.
function isValidationOptions(object) {
	const call = object?.parent;
	if (!ts.isObjectLiteralExpression(object) || !call || !ts.isCallExpression(call)) return false;
	return isValidationCall(call) || (ts.isPropertyAccessExpression(call.expression) && call.expression.name.text === "addIssue");
}

function scanFile(filePath) {
	const text = fs.readFileSync(filePath, "utf8");
	const kind = filePath.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS;
	const sourceFile = ts.createSourceFile(filePath, text, ts.ScriptTarget.Latest, true, kind);
	const findings = [];

	// `node` is a string leaf (see stringLeaves) or JSX text.
	const report = (node, category, maybeIdentifier = category === "object-field") => {
		const value = ts.isJsxText(node) ? node.text : literalText(node);
		if (!isProse(value, maybeIdentifier) || hasIgnoreComment(sourceFile, node)) return;
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
			const isExample = EXAMPLE_PROPS.has(node.name.getText());
			for (const leaf of stringLeaves(node.initializer)) report(leaf, "jsx-prop", isExample);
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
		// toast("Saved")
		else if (ts.isCallExpression(node) && ts.isIdentifier(node.expression) && node.expression.text === "toast") {
			for (const leaf of stringLeaves(node.arguments[0])) report(leaf, "toast");
		}
		// z.string().min(1, "Name is required"), .refine(fn, "...")
		else if (isValidationCall(node)) {
			for (const arg of node.arguments) for (const leaf of stringLeaves(arg)) report(leaf, "validation");
		}
		// ctx.addIssue({ message: "..." }), .refine(fn, { message: "..." })
		else if (ts.isPropertyAssignment(node) && ["message", "error"].includes(propertyName(node.name)) && isValidationOptions(node.parent)) {
			for (const leaf of stringLeaves(node.initializer)) report(leaf, "validation");
		}
		// { label: "Network" } in option, tab, and column definitions
		else if (ts.isPropertyAssignment(node) && TEXT_FIELDS.has(node.name.getText().replace(/["']/g, ""))) {
			if (!insideTranslationOrType(node) && !hasKeySibling(node))
				for (const leaf of stringLeaves(node.initializer)) report(leaf, "object-field");
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
	const roots = pathFilters.length > 0 ? pathFilters : SCAN_DIRS.map((dir) => path.join(UI_ROOT, dir)).filter((dir) => fs.existsSync(dir));
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
// Indexed (<0>, <b1>) or plain HTML tags. Named tags (<name>, <medium>) count only for keys a <Trans components={{...}}>
// renders, so literal text such as <new-skill> in other keys is left alone.
const TRANS_TAG = /^(\d+|[a-z]+\d+|b|i|em|strong|code|br|span|a|kbd|p|u|s|ul|ol|li)$/;

// Component names each <Trans i18nKey="..." components={{ name: ... }}> passes, by key.
function collectTransComponents() {
	const byKey = new Map();
	const attribute = (opening, name) =>
		opening.attributes.properties.find((prop) => ts.isJsxAttribute(prop) && prop.name.getText() === name);
	for (const file of SCAN_DIRS.flatMap((dir) => walk(path.join(UI_ROOT, dir)))) {
		const text = fs.readFileSync(file, "utf8");
		if (!text.includes("components=")) continue;
		const sourceFile = ts.createSourceFile(file, text, ts.ScriptTarget.Latest, true);
		const visit = (node) => {
			if ((ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) && node.tagName.getText() === "Trans") {
				const keys = stringLeaves(attribute(node, "i18nKey")?.initializer);
				const components = unwrap(attribute(node, "components")?.initializer);
				if (keys.length > 0 && components && ts.isObjectLiteralExpression(components)) {
					const names = components.properties.filter((prop) => prop.name).map((prop) => propertyName(prop.name));
					for (const key of keys) {
						const known = byKey.get(key.text) ?? new Set();
						for (const name of names) known.add(name);
						byKey.set(key.text, known);
					}
				}
			}
			ts.forEachChild(node, visit);
		};
		visit(sourceFile);
	}
	return byKey;
}

// <Trans> component tags in a catalog value (<0>, </strong>, <br/>), compared like placeholders.
function tags(value, named = new Set()) {
	if (typeof value === "string")
		return [...value.matchAll(/<\/?([\w-]+)\s*\/?>/g)]
			.filter((m) => TRANS_TAG.test(m[1]) || named.has(m[1]))
			.map((m) => m[0].replace(/\s+/g, ""))
			.sort()
			.join(",");
	if (Array.isArray(value)) return value.map((item) => tags(item, named)).join("|");
	if (value && typeof value === "object")
		return Object.keys(value)
			.sort()
			.map((key) => `${key}:${tags(value[key], named)}`)
			.join("|");
	return "";
}

// True when a translated string is blank where English has text; arrays compare item by item.
function hasBlank(value, translated) {
	if (typeof value === "string") return value.trim() !== "" && typeof translated === "string" && translated.trim() === "";
	if (Array.isArray(value) && Array.isArray(translated)) return value.some((item, index) => hasBlank(item, translated[index]));
	return false;
}

// Dotted paths of keys that appear twice in one JSON object. JSON.parse keeps the last silently.
function duplicateKeys(text) {
	const duplicates = [];
	const stack = [];
	let pendingKey;
	for (let i = 0; i < text.length; i++) {
		const ch = text[i];
		if (ch === '"') {
			let end = i + 1;
			while (text[end] !== '"') end += text[end] === "\\" ? 2 : 1;
			const str = JSON.parse(text.slice(i, end + 1));
			let next = end + 1;
			while (/\s/.test(text[next])) next++;
			const frame = stack[stack.length - 1];
			if (text[next] === ":" && frame?.keys) {
				if (frame.keys.has(str)) duplicates.push([...stack.slice(1).map((f) => f.name), str].join("."));
				frame.keys.add(str);
				pendingKey = str;
			}
			i = end;
		} else if (ch === "{") {
			stack.push({ keys: new Set(), name: pendingKey });
			pendingKey = undefined;
		} else if (ch === "[") {
			stack.push({ name: pendingKey });
			pendingKey = undefined;
		} else if (ch === "}" || ch === "]") stack.pop();
	}
	return duplicates;
}

let transComponents = new Map();
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
	transComponents = collectTransComponents();
	for (const root of LOCALE_ROOTS) checkLocaleRoot(root, report);
	for (const root of LOCALE_ROOTS)
		for (const lang of [SOURCE_LANG, ...SUPPORTED_LANGS]) {
			const dir = path.join(root, lang);
			if (!fs.existsSync(dir)) continue;
			for (const ns of fs.readdirSync(dir).filter((name) => name.endsWith(".json"))) {
				const found = duplicateKeys(fs.readFileSync(path.join(dir, ns), "utf8"));
				if (found.length === 0) continue;
				const issues = (report[lang] ??= {});
				(issues.duplicates ??= []).push(...found.map((key) => `${ns.replace(/\.json$/, "")}:${key}`));
			}
		}
	return report;
}

function checkLocaleRoot(localesDir, report) {
	const namespaces = fs.readdirSync(path.join(localesDir, SOURCE_LANG)).filter((name) => name.endsWith(".json"));
	for (const lang of SUPPORTED_LANGS) {
		const issues = (report[lang] ??= { missing: [], extra: [], shape: [], placeholders: [], tags: [], empty: [], untranslated: [] });
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
			// A singular form may spell the number out ("every day"), so it can drop {{count}}.
			const compare = (qualified, value, translated, singular = false) => {
				const named = transComponents.get(baseKey(qualified.slice(qualified.indexOf(":") + 1))) ?? new Set();
				const expected =
					singular && !/\{\{\s*count\b/.test(String(translated))
						? placeholders(value)
								.replace(/(^|,)count(?=,|$)/, "")
								.replace(/^,/, "")
						: placeholders(value);
				if (Array.isArray(value) !== Array.isArray(translated) || typeof value !== typeof translated) issues.shape.push(qualified);
				else if (Array.isArray(value) && value.length !== translated.length)
					issues.shape.push(`${qualified} (en: ${value.length} items vs ${lang}: ${translated.length})`);
				else if (hasBlank(value, translated)) issues.empty.push(qualified);
				else if (expected !== placeholders(translated))
					issues.placeholders.push(`${qualified} (en: {${placeholders(value)}} vs ${lang}: {${placeholders(translated)}})`);
				else if (tags(value, named) !== tags(translated, named))
					issues.tags.push(`${qualified} (en: ${tags(value, named) || "none"} vs ${lang}: ${tags(translated, named) || "none"})`);
				// Single words that match English are usually loanwords or product names (Relay, Status, SSE).
				else if (flag("untranslated") && typeof value === "string" && value === translated && /\s/.test(value.trim()) && isProse(value))
					issues.untranslated.push(qualified);
			};
			// Ordinary keys must exist exactly; a plural sibling (item_one) does not stand in for `item`.
			for (const [key, value] of source) {
				if (isPluralKey(key)) continue;
				const qualified = `${nsName}:${key}`;
				if (!target.has(key)) issues.missing.push(qualified);
				else compare(qualified, value, target.get(key), sourcePluralBases.has(key));
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
					if (reference !== undefined) compare(`${nsName}:${key}`, reference, target.get(key), category === "one");
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
// Unused keys
// ---------------------------------------------------------------------------

// A key counts as used when a source string equals it, names a parent read with returnObjects,
// or is the static head of a template key such as `providers.keys.vpc.${name}.label`.
function checkUnused() {
	const literals = new Set();
	const prefixes = new Set();
	for (const file of SCAN_DIRS.flatMap((dir) => walk(path.join(UI_ROOT, dir)))) {
		const sourceFile = ts.createSourceFile(file, fs.readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true);
		const visit = (node) => {
			if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) {
				literals.add(node.text);
				if (node.text.endsWith(".")) prefixes.add(node.text); // "providers." + name
			} else if (ts.isTemplateExpression(node) && node.head.text) prefixes.add(node.head.text);
			ts.forEachChild(node, visit);
		};
		visit(sourceFile);
	}
	const isUsed = (key) => {
		if (literals.has(key)) return true;
		for (let i = key.lastIndexOf("."); i > 0; i = key.lastIndexOf(".", i - 1)) if (literals.has(key.slice(0, i))) return true;
		for (const prefix of prefixes) if (key.startsWith(prefix)) return true;
		return false;
	};
	const unused = {};
	for (const root of LOCALE_ROOTS) {
		const dir = path.join(root, SOURCE_LANG);
		for (const ns of fs.readdirSync(dir).filter((name) => name.endsWith(".json"))) {
			const keys = [...new Set([...flatten(JSON.parse(fs.readFileSync(path.join(dir, ns), "utf8"))).keys()].map(baseKey))];
			const found = keys.filter((key) => !isUsed(key));
			if (found.length > 0) unused[ns.replace(/\.json$/, "")] = found;
		}
	}
	return unused;
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

const literals = runLiterals ? checkLiterals() : {};
const locales = runLocales ? checkLocales() : {};
const unused = flag("unused") ? checkUnused() : {};

if (flag("json")) {
	process.stdout.write(`${JSON.stringify({ literals, locales, unused }, null, 2)}\n`);
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
	if (flag("unused")) {
		const total = Object.values(unused).reduce((n, keys) => n + keys.length, 0);
		console.log(`\nUnused ${SOURCE_LANG} keys (no source string, parent key or template prefix matches): ${total}`);
		for (const [ns, keys] of Object.entries(unused)) {
			console.log(`  ${String(keys.length).padStart(4)}  ${ns}`);
			if (!summaryOnly) for (const key of keys) console.log(`         ${ns}:${key}`);
		}
	}
	console.log("");
}

const literalCount = Object.values(literals).reduce(
	(n, findings) => n + findings.filter((f) => flag("strict") || f.tier === "untranslated").length,
	0,
);
const localeCount = Object.values(locales).reduce((n, issues) => n + Object.values(issues).reduce((m, list) => m + list.length, 0), 0);
const unusedCount = Object.values(unused).reduce((n, keys) => n + keys.length, 0);
process.exitCode = literalCount + localeCount + unusedCount > 0 ? 1 : 0;