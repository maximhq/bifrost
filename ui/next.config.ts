import type { NextConfig } from "next";
import fs from "node:fs";
import path from "node:path";

const isEnterpriseBuild = fs.existsSync(path.join(__dirname, "app", "enterprise"));

const nextConfig: NextConfig = {
	output: "export",
	trailingSlash: true,
	skipTrailingSlashRedirect: true,
	distDir: "out",
	images: {
		unoptimized: true,
	},
	basePath: "",
	generateBuildId: () => "build",
	typescript: {
		ignoreBuildErrors: false,
	},
	env: {
		NEXT_PUBLIC_IS_ENTERPRISE: isEnterpriseBuild ? "true" : "false",
	},
	// Proxy API requests to backend in development
	async rewrites() {
		return [
			{
				source: "/api/:path*",
				destination: "http://localhost:8080/api/:path*",
			},
		];
	},
	webpack: (config) => {
		config.resolve = config.resolve || {};
		config.resolve.alias = config.resolve.alias || {};
		config.resolve.alias["@enterprise"] = isEnterpriseBuild
			? path.join(__dirname, "app", "enterprise")
			: path.join(__dirname, "app", "_fallbacks", "enterprise");
		config.resolve.alias["@schemas"] = isEnterpriseBuild
			? path.join(__dirname, "app", "enterprise", "lib", "schemas")
			: path.join(__dirname, "app", "_fallbacks", "enterprise", "lib", "schemas");		
		// Ensure modules are resolved from the main project's node_modules
		// This is important when enterprise is a symlink to an external folder
		config.resolve.modules = [
			path.join(__dirname, "node_modules"),
			"node_modules",
		];		
		// Ensure symlinks are resolved correctly
		config.resolve.symlinks = true;		
		return config;
	},
};

export default nextConfig;