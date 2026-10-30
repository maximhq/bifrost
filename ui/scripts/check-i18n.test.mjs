import { afterEach, describe, expect, it } from "vitest";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import ts from "typescript";
import {
	LOCALES,
	NAMESPACES,
	analyzeTranslationCalls,
	auditRemovedCalls,
	diffCallFindings,
	loadResources,
	runCli,
	validateCall,
	validateDynamicFamilies,
	validateLocales,
} from "./check-i18n.mjs";

const temporaryDirectories = [];

afterEach(() => {
	for (const directory of temporaryDirectories.splice(0)) {
		rmSync(directory, { recursive: true, force: true });
	}
});

function makeTemporaryDirectory() {
	const directory = mkdtempSync(path.join(tmpdir(), "check-i18n-"));
	temporaryDirectories.push(directory);
	return directory;
}

function writeMinimalResources(repoRoot) {
	for (const locale of LOCALES) {
		for (const namespace of NAMESPACES) {
			const directory = path.join(repoRoot, "ui", "locales", locale);
			mkdirSync(directory, { recursive: true });
			writeFileSync(path.join(directory, `${namespace}.json`), JSON.stringify({ greeting: "Hello" }));
		}
	}
}

describe("validateLocales", () => {
	it("requires nested English leaf keys in every locale", () => {
		const current = {
			en: { common: { nested: { title: "Title" } } },
			de: { common: { nested: {} } },
		};

		expect(validateLocales(current, current)).toEqual([
			expect.objectContaining({ type: "missing-key", locale: "de", namespace: "common", key: "nested.title" }),
		]);
	});

	it("accepts Russian plural categories and the optional zero override", () => {
		const current = {
			en: { common: { items_one: "{{count}} item", items_other: "{{count}} items" } },
			ru: {
				common: {
					items_one: "{{count}} элемент",
					items_few: "{{count}} элемента",
					items_many: "{{count}} элементов",
					items_other: "{{count}} элемента",
					items_zero: "Нет элементов",
				},
			},
		};

		expect(validateLocales(current, current)).toEqual([]);
	});

	it("allows zh-CN to omit locale extras but not English plural keys", () => {
		const english = { common: { items_one: "{{count}} item", items_other: "{{count}} items" } };
		const current = {
			en: english,
			"zh-CN": { common: { items_one: "{{count}} 项", items_other: "{{count}} 项" } },
		};

		expect(validateLocales(current, current)).toEqual([]);
		delete current["zh-CN"].common.items_one;
		expect(validateLocales(current, current)).toEqual([
			expect.objectContaining({ type: "missing-key", locale: "zh-CN", namespace: "common", key: "items_one" }),
		]);
	});

	it("reports removal of a valid PR-head plural variant", () => {
		const baseline = {
			en: { common: { items_one: "{{count}} item", items_other: "{{count}} items" } },
			ru: {
				common: {
					items_one: "{{count}} элемент",
					items_few: "{{count}} элемента",
					items_many: "{{count}} элементов",
					items_other: "{{count}} элемента",
				},
			},
		};
		const current = structuredClone(baseline);
		delete current.ru.common.items_few;

		expect(validateLocales(current, baseline)).toEqual([
			expect.objectContaining({
				type: "removed-plural-key",
				reason: "baseline-retention",
				locale: "ru",
				namespace: "common",
				key: "items_few",
			}),
		]);
	});

	it("reports invalid JSON loaded from a resource file", () => {
		const repoRoot = makeTemporaryDirectory();
		writeMinimalResources(repoRoot);
		const baseline = loadResources(repoRoot);
		writeFileSync(path.join(repoRoot, "ui", "locales", "ru", "common.json"), "{not-json");
		const current = loadResources(repoRoot);

		expect(validateLocales(current, baseline)).toEqual([
			expect.objectContaining({ type: "invalid-json", locale: "ru", namespace: "common" }),
		]);
	});

	it("reports an arbitrary non-plural extra key", () => {
		const current = {
			en: { common: { greeting: "Hello" } },
			de: { common: { greeting: "Hallo", untracked: "Extra" } },
		};

		expect(validateLocales(current, current)).toEqual([
			expect.objectContaining({ type: "unexpected-key", locale: "de", namespace: "common", key: "untracked" }),
		]);
	});
});

