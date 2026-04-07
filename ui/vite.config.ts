import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import fs from "node:fs";
import path from "node:path";
import { defineConfig } from "vite";
import checker from "vite-plugin-checker";

const isEnterpriseBuild = fs.existsSync(path.join(__dirname, "app", "enterprise"));

export default defineConfig({
	plugins: [
		tanstackRouter({
			target: "react",
			routesDirectory: "./app",
			generatedRouteTree: "./app/routeTree.gen.ts",
			// All routes live in layout.tsx files. page.tsx files are pure view
			// components imported by their sibling layout.tsx (Next-style mental
			// model preserved for content, but routing config lives in one place).
			routeToken: "layout",
			// Treat ONLY layout.tsx / __root.tsx as routes; everything else under app/
			// (page.tsx, views, components, helpers) is ignored.
			// Directory entries have no extension and are not matched, so recursion still works.
			routeFileIgnorePattern: "^(?!layout\\.tsx$|__root\\.tsx$).+\\.(tsx|ts|jsx|js)$",
			autoCodeSplitting: true,
		}),
		react(),
		tailwindcss(),
		checker({
			typescript: true,
			// Show errors as a Vite overlay during dev AND fail `vite build` on type errors.
			enableBuild: true,
		}),
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
