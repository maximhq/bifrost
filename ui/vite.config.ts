import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";
import fs from "node:fs";

const isEnterpriseBuild = fs.existsSync(path.join(__dirname, "app", "enterprise"));

export default defineConfig({
	plugins: [
		tanstackRouter({
			target: "react",
			routesDirectory: "./src/routes",
			generatedRouteTree: "./src/routeTree.gen.ts",
			routeFileIgnorePrefix: "-",
			autoCodeSplitting: true,
		}),
		react(),
		tailwindcss(),
	],
	resolve: {
		alias: {
			"@": path.resolve(__dirname),
			"@enterprise": isEnterpriseBuild
				? path.resolve(__dirname, "app", "enterprise")
				: path.resolve(__dirname, "app", "_fallbacks", "enterprise"),
			"@schemas": isEnterpriseBuild
				? path.resolve(__dirname, "app", "enterprise", "lib", "schemas")
				: path.resolve(__dirname, "app", "_fallbacks", "enterprise", "lib", "schemas"),
		},
	},
	define: {
		// Shim Next.js public env vars so existing call sites keep working
		// without a sweeping rename to import.meta.env. NODE_ENV is set by
		// Vite (mode), but the literal `process.env.NODE_ENV` reference is
		// not statically replaced unless we declare it here.
		"process.env.NODE_ENV": JSON.stringify(process.env.NODE_ENV ?? "production"),
		"process.env.NEXT_PUBLIC_IS_ENTERPRISE": JSON.stringify(isEnterpriseBuild ? "true" : "false"),
		"process.env.NEXT_PUBLIC_DISABLE_PROFILER": JSON.stringify(process.env.NEXT_PUBLIC_DISABLE_PROFILER ?? ""),
	},
	server: {
		port: 3000,
		proxy: {
			"/api": {
				target: "http://localhost:8080",
				changeOrigin: true,
			},
		},
	},
	build: {
		outDir: "out",
		emptyOutDir: true,
	},
});
