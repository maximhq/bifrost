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
		return config;
	},
};

export default nextConfig;
