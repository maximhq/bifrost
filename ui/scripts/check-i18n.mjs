import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

export const LOCALES = Object.freeze(["de", "en", "es", "fr", "it", "ja", "ko", "pt", "ru", "zh-CN", "zh-TW"]);
export const NAMESPACES = Object.freeze(["common", "shell", "models", "governance", "login", "config", "mcp", "observability"]);

const PLURAL_KEY_PATTERN = /^(.+)_(zero|one|two|few|many|other)$/;
const DEFAULT_REPO_ROOT = fileURLToPath(new URL("../..", import.meta.url));
const DEFAULT_INVENTORY = { namespaces: NAMESPACES, enterpriseNamespaces: new Set() };

function resourceInventory(repoRoot, ref = null) {
	const enterpriseFile = "ui/app/enterprise/lib/i18n/resources.ts";
	const fallbackFile = "ui/app/_fallbacks/enterprise/lib/i18n/resources.ts";
	const enterpriseModule = path.join(repoRoot, enterpriseFile);
	const fallbackModule = path.join(repoRoot, fallbackFile);
	let modulePath = enterpriseModule;
	let source;
	if (ref) {
		try {
			source = readSource(repoRoot, enterpriseFile, ref);
		} catch {
			modulePath = fallbackModule;
			try {
				source = readSource(repoRoot, fallbackFile, ref);
			} catch {
				return DEFAULT_INVENTORY;
			}
		}
	} else {
		modulePath = existsSync(enterpriseModule) ? enterpriseModule : fallbackModule;
		if (!existsSync(modulePath)) {
			return DEFAULT_INVENTORY;
		}
		source = readFileSync(modulePath, "utf8");
	}
	const sourceFile = ts.createSourceFile(modulePath, source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
	const enterpriseNamespaces = new Set();
	function visit(node) {
		if (ts.isVariableDeclaration(node) && ts.isIdentifier(node.name) && node.name.text === "ENTERPRISE_NAMESPACES" && node.initializer) {
			const initializer =
				ts.isAsExpression(node.initializer) || ts.isSatisfiesExpression(node.initializer) ? node.initializer.expression : node.initializer;
			if (ts.isArrayLiteralExpression(initializer)) {
				for (const element of initializer.elements) {
					if (ts.isStringLiteral(element) || ts.isNoSubstitutionTemplateLiteral(element)) {
						enterpriseNamespaces.add(element.text);
					}
				}
			}
		}
		ts.forEachChild(node, visit);
	}
	visit(sourceFile);
	return { namespaces: [...new Set([...NAMESPACES, ...enterpriseNamespaces])], enterpriseNamespaces };
}

function localePath(locale, namespace, enterpriseNamespaces = DEFAULT_INVENTORY.enterpriseNamespaces) {
	return enterpriseNamespaces.has(namespace)
		? `ui/app/enterprise/locales/${locale}/${namespace}.json`
		: `ui/locales/${locale}/${namespace}.json`;
}

function resourceError(type, message) {
	const error = new Error(message);
	error.resourceFindingType = type;
	return error;
}

function isResourceError(resource) {
	return resource instanceof Error && Boolean(resource.resourceFindingType);
}

function getResource(resources, locale, namespace) {
	return resources?.[locale]?.[namespace];
}

function makeFinding(type, locale, namespace, key, message, extra = {}, enterpriseNamespaces = DEFAULT_INVENTORY.enterpriseNamespaces) {
	return {
		type,
		locale,
		namespace,
		key,
		path: localePath(locale, namespace, enterpriseNamespaces),
		message,
		...extra,
	};
}

export function flattenKeys(resource) {
	const keys = new Set();

	function visit(value, prefix) {
		if (value !== null && typeof value === "object" && !Array.isArray(value) && !isResourceError(value)) {
			for (const [key, child] of Object.entries(value)) {
				visit(child, prefix ? `${prefix}.${key}` : key);
			}
			return;
		}
		if (prefix) {
			keys.add(prefix);
		}
	}

	visit(resource, "");
	return keys;
}

function isValidPluralExtra(key, englishKeys, locale) {
	const match = PLURAL_KEY_PATTERN.exec(key);
	if (!match) {
		return false;
	}
	const [, baseKey, category] = match;
	if (!englishKeys.has(`${baseKey}_one`) || !englishKeys.has(`${baseKey}_other`)) {
		return false;
	}
	if (category === "zero") {
		return true;
	}
	return new Intl.PluralRules(locale).resolvedOptions().pluralCategories.includes(category);
}

function validPluralExtras(englishResource, localeResource, locale) {
	const extras = new Set();
	if (isResourceError(englishResource) || isResourceError(localeResource) || !englishResource || !localeResource) {
		return extras;
	}
	const englishKeys = flattenKeys(englishResource);
	for (const key of flattenKeys(localeResource)) {
		if (!englishKeys.has(key) && isValidPluralExtra(key, englishKeys, locale)) {
			extras.add(key);
		}
	}
	return extras;
}

function inventoryFromResources(resources) {
	const enterpriseNamespaces = new Set(Object.keys(resources?.en ?? {}).filter((namespace) => !NAMESPACES.includes(namespace)));
	return { namespaces: [...new Set([...NAMESPACES, ...enterpriseNamespaces])], enterpriseNamespaces };
}

export function validateLocales(currentResources, baselineResources = {}, inventory = inventoryFromResources(currentResources)) {
	const findings = [];

	for (const locale of LOCALES) {
		for (const namespace of inventory.namespaces) {
			const baselineResource = getResource(baselineResources, locale, namespace);
			if (isResourceError(baselineResource)) {
				findings.push(
					makeFinding(
						`baseline-${baselineResource.resourceFindingType}`,
						locale,
						namespace,
						null,
						`Could not load baseline ${localePath(locale, namespace, inventory.enterpriseNamespaces)}: ${baselineResource.message}`,
						{},
						inventory.enterpriseNamespaces,
					),
				);
			}

			const currentResource = getResource(currentResources, locale, namespace);
			if (isResourceError(currentResource)) {
				findings.push(
					makeFinding(
						currentResource.resourceFindingType,
						locale,
						namespace,
						null,
						`Could not load ${localePath(locale, namespace, inventory.enterpriseNamespaces)}: ${currentResource.message}`,
						{},
						inventory.enterpriseNamespaces,
					),
				);
			}
		}
	}

	for (const locale of LOCALES) {
		if (locale === "en") {
			continue;
		}
		for (const namespace of inventory.namespaces) {
			const englishResource = getResource(currentResources, "en", namespace);
			const localeResource = getResource(currentResources, locale, namespace);
			if (!englishResource || !localeResource || isResourceError(englishResource) || isResourceError(localeResource)) {
				continue;
			}

			const englishKeys = flattenKeys(englishResource);
			const localeKeys = flattenKeys(localeResource);
			for (const key of englishKeys) {
				if (!localeKeys.has(key)) {
					findings.push(
						makeFinding(
							"missing-key",
							locale,
							namespace,
							key,
							`${locale}/${namespace} is missing English key ${key}`,
							{},
							inventory.enterpriseNamespaces,
						),
					);
				}
			}
			for (const key of localeKeys) {
				if (!englishKeys.has(key) && !isValidPluralExtra(key, englishKeys, locale)) {
					findings.push(
						makeFinding(
							"unexpected-key",
							locale,
							namespace,
							key,
							`${locale}/${namespace} has non-plural or locale-incompatible extra key ${key}`,
							{},
							inventory.enterpriseNamespaces,
						),
					);
				}
			}
		}
	}

	for (const locale of LOCALES) {
		if (locale === "en") {
			continue;
		}
		for (const namespace of inventory.namespaces) {
			const baselineEnglish = getResource(baselineResources, "en", namespace);
			const baselineLocale = getResource(baselineResources, locale, namespace);
			const currentLocale = getResource(currentResources, locale, namespace);
			if (isResourceError(currentLocale)) {
				continue;
			}
			const currentKeys = currentLocale ? flattenKeys(currentLocale) : new Set();
			for (const key of validPluralExtras(baselineEnglish, baselineLocale, locale)) {
				if (!currentKeys.has(key)) {
					findings.push(
						makeFinding(
							"removed-plural-key",
							locale,
							namespace,
							key,
							`${locale}/${namespace} removed PR-head plural key ${key}`,
							{ reason: "baseline-retention" },
							inventory.enterpriseNamespaces,
						),
					);
				}
			}
		}
	}

	return findings;
}

function loadResource(repoRoot, ref, locale, namespace, inventory) {
	const relativePath = localePath(locale, namespace, inventory.enterpriseNamespaces);
	let source;
	try {
		source = ref
			? execFileSync("git", ["show", `${ref}:${relativePath}`], {
					cwd: repoRoot,
					encoding: "utf8",
					stdio: ["ignore", "pipe", "pipe"],
				})
			: readFileSync(path.join(repoRoot, relativePath), "utf8");
	} catch (error) {
		return resourceError("load-error", error.message);
	}
	try {
		return JSON.parse(source);
	} catch (error) {
		return resourceError("invalid-json", error.message);
	}
}

export function loadResources(repoRoot, ref = null, inventory = resourceInventory(repoRoot)) {
	const resources = {};
	for (const locale of LOCALES) {
		resources[locale] = {};
		for (const namespace of inventory.namespaces) {
			resources[locale][namespace] = loadResource(repoRoot, ref, locale, namespace, inventory);
		}
	}
	return resources;
}

export function countRetainedPluralKeys(currentResources, baselineResources, inventory = DEFAULT_INVENTORY) {
	let count = 0;
	for (const locale of LOCALES) {
		if (locale === "en") {
			continue;
		}
		for (const namespace of inventory.namespaces) {
			const currentLocale = getResource(currentResources, locale, namespace);
			if (!currentLocale || isResourceError(currentLocale)) {
				continue;
			}
			const currentKeys = flattenKeys(currentLocale);
			const baselineExtras = validPluralExtras(
				getResource(baselineResources, "en", namespace),
				getResource(baselineResources, locale, namespace),
				locale,
			);
			for (const key of baselineExtras) {
				if (currentKeys.has(key)) {
					count += 1;
				}
			}
		}
	}
	return count;
}

function sourceLocation(sourceFile, node) {
	const position = sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile));
	return { line: position.line + 1, column: position.character + 1 };
}