function makeSourceFile(text, fileName = "fixture.tsx") {
	return ts.createSourceFile(fileName, text, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
}

function makeCall(overrides = {}) {
	return {
		file: "fixture.tsx",
		line: 1,
		column: 1,
		namespace: "common",
		key: "greeting",
		kind: "t",
		dynamic: false,
		prefix: null,
		options: { count: false, returnObjects: false, defaultValue: false },
		...overrides,
		options: { count: false, returnObjects: false, defaultValue: false, ...overrides.options },
	};
}

function makeFinding(overrides = {}) {
	return {
		type: "missing-key",
		file: "ui/example.tsx",
		line: 1,
		column: 1,
		namespace: "common",
		key: "greeting",
		...overrides,
	};
}

function git(repoRoot, ...args) {
	return execFileSync("git", args, { cwd: repoRoot, encoding: "utf8" });
}

function commitAll(repoRoot, message) {
	git(repoRoot, "add", "-A");
	git(repoRoot, "commit", "-m", message);
	return git(repoRoot, "rev-parse", "HEAD").trim();
}

function makeGitRepo() {
	const repoRoot = makeTemporaryDirectory();
	git(repoRoot, "init", "-q");
	git(repoRoot, "config", "user.email", "checker@example.com");
	git(repoRoot, "config", "user.name", "Checker");
	return repoRoot;
}

function writeRepoFile(repoRoot, file, text) {
	const absolutePath = path.join(repoRoot, file);
	mkdirSync(path.dirname(absolutePath), { recursive: true });
	writeFileSync(absolutePath, text);
}

function inventoryAtRef(repoRoot, ref, file) {
	const text = ref ? git(repoRoot, "show", `${ref}:${file}`) : readFileSync(path.join(repoRoot, file), "utf8");
	return analyzeTranslationCalls(makeSourceFile(text, file));
}

function sourceIdentity(entry) {
	return {
		file: entry.file,
		line: entry.line,
		column: entry.column,
		namespace: entry.namespace,
		key: entry.key,
	};
}

function firstDiffHeader(diff) {
	return diff.split("\n").find((line) => line.startsWith("@@"));
}

describe("analyzeTranslationCalls", () => {
	it("resolves namespace scopes, aliases, TFunction parameters, and Trans without suffix matching", () => {
		const entries = analyzeTranslationCalls(
			makeSourceFile(`
				const { t } = useTranslation("observability");
				t("models.foo");
				const { t: translate } = useTranslation("models");
				translate("title");
				i18n.t("governance.title", { ns: "governance" });
				function render(t: TFunction<"mcp">) { return t("servers"); }
				const explicit = <Trans i18nKey="footer" ns="common" />;
				function Panel() {
					const { t: tx } = useTranslation("observability");
					return <Trans i18nKey="title" />;
				}
				copyEndpoint("models.notATranslation");
			`),
		);

		expect(entries.map(({ namespace, key, kind }) => ({ namespace, key, kind }))).toEqual([
			{ namespace: "observability", key: "models.foo", kind: "t" },
			{ namespace: "models", key: "title", kind: "t" },
			{ namespace: "governance", key: "governance.title", kind: "t" },
			{ namespace: "mcp", key: "servers", kind: "t" },
			{ namespace: "common", key: "footer", kind: "trans" },
			{ namespace: "observability", key: "title", kind: "trans" },
		]);
		expect(validateCall(entries[0], { models: { foo: "Foo" }, observability: {} })).toEqual([
			expect.objectContaining({ type: "missing-key", namespace: "observability", key: "models.foo" }),
		]);
	});

	it("keeps namespace-like nested keys in the hook namespace", () => {
		const [entry] = analyzeTranslationCalls(makeSourceFile(`const { t } = useTranslation("mcp"); t("common.configCopied");`));
		expect(validateCall(entry, { mcp: { common: { configCopied: "Configuration copied" } }, common: {} })).toEqual([]);
	});

	it("applies useTranslation keyPrefix to static, plural, and Trans keys", () => {
		const entries = analyzeTranslationCalls(
			makeSourceFile(`
				const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" });
				tcpx("phraseDefaultsCallout");
				tcpx("phrases", { count: 2 });
				const element = <Trans t={tcpx} i18nKey="nested" />;
			`),
		);
		const resources = {
			models: {
				routing: {
					complexityUi: {
						phraseDefaultsCallout: "Defaults",
						phrases_one: "{{count}} phrase",
						phrases_other: "{{count}} phrases",
						nested: "Nested",
					},
				},
			},
		};
		expect(entries.map((entry) => entry.key)).toEqual([
			"routing.complexityUi.phraseDefaultsCallout",
			"routing.complexityUi.phrases",
			"routing.complexityUi.nested",
		]);
		for (const entry of entries) {
			expect(validateCall(entry, resources)).toEqual([]);
		}
	});

	it("applies useTranslation keyPrefix to dynamic families", () => {
		const entries = analyzeTranslationCalls(
			makeSourceFile(
				'const { t: tcpx } = useTranslation("models", { keyPrefix: "routing.complexityUi" }); tcpx(`status.${value}`);',
				"ui/models.tsx",
			),
		);
		expect(entries[0]).toEqual(expect.objectContaining({ dynamic: true, prefix: "routing.complexityUi.status." }));
		const review = {
			prefix: "routing.complexityUi.status.",
			producers: entries.map((entry) => `${entry.file}:${entry.line}:${entry.column}`),
			candidates: ["routing.complexityUi.status.ready"],
			englishResults: { "routing.complexityUi.status.ready": "resolved" },
		};
		const resources = { models: { routing: { complexityUi: { status: { ready: "Ready" } } } } };
		expect(validateDynamicFamilies(entries, resources, [review])).toEqual([]);
	});

	it("resolves namespace-like key subtrees against an explicit ns option", () => {
		const [entry] = analyzeTranslationCalls(
			makeSourceFile(`const { t } = useTranslation("mcp"); t("common.ofEntries", { ns: "mcp", from: 1, to: 1, total: 1 });`),
		);
		expect(validateCall(entry, { mcp: { common: { ofEntries: "{{from}}-{{to}} of {{total}} entries" } } })).toEqual([]);
	});

	it("resolves a Trans t prop through its own hook alias", () => {
		const [entry] = analyzeTranslationCalls(
			makeSourceFile(`
				const { t } = useTranslation("models");
				const { t: tc } = useTranslation("common");
				const element = <Trans t={t} i18nKey="title" />;
			`),
		);
		expect(entry).toEqual(expect.objectContaining({ namespace: "models", key: "title" }));
		expect(validateCall(entry, { models: { title: "Title" }, common: {} })).toEqual([]);
	});

	it("expands statically enumerable conditional keys", () => {
		const entries = analyzeTranslationCalls(makeSourceFile(`const { t } = useTranslation("common"); t(enabled ? "enabled" : "disabled");`));
		expect(entries.map((entry) => entry.key)).toEqual(["enabled", "disabled"]);
	});

	it("reports an unresolved TFunction namespace instead of assuming common", () => {
		const entries = analyzeTranslationCalls(makeSourceFile(`function render(t: TFunction<Namespace>) { return t("title"); }`));
		expect(entries).toHaveLength(1);
		expect(validateCall(entries[0], { common: { title: "Title" } })).toEqual([
			expect.objectContaining({ type: "unresolved-namespace", namespace: null, key: "title" }),
		]);
	});

	it("reports unresolved hook and option namespaces", () => {
		const entries = analyzeTranslationCalls(
			makeSourceFile(`
				const { t } = useTranslation(Namespace);
				t("hook");
				i18n.t("option", { ns: Namespace });
				const element = <Trans i18nKey="trans" ns={Namespace} />;
			`),
		);
		expect(entries.map((entry) => entry.key)).toEqual(["hook", "option", "trans"]);
		for (const entry of entries) {
			expect(validateCall(entry, { common: { hook: "Hook", option: "Option", trans: "Trans" } })).toEqual([
				expect.objectContaining({ type: "unresolved-namespace", namespace: null }),
			]);
		}
	});

	it("tracks count on Trans elements", () => {
		const [entry] = analyzeTranslationCalls(makeSourceFile(`const element = <Trans i18nKey="items" count={2} />;`));
		expect(entry.options).toEqual(expect.objectContaining({ count: true }));
	});
});

describe("validateCall", () => {
	const resources = {
		common: {
			items_one: "{{count}} item",
			items_other: "{{count}} items",
			items_zero: "No items",
			chars: "{{count}} chars",
			tree: { child: "Child" },
			empty: {},
			greeting: "Hello",
		},
	};

	it("accepts plural families with optional zero and bare count interpolation", () => {
		expect(validateCall(makeCall({ key: "items", options: { count: true } }), resources)).toEqual([]);
		expect(validateCall(makeCall({ key: "chars", options: { count: true } }), resources)).toEqual([]);
	});

	it("requires a non-empty returnObjects subtree", () => {
		expect(validateCall(makeCall({ key: "tree", options: { returnObjects: true } }), resources)).toEqual([]);
		expect(validateCall(makeCall({ key: "empty", options: { returnObjects: true } }), resources)).toEqual([
			expect.objectContaining({ type: "empty-return-object", key: "empty" }),
		]);
	});

	it("never lets defaultValue hide a missing key and reports fallback separately", () => {
		expect(validateCall(makeCall({ key: "missing", options: { defaultValue: true } }), resources).map((finding) => finding.type)).toEqual([
			"missing-key",
			"default-value",
		]);
		expect(validateCall(makeCall({ options: { defaultValue: true } }), resources)).toEqual([
			expect.objectContaining({ type: "default-value", key: "greeting" }),
		]);
	});
});

describe("dynamic family review", () => {
	const entries = analyzeTranslationCalls(makeSourceFile('const { t } = useTranslation("models"); t(`status.${value}`);', "ui/models.tsx"));
	const resources = { models: { status: { ready: "Ready", failed: "Failed" } } };

	it("requires complete producers, candidates, and English results", () => {
		expect(entries).toHaveLength(1);
		expect(entries[0]).toEqual(expect.objectContaining({ dynamic: true, prefix: "status." }));
		expect(validateDynamicFamilies(entries, resources, []).map((finding) => finding.type)).toEqual(["unresolved-dynamic"]);
		expect(
			validateDynamicFamilies(entries, resources, [
				{ prefix: "status.", producers: [], candidates: ["status.ready"], englishResults: { "status.ready": "resolved" } },
			]).map((finding) => finding.type),
		).toEqual(["incomplete-dynamic-review"]);
		expect(
			validateDynamicFamilies(entries, resources, [
				{
					prefix: "status.",
					producers: [`ui/models.tsx:${entries[0].line}:${entries[0].column}`],
					candidates: ["status.ready", "status.failed"],
					englishResults: { "status.ready": "resolved", "status.failed": "resolved" },
				},
			]),
		).toEqual([]);
	});
});

describe("dynamic producer-specific validation", () => {
	it("validates candidates in every producer namespace", () => {
		const crossNamespaceEntries = analyzeTranslationCalls(
			makeSourceFile(`
				const { t: modelT } = useTranslation("models");
				modelT(\`status.\${value}\`);
				const { t: observabilityT } = useTranslation("observability");
				observabilityT(\`status.\${value}\`);
			`),
		);
		const review = {
			prefix: "status.",
			producers: crossNamespaceEntries.map((entry) => `${entry.file}:${entry.line}:${entry.column}`),
			candidates: ["status.ready"],
			englishResults: { "status.ready": "resolved" },
		};
		const findings = validateDynamicFamilies(crossNamespaceEntries, { models: { status: { ready: "Ready" } } }, [review]);
		expect(findings).toEqual([expect.objectContaining({ type: "dynamic-candidate-unresolved", namespace: "observability" })]);
	});

	it("preserves count options when validating dynamic candidates", () => {
		const countEntries = analyzeTranslationCalls(makeSourceFile('const { t } = useTranslation("common"); t(`items.${value}`, { count });'));
		const review = {
			prefix: "items.",
			producers: countEntries.map((entry) => `${entry.file}:${entry.line}:${entry.column}`),
			candidates: ["items.ready"],
			englishResults: { "items.ready": "resolved" },
		};
		expect(validateDynamicFamilies(countEntries, { common: { items: { ready: "Ready" } } }, [review])).toEqual([
			expect.objectContaining({ type: "dynamic-candidate-unresolved", namespace: "common" }),
		]);
	});

	it("preserves returnObjects options when validating dynamic candidates", () => {
		const objectEntries = analyzeTranslationCalls(
			makeSourceFile('const { t } = useTranslation("common"); t(`items.${value}`, { returnObjects: true });'),
		);
		const review = {
			prefix: "items.",
			producers: objectEntries.map((entry) => `${entry.file}:${entry.line}:${entry.column}`),
			candidates: ["items.ready"],
			englishResults: { "items.ready": "resolved" },
		};
		expect(validateDynamicFamilies(objectEntries, { common: { items: { ready: "Ready" } } }, [review])).toEqual([
			expect.objectContaining({ type: "dynamic-candidate-unresolved", namespace: "common" }),
		]);
	});
});

describe("dynamic candidate producer mappings", () => {
	const mixedEntries = analyzeTranslationCalls(
		makeSourceFile(`
			const { t } = useTranslation("common");
			t(\`warp.errors.\${summary}\`);
			t(\`warp.errors.\${cause}\`);
			t(\`warp.errors.\${suggestions}\`, { returnObjects: true });
		`),
	);
	const candidates = ["warp.errors.summary", "warp.errors.cause", "warp.errors.suggestions"];
	const resources = {
		common: {
			warp: {
				errors: {
					summary: "Summary",
					cause: "Cause",
					suggestions: [{ text: "Try again" }],
				},
			},
		},
	};

	function mixedReview(candidateProducers) {
		return {
			prefix: "warp.errors.",
			producers: mixedEntries.map((entry) => `${entry.file}:${entry.line}:${entry.column}`),
			candidates,
			englishResults: Object.fromEntries(candidates.map((candidate) => [candidate, "resolved"])),
			...(candidateProducers ? { candidateProducers } : {}),
		};
	}

	it("validates mixed scalar and object candidates only against their mapped producers", () => {
		const candidateProducers = Object.fromEntries(
			candidates.map((candidate, index) => [
				candidate,
				[`${mixedEntries[index].file}:${mixedEntries[index].line}:${mixedEntries[index].column}`],
			]),
		);
		expect(validateDynamicFamilies(mixedEntries, resources, [mixedReview(candidateProducers)])).toEqual([]);
	});

	it("requires producer mappings for heterogeneous families", () => {
		expect(validateDynamicFamilies(mixedEntries, resources, [mixedReview()])).toEqual([
			expect.objectContaining({ type: "incomplete-dynamic-review", namespace: "common" }),
		]);
	});

	it("requires candidate mappings to cover every producer", () => {
		const candidateProducers = Object.fromEntries(
			candidates.map((candidate, index) => [
				candidate,
				[`${mixedEntries[Math.min(index, 1)].file}:${mixedEntries[Math.min(index, 1)].line}:${mixedEntries[Math.min(index, 1)].column}`],
			]),
		);
		expect(validateDynamicFamilies(mixedEntries, resources, [mixedReview(candidateProducers)])).toEqual([
			expect.objectContaining({ type: "incomplete-dynamic-review", namespace: "common" }),
		]);
	});

	it.each([
		["empty", []],
		["malformed", "not-an-array"],
		["unknown", ["ui/unknown.tsx:1:1"]],
	])("rejects %s candidate-producer mappings", (_name, mapping) => {
		const candidateProducers = Object.fromEntries(
			candidates.map((candidate, index) => [
				candidate,
				index === 0 ? mapping : [`${mixedEntries[index].file}:${mixedEntries[index].line}:${mixedEntries[index].column}`],
			]),
		);
		expect(validateDynamicFamilies(mixedEntries, resources, [mixedReview(candidateProducers)])).toEqual([
			expect.objectContaining({ type: "incomplete-dynamic-review", namespace: "common" }),
		]);
	});
});

describe("baseline call finding diff", () => {
	it("preserves untouched exceptions and gates only new or merge-touched findings", () => {
		const baseline = makeFinding();
		const current = makeFinding({ file: "ui/other.tsx", line: 2 });
		const touched = makeFinding({ file: "ui/conflicted.tsx", type: "default-value" });
		const result = diffCallFindings([baseline, current, touched], [baseline], ["ui/conflicted.tsx"]);
		expect(result.newFindings).toEqual([current]);
		expect(result.mergeTouchedFindings).toEqual([touched]);
		expect(result.baselineExceptions).toEqual([baseline]);
	});

	it("matches shifted baseline findings by multiplicity without hiding duplicates", () => {
		const baseline = makeFinding({ line: 10, column: 2 });
		const shifted = makeFinding({ line: 12, column: 4 });
		const duplicate = makeFinding({ line: 20, column: 6 });
		const result = diffCallFindings([shifted, duplicate], [baseline]);
		expect(result.newFindings).toEqual([duplicate]);
		expect(result.baselineExceptions).toEqual([shifted]);
	});
});

describe("removed call evidence", () => {
	const file = "ui/example.tsx";
	const baselineText = `export const Old = () => <button data-testid="save-button" onClick={save}>{i18n.t("save")}</button>;\nexport const Other = () => <button data-testid="other-button">{i18n.t("other")}</button>;\n`;
	const migratedText = `export const New = () => <button data-testid="save-button" onClick={save}>{i18n.t("saved")}</button>;\nexport const Other = () => <button data-testid="other-button">{i18n.t("other")}</button>;\n`;
	const resources = { common: { save: "Save", saved: "Saved", other: "Other" } };

	function migrationFixture() {
		const repoRoot = makeGitRepo();
		writeRepoFile(repoRoot, file, baselineText);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, migratedText);
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, "--", file));
		return { repoRoot, baseline, baselineEntries, currentEntries, diffHeader };
	}

	function prOnlyRemovalFixture({ mergeBaseText, prText, upstreamText, currentText = upstreamText }) {
		const repoRoot = makeGitRepo();
		writeRepoFile(repoRoot, file, mergeBaseText);
		const mergeBase = commitAll(repoRoot, "merge base");
		writeRepoFile(repoRoot, file, prText);
		const baseline = commitAll(repoRoot, "PR-only translation");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		git(repoRoot, "checkout", "-b", "dev", mergeBase);
		writeRepoFile(repoRoot, file, upstreamText);
		const upstreamSha = commitAll(repoRoot, "upstream behavior change");
		writeRepoFile(repoRoot, file, currentText);
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		return { repoRoot, mergeBase, baseline, baselineEntries, currentEntries, upstreamSha };
	}

	it("ignores location-only shifts without hiding a different behavior removal", () => {
		const baselineEntries = [
			makeCall({ file, line: 10, behaviorAnchors: ["save-button"] }),
			makeCall({ file, line: 20, behaviorAnchors: ["delete-button"] }),
		];
		const currentEntries = [makeCall({ file, line: 30, column: 4, behaviorAnchors: ["save-button"] })];
		const findings = auditRemovedCalls({
			baselineEntries,
			currentEntries,
			currentResources: resources,
			baselineResources: resources,
		});
		expect(findings).toEqual([expect.objectContaining({ type: "removed-call-unexplained", line: 20 })]);
	});

	it("matches location-only shifts by multiplicity for duplicate unanchored calls", () => {
		const baselineEntries = [makeCall({ file, line: 10 }), makeCall({ file, line: 20 })];
		const currentEntries = [makeCall({ file, line: 30 })];
		const findings = auditRemovedCalls({
			baselineEntries,
			currentEntries,
			currentResources: resources,
			baselineResources: resources,
		});
		expect(findings).toEqual([expect.objectContaining({ type: "removed-call-unexplained", line: 20 })]);
	});

	it("accepts a changed same-behavior migration target", () => {
		const fixture = migrationFixture();
		const source = fixture.baselineEntries.find((entry) => entry.key === "save");
		const target = fixture.currentEntries.find((entry) => entry.key === "saved");
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(source),
					reason: "equivalent-migration",
					target: { ...sourceIdentity(target), diffHeader: fixture.diffHeader, behaviorAnchor: "save-button" },
				},
			],
		});
		expect(findings).toEqual([]);
	});

	it("accepts a newly added dynamic migration target using its AST span", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(repoRoot, file, `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, 'export const New = () => <button data-testid="save-button">{i18n.t(`common.${action}`)}</button>;\n');
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		const target = currentEntries[0];
		expect(target).toEqual(expect.objectContaining({ dynamic: true, key: null, prefix: "common." }));
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, "--", file));
		const findings = auditRemovedCalls({
			repoRoot,
			baseline,
			baselineEntries,
			currentEntries,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(baselineEntries[0]),
					reason: "equivalent-migration",
					target: { ...sourceIdentity(target), diffHeader, behaviorAnchor: "save-button" },
				},
			],
		});
		expect(findings).toEqual([]);
	});

	it("rejects a missing migration target", () => {
		const fixture = migrationFixture();
		const source = fixture.baselineEntries.find((entry) => entry.key === "save");
		const target = fixture.currentEntries.find((entry) => entry.key === "saved");
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(source),
					reason: "equivalent-migration",
					target: { ...sourceIdentity(target), line: target.line + 20, diffHeader: fixture.diffHeader, behaviorAnchor: "save-button" },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["migration-target-not-found"]);
	});

	it("rejects an unchanged same-key call borrowed from another UI location", () => {
		const fixture = migrationFixture();
		const source = fixture.baselineEntries.find((entry) => entry.key === "save");
		const target = fixture.currentEntries.find((entry) => entry.key === "other");
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(source),
					reason: "equivalent-migration",
					target: { ...sourceIdentity(target), diffHeader: fixture.diffHeader, behaviorAnchor: "other-button" },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["migration-target-unchanged"]);
	});

	it("rejects an unchanged target line inside a hunk with another added occurrence of its key", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(
			repoRoot,
			file,
			`export const Old = () => (\n\t<div data-testid="save-button">\n\t\t<span>{i18n.t("save")}</span>\n\t\t<span>{i18n.t("saved")}</span>\n\t</div>\n);\n`,
		);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(
			repoRoot,
			file,
			`export const Old = () => (\n\t<div data-testid="save-button">\n\t\t<span>{null}</span>\n\t\t<span>{i18n.t("saved")}</span>\n\t\t<span>{i18n.t("saved")}</span>\n\t</div>\n);\n`,
		);
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		const source = baselineEntries.find((entry) => entry.key === "save");
		const target = currentEntries[0];
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, "--", file));
		const findings = auditRemovedCalls({
			repoRoot,
			baseline,
			baselineEntries,
			currentEntries,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(source),
					reason: "equivalent-migration",
					target: { ...sourceIdentity(target), diffHeader, behaviorAnchor: "save-button" },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["migration-target-unchanged"]);
	});

	it("rejects a behavior anchor attached to an unrelated control", () => {
		const fixture = migrationFixture();
		const source = fixture.baselineEntries.find((entry) => entry.key === "save");
		const target = fixture.currentEntries.find((entry) => entry.key === "saved");
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(source),
					reason: "equivalent-migration",
					target: { ...sourceIdentity(target), diffHeader: fixture.diffHeader, behaviorAnchor: "other-button" },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["migration-behavior-mismatch"]);
	});

	it("requires separate consolidation review for many-to-one migrations", () => {
		const repoRoot = makeGitRepo();
		const twoSourceText = `export const Old = () => <div data-testid="save-button"><span>{i18n.t("save")}</span><span>{i18n.t("saved")}</span></div>;\n`;
		writeRepoFile(repoRoot, file, twoSourceText);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, `export const New = () => <div data-testid="save-button">{i18n.t("save")}</div>;\n`);
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, "--", file));
		const target = currentEntries[0];
		const removedCalls = baselineEntries.map((source) => ({
			source: sourceIdentity(source),
			reason: "equivalent-migration",
			target: { ...sourceIdentity(target), diffHeader, behaviorAnchor: "save-button" },
		}));
		const input = { repoRoot, baseline, baselineEntries, currentEntries, currentResources: resources, baselineResources: resources };
		expect(auditRemovedCalls({ ...input, removedCalls }).map((finding) => finding.type)).toEqual([
			"consolidation-required",
			"consolidation-required",
		]);
		for (const record of removedCalls) {
			record.consolidation = { reason: "Two labels became one control", reviewer: "i18n-reviewer" };
		}
		expect(auditRemovedCalls({ ...input, removedCalls })).toEqual([]);
	});

	it("requires a matching upstream removal hunk", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(repoRoot, file, baselineText);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, "export const Removed = () => null;\n");
		const upstreamSha = commitAll(repoRoot, "remove controls");
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, upstreamSha, "--", file));
		const input = { repoRoot, baseline, baselineEntries, currentEntries, currentResources: resources, baselineResources: resources };
		const removedCalls = baselineEntries.map((source) => ({
			source: sourceIdentity(source),
			reason: "upstream-removed",
			upstream: { sha: upstreamSha, file, diffHeader },
		}));
		expect(auditRemovedCalls({ ...input, removedCalls })).toEqual([]);
		removedCalls[0].upstream.diffHeader = "@@ mismatched @@";
		expect(auditRemovedCalls({ ...input, removedCalls }).map((finding) => finding.type)).toEqual(["upstream-evidence-invalid"]);
	});

	it("rejects an upstream hunk that re-adds the same translation behavior", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(repoRoot, file, `export const Old = () => <button>{i18n.t("save")}</button>;\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, `export const New = () => null;\nexport const Retained = () => <button>{i18n.t("save")}</button>;\n`);
		const upstreamSha = commitAll(repoRoot, "move control");
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, upstreamSha, "--", file));
		const findings = auditRemovedCalls({
			repoRoot,
			baseline,
			baselineEntries,
			currentEntries,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["upstream-evidence-invalid"]);
	});

	it("rejects a current hardcoded English replacement despite valid upstream removal evidence", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(repoRoot, file, `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, "export const Removed = () => null;\n");
		const upstreamSha = commitAll(repoRoot, "remove upstream control");
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, upstreamSha, "--", file));
		writeRepoFile(repoRoot, file, `export const Current = () => <button data-testid="save-button">Save</button>;\n`);
		const findings = auditRemovedCalls({
			repoRoot,
			baseline,
			baselineEntries,
			currentEntries: [],
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["hardcoded-english-reversion"]);
	});

	it("rejects upstream evidence that only removes a matching comment", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(repoRoot, file, `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n// Save\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n`);
		const upstreamSha = commitAll(repoRoot, "remove comment");
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, upstreamSha, "--", file));
		writeRepoFile(repoRoot, file, "export const Current = () => null;\n");
		const findings = auditRemovedCalls({
			repoRoot,
			baseline,
			baselineEntries,
			currentEntries: [],
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["upstream-evidence-invalid"]);
	});

	it("rejects upstream evidence from an unrelated control with the same copy", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(
			repoRoot,
			file,
			`export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\nexport const Other = () => <button data-testid="other-button">Save</button>;\n`,
		);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n`);
		const upstreamSha = commitAll(repoRoot, "remove unrelated control");
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, upstreamSha, "--", file));
		writeRepoFile(repoRoot, file, "export const Current = () => null;\n");
		const findings = auditRemovedCalls({
			repoRoot,
			baseline,
			baselineEntries,
			currentEntries: [],
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["upstream-evidence-invalid"]);
	});

	it("accepts PR-only removal evidence from the PR-head to upstream diff", () => {
		const mergeBaseText = "export const Old = () => null;\n";
		const prText = `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n`;
		const fixture = prOnlyRemovalFixture({ mergeBaseText, prText, upstreamText: "export const Old = () => null;\n// dev\n" });
		expect(fixture.mergeBase).not.toBe(fixture.baseline);
		const diffHeader = firstDiffHeader(git(fixture.repoRoot, "diff", fixture.baseline, fixture.upstreamSha, "--", file));
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(fixture.baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: fixture.upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings).toEqual([]);
	});

	it("rejects PR-only evidence from an unrelated copy-removal hunk", () => {
		const filler = Array.from({ length: 60 }, (_, index) => `// filler ${index}\n`).join("");
		const mergeBaseText = `export const Old = () => null;\n${filler}export const Other = () => <button data-testid="other-button">Save</button>;\n`;
		const prText = `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n${filler}export const Other = () => <button data-testid="other-button">Save</button>;\n`;
		const upstreamText = `export const Old = () => null;\n${filler}`;
		const fixture = prOnlyRemovalFixture({ mergeBaseText, prText, upstreamText });
		const diffHeader = git(fixture.repoRoot, "diff", fixture.baseline, fixture.upstreamSha, "--", file)
			.split("\n")
			.filter((line) => line.startsWith("@@"))
			.at(-1);
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(fixture.baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: fixture.upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["upstream-evidence-invalid"]);
	});

	it("rejects hardcoded English reversion after valid PR-only upstream evidence", () => {
		const mergeBaseText = "export const Old = () => null;\n";
		const prText = `export const Old = () => <button data-testid="save-button">{i18n.t("save")}</button>;\n`;
		const currentText = `export const Current = () => <button data-testid="save-button">Save</button>;\n`;
		const fixture = prOnlyRemovalFixture({ mergeBaseText, prText, upstreamText: "export const Old = () => null;\n// dev\n", currentText });
		const diffHeader = firstDiffHeader(git(fixture.repoRoot, "diff", fixture.baseline, fixture.upstreamSha, "--", file));
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(fixture.baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: fixture.upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["hardcoded-english-reversion"]);
	});

	it("rejects hardcoded English reversion of a nested key after valid upstream evidence", () => {
		const mergeBaseText = "export const Old = () => null;\n";
		const prText = `import { useTranslation } from "react-i18next";\nexport const Old = () => { const { t } = useTranslation("mcp"); return <button data-testid="copy-button">{t("common.configCopied")}</button>; };\n`;
		const currentText = `export const Current = () => <button data-testid="copy-button">Configuration copied</button>;\n`;
		const fixture = prOnlyRemovalFixture({ mergeBaseText, prText, upstreamText: "export const Old = () => null;\n// dev\n", currentText });
		const diffHeader = firstDiffHeader(git(fixture.repoRoot, "diff", fixture.baseline, fixture.upstreamSha, "--", file));
		const nestedResources = { mcp: { common: { configCopied: "Configuration copied" } } };
		const findings = auditRemovedCalls({
			...fixture,
			currentResources: nestedResources,
			baselineResources: nestedResources,
			removedCalls: [
				{
					source: sourceIdentity(fixture.baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: fixture.upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings.map((finding) => finding.type)).toEqual(["hardcoded-english-reversion"]);
	});

	it("distinguishes two removed calls to the same key by source location", () => {
		const repoRoot = makeGitRepo();
		writeRepoFile(
			repoRoot,
			file,
			`export const Old = () => <div><button data-testid="one">{i18n.t("save")}</button><button data-testid="two">{i18n.t("save")}</button></div>;\n`,
		);
		const baseline = commitAll(repoRoot, "baseline");
		const baselineEntries = inventoryAtRef(repoRoot, baseline, file);
		writeRepoFile(repoRoot, file, "export const Removed = () => null;\n");
		const upstreamSha = commitAll(repoRoot, "remove controls");
		const currentEntries = inventoryAtRef(repoRoot, null, file);
		const diffHeader = firstDiffHeader(git(repoRoot, "diff", baseline, upstreamSha, "--", file));
		const findings = auditRemovedCalls({
			repoRoot,
			baseline,
			baselineEntries,
			currentEntries,
			currentResources: resources,
			baselineResources: resources,
			removedCalls: [
				{
					source: sourceIdentity(baselineEntries[0]),
					reason: "upstream-removed",
					upstream: { sha: upstreamSha, file, diffHeader },
				},
			],
		});
		expect(findings).toEqual([
			expect.objectContaining({ type: "removed-call-unexplained", line: baselineEntries[1].line, column: baselineEntries[1].column }),
		]);
	});
});

describe("checker CLI review contract", () => {
	it("requires review normally but permits inventory-only without it", () => {
		const repoRoot = makeGitRepo();
		writeMinimalResources(repoRoot);
		writeRepoFile(repoRoot, "ui/example.tsx", `export const greeting = i18n.t("greeting");\n`);
		const baseline = commitAll(repoRoot, "baseline");
		expect(() => runCli(["--repo-root", repoRoot, "--baseline", baseline])).toThrow("--review is required");
		expect(runCli(["--repo-root", repoRoot, "--baseline", baseline, "--inventory-only"])).toBe(0);
	});

	it("rejects a review file without the required evidence arrays", () => {
		const repoRoot = makeGitRepo();
		writeMinimalResources(repoRoot);
		writeRepoFile(repoRoot, "ui/example.tsx", `export const greeting = i18n.t("greeting");\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const review = path.join(repoRoot, "review.json");
		writeFileSync(review, "{}");
		expect(() => runCli(["--repo-root", repoRoot, "--baseline", baseline, "--review", review])).toThrow(
			"dynamicFamilies, removedCalls, and untranslated arrays",
		);
	});

	it("preserves baseline call findings but gates new findings in normal mode", () => {
		const repoRoot = makeGitRepo();
		writeMinimalResources(repoRoot);
		writeRepoFile(repoRoot, "ui/example.tsx", `export const oldMissing = i18n.t("oldMissing");\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const review = path.join(repoRoot, "review.json");
		writeFileSync(review, JSON.stringify({ dynamicFamilies: [], removedCalls: [], untranslated: [] }));
		const args = ["--repo-root", repoRoot, "--baseline", baseline, "--review", review];
		expect(runCli(args)).toBe(0);
		writeRepoFile(
			repoRoot,
			"ui/example.tsx",
			`export const oldMissing = i18n.t("oldMissing");\nexport const newMissing = i18n.t("newMissing");\n`,
		);
		expect(runCli(args)).toBe(1);
	});

	it("does not audit removed-call reviews outside the selected paths", () => {
		const repoRoot = makeGitRepo();
		writeMinimalResources(repoRoot);
		writeRepoFile(repoRoot, "ui/a.tsx", `export const greeting = i18n.t("greeting");\n`);
		writeRepoFile(repoRoot, "ui/b.tsx", `export const save = i18n.t("save");\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const removedSource = inventoryAtRef(repoRoot, baseline, "ui/b.tsx")[0];
		writeRepoFile(repoRoot, "ui/b.tsx", "export const save = null;\n");
		const review = path.join(repoRoot, "review.json");
		writeFileSync(
			review,
			JSON.stringify({
				dynamicFamilies: [],
				removedCalls: [
					{
						source: sourceIdentity(removedSource),
						reason: "upstream-removed",
						upstream: { sha: "not-evaluated-for-this-domain", file: "ui/b.tsx", diffHeader: "@@ not evaluated @@" },
					},
				],
				untranslated: [],
			}),
		);
		const conflicts = path.join(repoRoot, "conflicted-files.txt");
		writeFileSync(conflicts, "ui/a.tsx\nui/b.tsx\n");
		expect(
			runCli(["--repo-root", repoRoot, "--baseline", baseline, "--review", review, "--conflicted-files", conflicts, "--paths", "ui/a.tsx"]),
		).toBe(0);
	});

	it("inventories an explicitly selected plain TypeScript source", () => {
		const repoRoot = makeGitRepo();
		writeMinimalResources(repoRoot);
		writeRepoFile(repoRoot, "ui/example.ts", `export const greeting: string = i18n.t("greeting");\n`);
		const baseline = commitAll(repoRoot, "baseline");
		const report = path.join(repoRoot, "report.json");
		expect(
			runCli(["--repo-root", repoRoot, "--baseline", baseline, "--inventory-only", "--paths", "ui/example.ts", "--report", report]),
		).toBe(0);
		expect(JSON.parse(readFileSync(report, "utf8")).calls.currentCallCount).toBe(1);
	});
});