import commonEn from "@/locales/en/common.json";
import configEn from "@/locales/en/config.json";
import governanceEn from "@/locales/en/governance.json";
import loginEn from "@/locales/en/login.json";
import mcpEn from "@/locales/en/mcp.json";
import modelsEn from "@/locales/en/models.json";
import observabilityEn from "@/locales/en/observability.json";
import shellEn from "@/locales/en/shell.json";
import commonZhCN from "@/locales/zh-CN/common.json";
import configZhCN from "@/locales/zh-CN/config.json";
import governanceZhCN from "@/locales/zh-CN/governance.json";
import loginZhCN from "@/locales/zh-CN/login.json";
import mcpZhCN from "@/locales/zh-CN/mcp.json";
import modelsZhCN from "@/locales/zh-CN/models.json";
import observabilityZhCN from "@/locales/zh-CN/observability.json";
import shellZhCN from "@/locales/zh-CN/shell.json";

/**
 * i18n namespaces currently wired for Bifrost UI.
 *
 * Active (with copy): common, shell, login, config
 * Reserved for other workers (empty JSON stubs — fill keys only):
 * observability, models, mcp, governance
 */
export const NAMESPACES = ["common", "shell", "login", "observability", "models", "mcp", "governance", "config"] as const;

export type I18nNamespace = (typeof NAMESPACES)[number];

export const resources = {
	en: {
		common: commonEn,
		shell: shellEn,
		login: loginEn,
		observability: observabilityEn,
		models: modelsEn,
		mcp: mcpEn,
		governance: governanceEn,
		config: configEn,
	},
	"zh-CN": {
		common: commonZhCN,
		shell: shellZhCN,
		login: loginZhCN,
		observability: observabilityZhCN,
		models: modelsZhCN,
		mcp: mcpZhCN,
		governance: governanceZhCN,
		config: configZhCN,
	},
} as const;