function propertyNameText(node) {
	if (!node) {
		return null;
	}
	if (ts.isIdentifier(node) || ts.isStringLiteral(node) || ts.isNumericLiteral(node)) {
		return node.text;
	}
	return null;
}

function optionValue(optionsNode, name) {
	if (!optionsNode || !ts.isObjectLiteralExpression(optionsNode)) {
		return undefined;
	}
	for (const property of optionsNode.properties) {
		if (ts.isPropertyAssignment(property) && propertyNameText(property.name) === name) {
			return property.initializer;
		}
		if (ts.isShorthandPropertyAssignment(property) && property.name.text === name) {
			return property.name;
		}
		if (ts.isMethodDeclaration(property) && propertyNameText(property.name) === name) {
			return property;
		}
	}
	return undefined;
}

function hasOption(optionsNode, name) {
	return optionValue(optionsNode, name) !== undefined;
}

function literalBoolean(node) {
	return node?.kind === ts.SyntaxKind.TrueKeyword ? true : node?.kind === ts.SyntaxKind.FalseKeyword ? false : undefined;
}

function staticKeyValues(expression) {
	if (ts.isStringLiteral(expression) || ts.isNoSubstitutionTemplateLiteral(expression)) {
		return [expression.text];
	}
	if (ts.isParenthesizedExpression(expression) || ts.isAsExpression(expression) || ts.isSatisfiesExpression(expression)) {
		return staticKeyValues(expression.expression);
	}
	if (ts.isConditionalExpression(expression)) {
		const whenTrue = staticKeyValues(expression.whenTrue);
		const whenFalse = staticKeyValues(expression.whenFalse);
		return whenTrue && whenFalse ? [...whenTrue, ...whenFalse] : null;
	}
	if (ts.isBinaryExpression(expression) && expression.operatorToken.kind === ts.SyntaxKind.PlusToken) {
		const left = staticKeyValues(expression.left);
		const right = staticKeyValues(expression.right);
		if (left && right) {
			return left.flatMap((leftValue) => right.map((rightValue) => `${leftValue}${rightValue}`));
		}
	}
	if (ts.isTemplateExpression(expression)) {
		let values = [expression.head.text];
		for (const span of expression.templateSpans) {
			const expressionValues = staticKeyValues(span.expression);
			if (!expressionValues) {
				return null;
			}
			values = values.flatMap((prefix) => expressionValues.map((value) => `${prefix}${value}${span.literal.text}`));
		}
		return values;
	}
	return null;
}

function dynamicPrefix(expression) {
	if (ts.isTemplateExpression(expression)) {
		return expression.head.text;
	}
	if (ts.isBinaryExpression(expression) && expression.operatorToken.kind === ts.SyntaxKind.PlusToken) {
		const left = staticKeyValues(expression.left);
		return left?.length === 1 ? left[0] : expression.left.getText();
	}
	return "";
}

function translationOptions(argument) {
	return {
		count: hasOption(argument, "count"),
		returnObjects: literalBoolean(optionValue(argument, "returnObjects")) === true,
		defaultValue: hasOption(argument, "defaultValue"),
	};
}

