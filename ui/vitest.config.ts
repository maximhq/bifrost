import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vitest/config";

const rootDir = fileURLToPath(new URL(".", import.meta.url));

export default defineConfig({
	esbuild: {
		jsx: "automatic",
	},
	test: {
		environment: "jsdom",
		globals: true,
		setupFiles: ["./tests/setup.ts"],
	},
	resolve: {
		alias: {
			"@": rootDir,
			"@enterprise": `${rootDir}app/_fallbacks/enterprise`,
			"@schemas": `${rootDir}app/_fallbacks/enterprise/lib/schemas`,
		},
	},
});
