import { fixupConfigRules } from "@eslint/compat";
import eslintPluginImport from "eslint-plugin-import";
import { FlatCompat } from "@eslint/eslintrc";
import js from "@eslint/js";
import typescriptEslintEslintPlugin from "@typescript-eslint/eslint-plugin";
import typescriptEslintParser from "@typescript-eslint/parser";
import eslintPluginPrettier from "eslint-plugin-prettier";
import eslintPluginUnusedImports from "eslint-plugin-unused-imports";
import path from "path";
import { fileURLToPath } from "url";

const FILENAME = fileURLToPath(import.meta.url);
const DIRNAME = path.dirname(FILENAME);

const compat = new FlatCompat({
	baseDirectory: DIRNAME,
	recommendedConfig: js.configs.recommended,
});

const eslintConfig = [
	...fixupConfigRules(compat.extends("next/core-web-vitals", "next", "prettier")),
	{
		plugins: {
			prettier: eslintPluginPrettier,
			"@typescript-eslint": typescriptEslintEslintPlugin,
			"unused-imports": eslintPluginUnusedImports,
			import: eslintPluginImport,
		},
	},
	{
		languageOptions: {
			parser: typescriptEslintParser,
			parserOptions: {
				ecmaVersion: "latest",
				sourceType: "module",
				tsconfigRootDir: DIRNAME,
			},
		},
	},
	{
		rules: {
			"import/no-cycle": [
				"error",
				{
					maxDepth: 1,
					ignoreExternal: true,
				},
			],
			"comma-dangle": ["error", "always-multiline"],
			"@next/next/no-html-link-for-pages": ["off"],
			"@next/next/no-img-element": "off",
			"import/no-extraneous-dependencies": "off",
			"import/no-named-as-default": "off",
			"react/react-in-jsx-scope": "off",
			"unused-imports/no-unused-imports": "error",
			"prettier/prettier": [
				"error",
				{
					endOfLine: "lf",
				},
			],
			"@typescript-eslint/ban-ts-comment": ["off"],
			"@typescript-eslint/no-explicit-any": ["warn"],
			// "@typescript-eslint/no-floating-promises": "error",
			// "@typescript-eslint/no-non-null-assertion": "error",
			"@typescript-eslint/naming-convention": [
				"error",
				{
					selector: "variable",
					format: ["camelCase"],
				},
				{
					selector: "variable",
					modifiers: ["exported"],
					format: ["camelCase", "PascalCase", "UPPER_CASE"],
				},
				{
					selector: "variable",
					modifiers: ["const"],
					format: ["camelCase", "PascalCase", "UPPER_CASE"],
				},
				{
					selector: "function",
					modifiers: ["exported"],
					format: ["camelCase", "PascalCase", "UPPER_CASE"],
				},
			],
		},
	},
];

export default eslintConfig;