function namespaceFromType(typeNode, typeAliases = new Map(), seen = new Set()) {
	if (!typeNode || !ts.isTypeReferenceNode(typeNode)) {
		return undefined;
	}
	const typeName = typeNode.typeName.getText();
	if (typeName !== "TFunction") {
		if (seen.has(typeName) || !typeAliases.has(typeName)) {
			return undefined;
		}
		seen.add(typeName);
		return namespaceFromType(typeAliases.get(typeName), typeAliases, seen);
	}
	const namespaceArgument = typeNode.typeArguments?.[0];
	if (!namespaceArgument) {
		return "common";
	}
	if (ts.isLiteralTypeNode(namespaceArgument) && ts.isStringLiteral(namespaceArgument.literal)) {
		return namespaceArgument.literal.text;
	}
	if (ts.isTupleTypeNode(namespaceArgument) && namespaceArgument.elements.length === 1) {
		const element = namespaceArgument.elements[0];
		if (ts.isLiteralTypeNode(element) && ts.isStringLiteral(element.literal)) {
			return element.literal.text;
		}
	}
	return null;
}

function makeScope(parent = null) {
	return { parent, namespace: parent ? parent.namespace : "common", keyPrefix: parent ? parent.keyPrefix : null, aliases: new Map() };
}

function applyKeyPrefix(keyPrefix, key) {
	return keyPrefix ? `${keyPrefix}.${key}` : key;
}

function lookupAlias(scope, name) {
	for (let current = scope; current; current = current.parent) {
		if (current.aliases.has(name)) {
			return current.aliases.get(name);
		}
	}
	return undefined;
}

function translationNamespace(argument, fallback) {
	const namespaceNode = optionValue(argument, "ns");
	if (!namespaceNode) {
		return fallback;
	}
	return ts.isStringLiteral(namespaceNode) || ts.isNoSubstitutionTemplateLiteral(namespaceNode) ? namespaceNode.text : null;
}

function jsxAttributeValue(attribute) {
	if (!attribute.initializer) {
		return null;
	}
	if (ts.isStringLiteral(attribute.initializer)) {
		return attribute.initializer.text;
	}
	if (ts.isJsxExpression(attribute.initializer) && attribute.initializer.expression) {
		const expression = attribute.initializer.expression;
		if (ts.isStringLiteral(expression) || ts.isNoSubstitutionTemplateLiteral(expression)) {
			return expression.text;
		}
	}
	return null;
}

function behaviorAnchorsForNode(node, sourceFile) {
	const anchors = new Set();
	for (let current = node; current; current = current.parent) {
		let openingElement = null;
		if (ts.isJsxElement(current)) {
			openingElement = current.openingElement;
		} else if (ts.isJsxSelfClosingElement(current)) {
			openingElement = current;
		}
		if (openingElement) {
			for (const attribute of openingElement.attributes.properties) {
				if (!ts.isJsxAttribute(attribute)) {
					continue;
				}
				const name = attribute.name.getText(sourceFile);
				if (["data-testid", "data-test", "id"].includes(name)) {
					const value = jsxAttributeValue(attribute);
					if (value) {
						anchors.add(value);
					}
				} else if (
					/^on[A-Z]/.test(name) &&
					attribute.initializer &&
					ts.isJsxExpression(attribute.initializer) &&
					attribute.initializer.expression
				) {
					anchors.add(attribute.initializer.expression.getText(sourceFile));
				}
			}
		}
		if (ts.isFunctionDeclaration(current) && current.name) {
			anchors.add(current.name.text);
		}
		if (ts.isVariableDeclaration(current) && ts.isIdentifier(current.name)) {
			anchors.add(current.name.text);
		}
	}
	return [...anchors].sort();
}

function sharesBehavior(left, right) {
	const rightAnchors = new Set(right.behaviorAnchors ?? []);
	return (left.behaviorAnchors ?? []).some((anchor) => rightAnchors.has(anchor));
}

