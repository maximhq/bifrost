import type { NextConfig } from "next";
import fs from "node:fs";
import path from "node:path";

const haveEnterprise = fs.existsSync(path.join(__dirname, "app", "enterprise"));

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
		NEXT_PUBLIC_IS_ENTERPRISE: haveEnterprise ? "true" : "false",
	},
	eslint: {
		ignoreDuringBuilds: false,
	},
	webpack: (config) => {
		config.resolve = config.resolve || {};
		config.resolve.alias = config.resolve.alias || {};
		config.resolve.alias["@enterprise"] = haveEnterprise
			? path.join(__dirname, "app", "enterprise")
			: path.join(__dirname, "app", "_fallbacks", "enterprise");
		config.resolve.alias["@schemas"] = haveEnterprise
			? path.join(__dirname, "app", "enterprise", "lib", "schemas")
			: path.join(__dirname, "app", "_fallbacks", "enterprise", "lib");		
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
