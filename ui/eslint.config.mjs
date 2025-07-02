import { FlatCompat } from "@eslint/eslintrc";
import eslintPluginPrettier from "eslint-plugin-prettier";
import typescriptEslintEslintPlugin from "@typescript-eslint/eslint-plugin";
import eslintPluginUnusedImports from "eslint-plugin-unused-imports";
import typescriptEslintParser from "@typescript-eslint/parser";
import { fixupConfigRules } from "@eslint/compat";
import js from "@eslint/js";

const compat = new FlatCompat({
	baseDirectory: __dirname,
	recommendedConfig: js.configs.recommended,
});

export default [
	...fixupConfigRules(compat.extends("next/core-web-vitals", "next", "prettier")),
	{
		plugins: {
			prettier: eslintPluginPrettier,
			"@typescript-eslint": typescriptEslintEslintPlugin,
			"unused-imports": eslintPluginUnusedImports,
		},
	},
	{
		languageOptions: {
			parser: typescriptEslintParser,
			parserOptions: {
				ecmaVersion: "latest",
				sourceType: "module",
				tsconfigRootDir: __dirname,
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
			"linebreak-style": "error",
			"unused-imports/no-unused-imports": "error",
			"prettier/prettier": ["error"],
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