export function analyzeTranslationCalls(sourceFile) {
	const entries = [];
	const typeAliases = new Map();
	for (const statement of sourceFile.statements) {
		if (ts.isTypeAliasDeclaration(statement)) {
			typeAliases.set(statement.name.text, statement.type);
		}
	}

	function addEntry(node, namespace, keyExpression, optionsNode, kind, optionOverrides = {}, keyPrefix = null) {
		const location = sourceLocation(sourceFile, node);
		const base = {
			file: sourceFile.fileName.replaceAll("\\", "/"),
			...location,
			endLine: sourceFile.getLineAndCharacterOfPosition(node.getEnd()).line + 1,
			namespace: translationNamespace(optionsNode, namespace),
			namespaceExplicit: hasOption(optionsNode, "ns") || optionOverrides.namespaceExplicit === true,
			kind,
			behaviorAnchors: behaviorAnchorsForNode(node, sourceFile),
			options: { ...translationOptions(optionsNode), ...optionOverrides },
		};
		const keys = staticKeyValues(keyExpression);
		if (keys) {
			for (const key of keys) {
				entries.push({ ...base, key: applyKeyPrefix(keyPrefix, key), rawKey: key, dynamic: false, prefix: null });
			}
			return;
		}
		entries.push({
			...base,
			key: null,
			dynamic: true,
			prefix: applyKeyPrefix(keyPrefix, dynamicPrefix(keyExpression)),
			rawPrefix: dynamicPrefix(keyExpression),
		});
	}

	function visit(node, scope) {
		if (ts.isSourceFile(node)) {
			const rootScope = makeScope();
			ts.forEachChild(node, (child) => visit(child, rootScope));
			return;
		}
		if (ts.isFunctionLike(node)) {
			const functionScope = makeScope(scope);
			for (const parameter of node.parameters) {
				if (ts.isIdentifier(parameter.name)) {
					const namespace = namespaceFromType(parameter.type, typeAliases);
					if (namespace !== undefined) {
						functionScope.aliases.set(parameter.name.text, { namespace, keyPrefix: null });
					}
				}
			}
			ts.forEachChild(node, (child) => visit(child, functionScope));
			return;
		}
		if (ts.isVariableDeclaration(node) && node.initializer && ts.isCallExpression(node.initializer)) {
			const initializer = node.initializer;
			if (ts.isIdentifier(initializer.expression) && initializer.expression.text === "useTranslation") {
				const namespaceArgument = initializer.arguments[0];
				const namespace = !namespaceArgument
					? "common"
					: ts.isStringLiteral(namespaceArgument) || ts.isNoSubstitutionTemplateLiteral(namespaceArgument)
						? namespaceArgument.text
						: null;
				const keyPrefixNode = optionValue(initializer.arguments[1], "keyPrefix");
				const keyPrefix =
					keyPrefixNode && (ts.isStringLiteral(keyPrefixNode) || ts.isNoSubstitutionTemplateLiteral(keyPrefixNode))
						? keyPrefixNode.text
						: null;
				scope.namespace = namespace;
				scope.keyPrefix = keyPrefix;
				if (ts.isObjectBindingPattern(node.name)) {
					for (const element of node.name.elements) {
						if (
							element.propertyName &&
							ts.isIdentifier(element.propertyName) &&
							element.propertyName.text === "t" &&
							ts.isIdentifier(element.name)
						) {
							scope.aliases.set(element.name.text, { namespace, keyPrefix });
						} else if (!element.propertyName && ts.isIdentifier(element.name) && element.name.text === "t") {
							scope.aliases.set("t", { namespace, keyPrefix });
						}
					}
				}
			}
		}
		if (ts.isCallExpression(node) && node.arguments.length > 0) {
			let namespace;
			let keyPrefix = null;
			if (ts.isIdentifier(node.expression)) {
				const alias = lookupAlias(scope, node.expression.text);
				namespace = alias?.namespace;
				keyPrefix = alias?.keyPrefix ?? null;
			} else if (
				ts.isPropertyAccessExpression(node.expression) &&
				node.expression.name.text === "t" &&
				ts.isIdentifier(node.expression.expression) &&
				node.expression.expression.text === "i18n"
			) {
				namespace = "common";
			}
			if (namespace !== undefined) {
				addEntry(node, namespace, node.arguments[0], node.arguments[1], "t", {}, keyPrefix);
			}
		}
		if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
			if (node.tagName.getText(sourceFile) === "Trans") {
				let keyExpression = null;
				let namespaceNode = null;
				let translationFunctionNode = null;
				const optionOverrides = {};
				for (const attribute of node.attributes.properties) {
					if (!ts.isJsxAttribute(attribute)) {
						continue;
					}
					const name = attribute.name.getText(sourceFile);
					if (name === "i18nKey" && attribute.initializer) {
						if (ts.isStringLiteral(attribute.initializer)) {
							keyExpression = attribute.initializer;
						} else if (ts.isJsxExpression(attribute.initializer) && attribute.initializer.expression) {
							keyExpression = attribute.initializer.expression;
						}
					}
					if (name === "ns" && attribute.initializer) {
						optionOverrides.namespaceExplicit = true;
						if (ts.isStringLiteral(attribute.initializer)) {
							namespaceNode = attribute.initializer;
						} else if (ts.isJsxExpression(attribute.initializer) && attribute.initializer.expression) {
							namespaceNode = attribute.initializer.expression;
						}
					}
					if (name === "t" && attribute.initializer && ts.isJsxExpression(attribute.initializer) && attribute.initializer.expression) {
						translationFunctionNode = attribute.initializer.expression;
					}
					if (name === "count" && attribute.initializer) {
						optionOverrides.count = true;
					}
				}
				if (keyExpression) {
					const inheritedAlias =
						translationFunctionNode && ts.isIdentifier(translationFunctionNode) ? lookupAlias(scope, translationFunctionNode.text) : null;
					const inheritedNamespace = translationFunctionNode ? (inheritedAlias?.namespace ?? null) : scope.namespace;
					const inheritedKeyPrefix = translationFunctionNode ? (inheritedAlias?.keyPrefix ?? null) : scope.keyPrefix;
					const namespace = !namespaceNode
						? inheritedNamespace
						: ts.isStringLiteral(namespaceNode) || ts.isNoSubstitutionTemplateLiteral(namespaceNode)
							? namespaceNode.text
							: null;
					addEntry(node, namespace, keyExpression, undefined, "trans", optionOverrides, inheritedKeyPrefix);
				}
			}
		}
		ts.forEachChild(node, (child) => visit(child, scope));
	}

	visit(sourceFile, null);
	return entries;
}

function callFinding(type, entry, message, extra = {}) {
	return {
		type,
		file: entry.file,
		line: entry.line,
		column: entry.column,
		namespace: entry.namespace,
		key: entry.key,
		message,
		...extra,
	};
}

function resourceSubtree(resource, key) {
	if (resource === null || typeof resource !== "object") {
		return undefined;
	}
	if (Object.hasOwn(resource, key)) {
		return resource[key];
	}
	let value = resource;
	for (const segment of key.split(".")) {
		if (value === null || typeof value !== "object" || !Object.hasOwn(value, segment)) {
			return undefined;
		}
		value = value[segment];
	}
	return value;
}

function nonEmptyContainer(value) {
	return value !== null && typeof value === "object" && Object.keys(value).length > 0;
}

export function validateCall(entry, englishResources) {
	const resources = englishResources?.en ?? englishResources;
	if (entry.namespace === null) {
		return [callFinding("unresolved-namespace", entry, `Translation call ${entry.key ?? entry.prefix} has an unresolved namespace`)];
	}
	if (entry.dynamic) {
		return [callFinding("unresolved-dynamic", entry, `Dynamic translation family ${entry.prefix || "<unknown>"} requires review`)];
	}
	const namespace = entry.namespace;
	const key = entry.key;
	const resource = resources?.[namespace];
	const findings = [];
	if (entry.options.returnObjects) {
		const value = resourceSubtree(resource, key);
		if (value !== null && typeof value === "object" && Object.keys(value).length === 0) {
			findings.push(callFinding("empty-return-object", entry, `returnObjects key ${entry.key} resolves to an empty subtree`));
		} else if (!nonEmptyContainer(value)) {
			findings.push(callFinding("invalid-return-object", entry, `returnObjects key ${entry.key} does not resolve to an object or array`));
		}
	} else if (entry.options.count) {
		const one = resourceSubtree(resource, `${key}_one`);
		const other = resourceSubtree(resource, `${key}_other`);
		if (one !== undefined || other !== undefined) {
			if (typeof one !== "string" || typeof other !== "string") {
				findings.push(callFinding("invalid-plural-family", entry, `Count key ${entry.key} requires _one and _other string values`));
			}
			const zero = resourceSubtree(resource, `${key}_zero`);
			if (zero !== undefined && typeof zero !== "string") {
				findings.push(callFinding("invalid-plural-family", entry, `Plural override ${entry.key}_zero must be a string`));
			}
		} else {
			const value = resourceSubtree(resource, key);
			if (typeof value !== "string") {
				findings.push(callFinding("missing-key", entry, `Missing English translation ${namespace}.${key}`));
			} else if (!value.includes("{{count}}")) {
				findings.push(
					callFinding("missing-count-interpolation", entry, `Count key ${entry.key} has no plural family or {{count}} interpolation`),
				);
			}
		}
	} else if (typeof resourceSubtree(resource, key) !== "string") {
		findings.push(callFinding("missing-key", entry, `Missing English translation ${namespace}.${key}`));
	}
	if (entry.options.defaultValue) {
		findings.push(callFinding("default-value", entry, `Translation call ${entry.key} uses defaultValue`));
	}
	return findings;
}

