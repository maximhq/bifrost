import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import fs from "fs";
import path from "path";

const isEnterpriseBuild = fs.existsSync(path.resolve(__dirname, "app", "enterprise"));

export default defineConfig({
	plugins: [react()],
	resolve: {
		alias: {
			"@": path.resolve(__dirname, "."),
			"@enterprise": path.resolve(__dirname, "app", isEnterpriseBuild ? "enterprise" : "_fallbacks/enterprise"),
		},
	},
	test: {
		globals: true,
	},
});