function producerIdentity(entry) {
	return `${entry.file}:${entry.line}:${entry.column}`;
}

function recordedEnglishResult(englishResults, candidate) {
	if (Array.isArray(englishResults)) {
		return englishResults.find((result) => result?.key === candidate)?.result;
	}
	return englishResults?.[candidate];
}

function resultIsResolved(result) {
	return result === true || result === "resolved" || result?.status === "resolved";
}

export function validateDynamicFamilies(entries, englishResources, dynamicFamilies = []) {
	const findings = [];
	const groups = new Map();
	for (const entry of entries.filter((candidate) => candidate.dynamic)) {
		const prefix = entry.prefix ?? "";
		const identity = `${entry.namespace}\0${prefix}`;
		if (!groups.has(identity)) {
			groups.set(identity, { namespace: entry.namespace, prefix, producers: [] });
		}
		groups.get(identity).producers.push(entry);
	}
	for (const { namespace, prefix, producers } of groups.values()) {
		const review = dynamicFamilies.find(
			(family) => family?.prefix === prefix && (family.namespace === undefined || family.namespace === namespace),
		);
		if (!review) {
			for (const entry of producers) {
				findings.push(callFinding("unresolved-dynamic", entry, `Dynamic translation family ${prefix || "<unknown>"} has no review`));
			}
			continue;
		}
		const expectedProducers = producers.map(producerIdentity);
		const recordedProducers = new Set(
			(review.producers ?? []).map((producer) =>
				typeof producer === "string" ? producer : producer ? `${producer.file}:${producer.line}:${producer.column}` : "",
			),
		);
		const candidates = Array.isArray(review.candidates) ? review.candidates.filter((candidate) => typeof candidate === "string") : [];
		const complete =
			expectedProducers.every((producer) => recordedProducers.has(producer)) &&
			candidates.length > 0 &&
			candidates.every((candidate) => Object.hasOwn(review.englishResults ?? {}, candidate) || Array.isArray(review.englishResults));
		if (!complete) {
			findings.push(
				callFinding(
					"incomplete-dynamic-review",
					producers[0],
					`Dynamic family ${prefix || "<unknown>"} lacks complete producer/candidate evidence`,
				),
			);
			continue;
		}
		const heterogeneous = new Set(producers.map((producer) => JSON.stringify(producer.options))).size > 1;
		const candidateProducers =
			review.candidateProducers && typeof review.candidateProducers === "object" && !Array.isArray(review.candidateProducers)
				? review.candidateProducers
				: null;
		if ((review.candidateProducers !== undefined && !candidateProducers) || (heterogeneous && !candidateProducers)) {
			findings.push(
				callFinding(
					"incomplete-dynamic-review",
					producers[0],
					`Heterogeneous dynamic family ${prefix} requires candidate-producer mappings`,
				),
			);
			continue;
		}
		const mappedProducers = new Set();
		if (candidateProducers) {
			for (const candidate of candidates) {
				if (!Object.hasOwn(candidateProducers, candidate)) {
					continue;
				}
				const mappedIdentities = candidateProducers[candidate];
				if (Array.isArray(mappedIdentities) && mappedIdentities.every((identity) => expectedProducers.includes(identity))) {
					for (const identity of mappedIdentities) {
						mappedProducers.add(identity);
					}
				}
			}
			if (expectedProducers.some((producer) => !mappedProducers.has(producer))) {
				findings.push(callFinding("incomplete-dynamic-review", producers[0], `Dynamic family ${prefix} does not cover every producer`));
				continue;
			}
		}
		for (const candidate of candidates) {
			const result = recordedEnglishResult(review.englishResults, candidate);
			const hasMapping = candidateProducers && Object.hasOwn(candidateProducers, candidate);
			const mappedIdentities = hasMapping ? candidateProducers[candidate] : null;
			if (
				(heterogeneous && !hasMapping) ||
				(hasMapping &&
					(!Array.isArray(mappedIdentities) ||
						mappedIdentities.length === 0 ||
						mappedIdentities.some((identity) => !expectedProducers.includes(identity))))
			) {
				findings.push(
					callFinding("incomplete-dynamic-review", producers[0], `Dynamic candidate ${candidate} lacks a valid producer mapping`),
				);
				continue;
			}
			const candidateProducerSet = hasMapping
				? producers.filter((producer) => mappedIdentities.includes(producerIdentity(producer)))
				: producers;
			for (const producer of candidateProducerSet) {
				const candidateEntry = { ...producer, key: candidate, dynamic: false };
				if (!resultIsResolved(result) || validateCall(candidateEntry, englishResources).length > 0) {
					findings.push(
						callFinding("dynamic-candidate-unresolved", candidateEntry, `Dynamic candidate ${candidate} is not resolved in English`),
					);
				}
			}
		}
	}
	return findings;
}

function findingBaselineIdentity(finding) {
	return [finding.type, finding.file, finding.namespace, finding.key].join("\0");
}

export function diffCallFindings(currentFindings, baselineFindings, conflictedFiles = []) {
	const baselineCounts = new Map();
	for (const finding of baselineFindings) {
		const identity = findingBaselineIdentity(finding);
		baselineCounts.set(identity, (baselineCounts.get(identity) ?? 0) + 1);
	}
	const conflicts = new Set(conflictedFiles);
	const newFindings = [];
	const mergeTouchedFindings = [];
	const baselineExceptions = [];
	for (const finding of currentFindings) {
		if (conflicts.has(finding.file)) {
			mergeTouchedFindings.push(finding);
			continue;
		}
		const identity = findingBaselineIdentity(finding);
		const baselineCount = baselineCounts.get(identity) ?? 0;
		if (baselineCount > 0) {
			baselineCounts.set(identity, baselineCount - 1);
			baselineExceptions.push(finding);
		} else {
			newFindings.push(finding);
		}
	}
	return { newFindings, mergeTouchedFindings, baselineExceptions };
}

function callIdentity(entry) {
	return [entry?.file, entry?.line, entry?.column, entry?.namespace, entry?.key].join("\0");
}

function shiftedCallGroup(entry) {
	return [entry.file, entry.namespace, entry.key, entry.kind, entry.prefix].join("\0");
}

function removedCallsAfterShiftMatching(baselineEntries, currentEntries, removedCalls) {
	const targetUseCounts = new Map();
	for (const record of removedCalls) {
		if (record?.reason === "equivalent-migration") {
			const identity = callIdentity(record.target);
			targetUseCounts.set(identity, (targetUseCounts.get(identity) ?? 0) + 1);
		}
	}
	const consolidationSources = new Set(
		removedCalls
			.filter((record) => record?.reason === "equivalent-migration" && targetUseCounts.get(callIdentity(record.target)) > 1)
			.map((record) => callIdentity(record.source)),
	);
	const currentIdentities = new Set(currentEntries.map(callIdentity));
	const baselineIdentities = new Set(baselineEntries.map(callIdentity));
	const available = currentEntries.filter((entry) => !baselineIdentities.has(callIdentity(entry)));
	const removedEntries = [];
	for (const baselineEntry of baselineEntries) {
		if (currentIdentities.has(callIdentity(baselineEntry))) {
			continue;
		}
		if (consolidationSources.has(callIdentity(baselineEntry))) {
			removedEntries.push(baselineEntry);
			continue;
		}
		const baselineAnchors = baselineEntry.behaviorAnchors ?? [];
		const candidateIndex = available.findIndex(
			(currentEntry) =>
				shiftedCallGroup(currentEntry) === shiftedCallGroup(baselineEntry) &&
				(baselineAnchors.length > 0 ? sharesBehavior(currentEntry, baselineEntry) : (currentEntry.behaviorAnchors ?? []).length === 0),
		);
		if (candidateIndex >= 0) {
			available.splice(candidateIndex, 1);
		} else {
			removedEntries.push(baselineEntry);
		}
	}
	return removedEntries;
}

function readSource(repoRoot, file, ref = null) {
	if (ref) {
		return execFileSync("git", ["show", `${ref}:${file}`], {
			cwd: repoRoot,
			encoding: "utf8",
			stdio: ["ignore", "pipe", "pipe"],
		});
	}
	return readFileSync(path.join(repoRoot, file), "utf8");
}

function gitOutput(repoRoot, args) {
	return execFileSync("git", args, {
		cwd: repoRoot,
		encoding: "utf8",
		stdio: ["ignore", "pipe", "pipe"],
	});
}

function parseDiffHunks(diff) {
	const hunks = [];
	let current = null;
	for (const line of diff.split("\n")) {
		const match = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/.exec(line);
		if (match) {
			current = {
				header: line,
				oldStart: Number(match[1]),
				newStart: Number(match[3]),
				lines: [],
			};
			hunks.push(current);
		} else if (current) {
			current.lines.push(line);
		}
	}
	return hunks;
}

function diffLineRecords(hunk) {
	const records = [];
	let oldLine = hunk.oldStart;
	let newLine = hunk.newStart;
	for (const line of hunk.lines) {
		if (line.startsWith("\\")) {
			continue;
		}
		if (line.startsWith("-") && !line.startsWith("---")) {
			records.push({ type: "removed", line, oldLine });
			oldLine += 1;
		} else if (line.startsWith("+") && !line.startsWith("+++")) {
			records.push({ type: "added", line, newLine });
			newLine += 1;
		} else {
			oldLine += 1;
			newLine += 1;
		}
	}
	return records;
}

function hunkContainsAddedCall(hunk, target) {
	const targetText = target.dynamic ? (target.rawPrefix ?? target.prefix) : (target.rawKey ?? target.key);
	return diffLineRecords(hunk).some(
		(record) =>
			record.type === "added" &&
			record.newLine >= target.line &&
			record.newLine <= (target.endLine ?? target.line) &&
			typeof targetText === "string" &&
			record.line.includes(targetText),
	);
}

function hunkRemovesBehavior(hunk, behavior) {
	return diffLineRecords(hunk).some(
		(record) => record.type === "removed" && record.oldLine >= behavior.line && record.oldLine <= (behavior.endLine ?? behavior.line),
	);
}

function lineIncludesQuotedKey(line, key) {
	return [`"${key}"`, `'${key}'`, `\`${key}\``].some((literal) => line.includes(literal));
}

function hunkAddsBehaviorText(hunk, source, copy) {
	return diffLineRecords(hunk).some(
		(record) =>
			record.type === "added" &&
			((typeof source.key === "string" && lineIncludesQuotedKey(record.line, source.rawKey ?? source.key)) ||
				(copy && record.line.includes(copy))),
	);
}

function evidenceFinding(type, source, message, extra = {}) {
	return callFinding(type, source, message, extra);
}

function upstreamCopy(entry, baselineResources) {
	if (typeof entry.key !== "string") {
		return null;
	}
	const value = resourceSubtree(baselineResources?.en?.[entry.namespace] ?? baselineResources?.[entry.namespace], entry.key);
	return typeof value === "string" && value.length > 0 ? value : null;
}

function parseBehaviorSource(file, source) {
	return ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, scriptKind(file));
}

function copyBehaviorNodes(sourceFile, copy) {
	if (!copy) {
		return [];
	}
	const behaviors = [];
	function visit(node) {
		const isJsxCopy = ts.isJsxText(node) && node.text.trim() === copy;
		const isExpressionCopy =
			(ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) &&
			node.text === copy &&
			(ts.isJsxExpression(node.parent) || ts.isJsxAttribute(node.parent));
		if (isJsxCopy || isExpressionCopy) {
			behaviors.push({
				line: sourceFile.getLineAndCharacterOfPosition(node.getStart(sourceFile)).line + 1,
				endLine: sourceFile.getLineAndCharacterOfPosition(node.getEnd()).line + 1,
				behaviorAnchors: behaviorAnchorsForNode(node, sourceFile),
			});
		}
		ts.forEachChild(node, visit);
	}
	visit(sourceFile);
	return behaviors;
}

function matchingSourceBehaviors(sourceFile, source, copy) {
	const translationBehaviors = analyzeTranslationCalls(sourceFile).filter(
		(entry) => entry.namespace === source.namespace && entry.key === source.key && sharesBehavior(entry, source),
	);
	const copyBehaviors = copyBehaviorNodes(sourceFile, copy).filter((entry) => sharesBehavior(entry, source));
	return [...translationBehaviors, ...copyBehaviors];
}

export function auditRemovedCalls({
	repoRoot,
	baseline,
	baselineEntries,
	currentEntries,
	currentResources,
	baselineResources,
	removedCalls = [],
}) {
	const findings = [];
	const removedEntries = removedCallsAfterShiftMatching(baselineEntries, currentEntries, removedCalls);
	const removedByIdentity = new Map(removedEntries.map((entry) => [callIdentity(entry), entry]));
	const reviewBySource = new Map();
	for (const record of removedCalls) {
		const identity = callIdentity(record?.source);
		if (reviewBySource.has(identity)) {
			findings.push(evidenceFinding("duplicate-removed-call-review", record.source, `Duplicate removed-call review for ${identity}`));
		} else {
			reviewBySource.set(identity, record);
		}
		if (!removedByIdentity.has(identity)) {
			findings.push(evidenceFinding("stale-removed-call-review", record.source, `Removed-call review does not match a PR-head removal`));
		}
	}

	const targetUseCounts = new Map();
	for (const record of removedCalls) {
		if (record?.reason === "equivalent-migration") {
			const identity = callIdentity(record.target);
			targetUseCounts.set(identity, (targetUseCounts.get(identity) ?? 0) + 1);
		}
	}

	for (const source of removedEntries) {
		const record = reviewBySource.get(callIdentity(source));
		if (!record) {
			findings.push(evidenceFinding("removed-call-unexplained", source, `PR-head translation call was removed without evidence`));
			continue;
		}
		if (record.reason === "equivalent-migration") {
			const targetRecord = record.target;
			const target = currentEntries.find((entry) => callIdentity(entry) === callIdentity(targetRecord));
			if (!target) {
				findings.push(
					evidenceFinding("migration-target-not-found", source, `Equivalent-migration target does not resolve to a current call`),
				);
				continue;
			}
			let diff = "";
			try {
				diff = gitOutput(repoRoot, ["diff", baseline, "--", target.file]);
			} catch {
				// The missing/invalid diff is reported as evidence failure below.
			}
			const hunk = parseDiffHunks(diff).find((candidate) => candidate.header === targetRecord.diffHeader);
			const targetWasPresent = baselineEntries.some((entry) => callIdentity(entry) === callIdentity(target));
			if (targetWasPresent || !hunk || !hunkContainsAddedCall(hunk, target)) {
				findings.push(
					evidenceFinding(
						"migration-target-unchanged",
						source,
						`Equivalent-migration target is not an added call in the recorded diff hunk`,
					),
				);
				continue;
			}
			if (
				typeof targetRecord.behaviorAnchor !== "string" ||
				targetRecord.behaviorAnchor.length === 0 ||
				!(source.behaviorAnchors ?? []).includes(targetRecord.behaviorAnchor) ||
				!(target.behaviorAnchors ?? []).includes(targetRecord.behaviorAnchor)
			) {
				findings.push(
					evidenceFinding("migration-behavior-mismatch", source, `Migration behavior anchor is absent from source or target behavior`),
				);
				continue;
			}
			const targetFindings = target.dynamic ? [] : validateCall(target, currentResources);
			if (targetFindings.length > 0) {
				findings.push(
					evidenceFinding("migration-target-invalid", source, `Equivalent-migration target has unresolved English resources`, {
						targetFindings,
					}),
				);
				continue;
			}
			if ((targetUseCounts.get(callIdentity(targetRecord)) ?? 0) > 1) {
				if (
					typeof record.consolidation?.reason !== "string" ||
					record.consolidation.reason.length === 0 ||
					typeof record.consolidation?.reviewer !== "string" ||
					record.consolidation.reviewer.length === 0
				) {
					findings.push(evidenceFinding("consolidation-required", source, `Many-to-one migration requires separate consolidation review`));
				}
			}
			continue;
		}
		if (record.reason === "upstream-removed") {
			const upstream = record.upstream;
			const copy = upstreamCopy(source, baselineResources);
			let hunk = null;
			let baselineBehaviors = [];
			let upstreamBehaviors = [];
			try {
				if (!upstream?.sha || upstream.file !== source.file) {
					throw new Error("invalid upstream evidence");
				}
				const mergeBase = gitOutput(repoRoot, ["merge-base", baseline, upstream.sha]).trim();
				const mergeBaseSource = parseBehaviorSource(upstream.file, readSource(repoRoot, upstream.file, mergeBase));
				baselineBehaviors = matchingSourceBehaviors(mergeBaseSource, source, copy);
				let diff = gitOutput(repoRoot, ["diff", mergeBase, upstream.sha, "--", upstream.file]);
				if (baselineBehaviors.length === 0) {
					const prHeadSource = parseBehaviorSource(upstream.file, readSource(repoRoot, upstream.file, baseline));
					const prHeadBehaviors = matchingSourceBehaviors(prHeadSource, source, copy);
					if (prHeadBehaviors.length > 0) {
						baselineBehaviors = prHeadBehaviors;
						diff = gitOutput(repoRoot, ["diff", baseline, upstream.sha, "--", upstream.file]);
					}
				}
				hunk = parseDiffHunks(diff).find((candidate) => candidate.header === upstream.diffHeader) ?? null;
				if (hunk) {
					const upstreamSource = parseBehaviorSource(upstream.file, readSource(repoRoot, upstream.file, upstream.sha));
					upstreamBehaviors = matchingSourceBehaviors(upstreamSource, source, copy);
				}
			} catch {
				hunk = null;
			}
			const removesBehavior = hunk && baselineBehaviors.some((behavior) => hunkRemovesBehavior(hunk, behavior));
			const retainsBehavior = upstreamBehaviors.length > 0 || (hunk && hunkAddsBehaviorText(hunk, source, copy));
			if (!hunk || !removesBehavior || retainsBehavior) {
				findings.push(
					evidenceFinding("upstream-evidence-invalid", source, `Upstream diff hunk does not remove the corresponding behavior or copy`),
				);
				continue;
			}
			try {
				const currentSource = parseBehaviorSource(source.file, readSource(repoRoot, source.file));
				const hardcodedReversion = copyBehaviorNodes(currentSource, copy).some((behavior) => sharesBehavior(behavior, source));
				if (hardcodedReversion) {
					findings.push(
						evidenceFinding("hardcoded-english-reversion", source, `Current source reintroduces removed English behavior or copy`),
					);
				}
			} catch {
				// A missing current file cannot reintroduce hardcoded copy.
			}
			continue;
		}
		findings.push(
			evidenceFinding("removed-call-reason-invalid", source, `Removed-call reason must be upstream-removed or equivalent-migration`),
		);
	}
	return findings;
}

function parseArgs(argv) {
	const options = { repoRoot: DEFAULT_REPO_ROOT, inventoryOnly: false };
	const valuedOptions = new Map([
		["--repo-root", "repoRoot"],
		["--baseline", "baseline"],
		["--conflicted-files", "conflictedFiles"],
		["--review", "review"],
		["--report", "report"],
		["--paths", "paths"],
	]);

	for (let index = 0; index < argv.length; index += 1) {
		const argument = argv[index];
		if (argument === "--inventory-only") {
			options.inventoryOnly = true;
			continue;
		}
		const option = valuedOptions.get(argument);
		if (!option || index + 1 >= argv.length) {
			throw new Error(`Unknown or incomplete argument: ${argument}`);
		}
		options[option] = argv[index + 1];
		index += 1;
	}
	return options;
}

function selectedSourceFiles(repoRoot, options, conflictedFiles) {
	if (options.paths) {
		return [
			...new Set(
				options.paths
					.split(",")
					.map((file) => file.trim())
					.filter(Boolean),
			),
		].sort();
	}
	const output = gitOutput(repoRoot, ["ls-files", "--cached", "--others", "--exclude-standard", "--", "ui"]);
	const files = output.split("\n").filter((file) => /\.(?:[cm]?[jt]sx?)$/.test(file));
	return [...new Set([...files, ...conflictedFiles])].sort();
}

function scriptKind(file) {
	if (file.endsWith(".tsx")) {
		return ts.ScriptKind.TSX;
	}
	if (file.endsWith(".jsx")) {
		return ts.ScriptKind.JSX;
	}
	if (file.endsWith(".js") || file.endsWith(".mjs") || file.endsWith(".cjs")) {
		return ts.ScriptKind.JS;
	}
	return ts.ScriptKind.TS;
}

function inventoryForFiles(repoRoot, files, ref = null) {
	const entries = [];
	for (const file of files) {
		if (!/\.(?:[cm]?[jt]sx?)$/.test(file)) {
			continue;
		}
		let source;
		try {
			source = readSource(repoRoot, file, ref);
		} catch {
			continue;
		}
		const sourceFile = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true, scriptKind(file));
		entries.push(...analyzeTranslationCalls(sourceFile));
	}
	return entries;
}

function callFindings(entries, resources, review) {
	return [
		...entries.filter((entry) => !entry.dynamic).flatMap((entry) => validateCall(entry, resources)),
		...validateDynamicFamilies(entries, resources, review.dynamicFamilies),
	];
}

function emptyReview() {
	return { dynamicFamilies: [], removedCalls: [], untranslated: [] };
}

function readReview(options) {
	if (!options.review) {
		return emptyReview();
	}
	const reviewPath = path.resolve(options.review);
	if (!existsSync(reviewPath)) {
		if (options.inventoryOnly) {
			return emptyReview();
		}
		throw new Error(`Review file does not exist: ${options.review}`);
	}
	const review = JSON.parse(readFileSync(reviewPath, "utf8"));
	if (!review || !Array.isArray(review.dynamicFamilies) || !Array.isArray(review.removedCalls) || !Array.isArray(review.untranslated)) {
		throw new Error("Review file must contain dynamicFamilies, removedCalls, and untranslated arrays");
	}
	return review;
}

function validateUntranslated(review) {
	const findings = [];
	for (const record of review.untranslated) {
		if (
			typeof record?.file !== "string" ||
			record.file.length === 0 ||
			typeof record?.text !== "string" ||
			record.text.length === 0 ||
			!["new-upstream-copy", "english-fallback-value"].includes(record?.reason)
		) {
			findings.push({
				type: "invalid-untranslated-review",
				file: record?.file ?? null,
				text: record?.text ?? null,
				message: "Untranslated copy requires file, text, and a supported reason",
			});
		}
	}
	return findings;
}

function readConflictedFiles(options) {
	if (!options.conflictedFiles) {
		return [];
	}
	return [
		...new Set(
			readFileSync(path.resolve(options.conflictedFiles), "utf8")
				.split("\n")
				.map((file) => file.trim())
				.filter(Boolean),
		),
	];
}

export function runCli(argv = process.argv.slice(2)) {
	const options = parseArgs(argv);
	if (!options.baseline) {
		throw new Error("--baseline is required");
	}
	const repoRoot = path.resolve(options.repoRoot);
	if (!options.inventoryOnly && !options.review) {
		throw new Error("--review is required unless --inventory-only is set");
	}
	const review = readReview(options);
	const conflictedFiles = readConflictedFiles(options);
	const selectedFiles = selectedSourceFiles(repoRoot, options, conflictedFiles);
	const selectedFileSet = new Set(selectedFiles);
	const auditedConflicts = conflictedFiles.filter((file) => selectedFileSet.has(file));

	const inventory = resourceInventory(repoRoot);
	const baselineInventory = resourceInventory(repoRoot, options.baseline);
	const currentResources = loadResources(repoRoot, null, inventory);
	const baselineResources = loadResources(repoRoot, options.baseline, baselineInventory);
	const localeFindings = validateLocales(currentResources, baselineResources, inventory);
	const currentEntries = inventoryForFiles(repoRoot, selectedFiles);
	const baselineEntries = inventoryForFiles(repoRoot, selectedFiles, options.baseline);
	const currentCallFindings = callFindings(currentEntries, currentResources.en, review);
	const baselineCallFindings = callFindings(baselineEntries, baselineResources.en, review);
	const diff = diffCallFindings(currentCallFindings, baselineCallFindings, auditedConflicts);
	const removedCallFindings = auditRemovedCalls({
		repoRoot,
		baseline: options.baseline,
		baselineEntries: baselineEntries.filter((entry) => auditedConflicts.includes(entry.file)),
		currentEntries: currentEntries.filter((entry) => auditedConflicts.includes(entry.file)),
		currentResources: currentResources.en,
		baselineResources: baselineResources.en,
		removedCalls: review.removedCalls.filter((record) => auditedConflicts.includes(record?.source?.file)),
	});
	const untranslatedFindings = validateUntranslated(review);
	const gateFindings = [
		...localeFindings,
		...diff.newFindings,
		...diff.mergeTouchedFindings,
		...removedCallFindings,
		...untranslatedFindings,
	];
	const report = {
		baseline: options.baseline,
		inventoryOnly: options.inventoryOnly,
		locales: {
			localeCount: LOCALES.length,
			namespaceCount: inventory.namespaces.length,
			fileCount: LOCALES.length * inventory.namespaces.length,
			retainedPluralKeys: countRetainedPluralKeys(currentResources, baselineResources, inventory),
			findings: localeFindings,
		},
		calls: {
			fileCount: selectedFiles.length,
			currentCallCount: currentEntries.length,
			baselineCallCount: baselineEntries.length,
			currentFindings: currentCallFindings,
			baselineFindings: baselineCallFindings,
			newFindings: diff.newFindings,
			mergeTouchedFindings: diff.mergeTouchedFindings,
			baselineExceptions: diff.baselineExceptions,
			removedCallFindings,
		},
		untranslated: review.untranslated,
		gateFindings,
		findings: [...localeFindings, ...currentCallFindings, ...removedCallFindings, ...untranslatedFindings],
	};

	if (options.report) {
		const reportPath = path.resolve(options.report);
		mkdirSync(path.dirname(reportPath), { recursive: true });
		writeFileSync(reportPath, `${JSON.stringify(report, null, 2)}\n`);
	}
	console.log(JSON.stringify(report, null, 2));
	const baselineFailed = gateFindings.some((finding) => finding.type.startsWith("baseline-"));
	return gateFindings.length > 0 && (!options.inventoryOnly || baselineFailed) ? 1 : 0;
}

const isMain = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
	try {
		process.exitCode = runCli();
	} catch (error) {
		console.error(`check-i18n: ${error.message}`);
		process.exitCode = 1;
	}
}