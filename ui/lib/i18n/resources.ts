import commonEn from "@/locales/en/common.json";
import shellEn from "@/locales/en/shell.json";
import loginEn from "@/locales/en/login.json";
import observabilityEn from "@/locales/en/observability.json";
import modelsEn from "@/locales/en/models.json";
import mcpEn from "@/locales/en/mcp.json";
import governanceEn from "@/locales/en/governance.json";
import configEn from "@/locales/en/config.json";
import commonZhCN from "@/locales/zh-CN/common.json";
import shellZhCN from "@/locales/zh-CN/shell.json";
import loginZhCN from "@/locales/zh-CN/login.json";
import observabilityZhCN from "@/locales/zh-CN/observability.json";
import modelsZhCN from "@/locales/zh-CN/models.json";
import mcpZhCN from "@/locales/zh-CN/mcp.json";
import governanceZhCN from "@/locales/zh-CN/governance.json";
import configZhCN from "@/locales/zh-CN/config.json";
import commonZhTW from "@/locales/zh-TW/common.json";
import shellZhTW from "@/locales/zh-TW/shell.json";
import loginZhTW from "@/locales/zh-TW/login.json";
import observabilityZhTW from "@/locales/zh-TW/observability.json";
import modelsZhTW from "@/locales/zh-TW/models.json";
import mcpZhTW from "@/locales/zh-TW/mcp.json";
import governanceZhTW from "@/locales/zh-TW/governance.json";
import configZhTW from "@/locales/zh-TW/config.json";
import commonJa from "@/locales/ja/common.json";
import shellJa from "@/locales/ja/shell.json";
import loginJa from "@/locales/ja/login.json";
import observabilityJa from "@/locales/ja/observability.json";
import modelsJa from "@/locales/ja/models.json";
import mcpJa from "@/locales/ja/mcp.json";
import governanceJa from "@/locales/ja/governance.json";
import configJa from "@/locales/ja/config.json";
import commonKo from "@/locales/ko/common.json";
import shellKo from "@/locales/ko/shell.json";
import loginKo from "@/locales/ko/login.json";
import observabilityKo from "@/locales/ko/observability.json";
import modelsKo from "@/locales/ko/models.json";
import mcpKo from "@/locales/ko/mcp.json";
import governanceKo from "@/locales/ko/governance.json";
import configKo from "@/locales/ko/config.json";
import commonEs from "@/locales/es/common.json";
import shellEs from "@/locales/es/shell.json";
import loginEs from "@/locales/es/login.json";
import observabilityEs from "@/locales/es/observability.json";
import modelsEs from "@/locales/es/models.json";
import mcpEs from "@/locales/es/mcp.json";
import governanceEs from "@/locales/es/governance.json";
import configEs from "@/locales/es/config.json";
import commonPt from "@/locales/pt/common.json";
import shellPt from "@/locales/pt/shell.json";
import loginPt from "@/locales/pt/login.json";
import observabilityPt from "@/locales/pt/observability.json";
import modelsPt from "@/locales/pt/models.json";
import mcpPt from "@/locales/pt/mcp.json";
import governancePt from "@/locales/pt/governance.json";
import configPt from "@/locales/pt/config.json";
import commonFr from "@/locales/fr/common.json";
import shellFr from "@/locales/fr/shell.json";
import loginFr from "@/locales/fr/login.json";
import observabilityFr from "@/locales/fr/observability.json";
import modelsFr from "@/locales/fr/models.json";
import mcpFr from "@/locales/fr/mcp.json";
import governanceFr from "@/locales/fr/governance.json";
import configFr from "@/locales/fr/config.json";
import commonDe from "@/locales/de/common.json";
import shellDe from "@/locales/de/shell.json";
import loginDe from "@/locales/de/login.json";
import observabilityDe from "@/locales/de/observability.json";
import modelsDe from "@/locales/de/models.json";
import mcpDe from "@/locales/de/mcp.json";
import governanceDe from "@/locales/de/governance.json";
import configDe from "@/locales/de/config.json";
import commonIt from "@/locales/it/common.json";
import shellIt from "@/locales/it/shell.json";
import loginIt from "@/locales/it/login.json";
import observabilityIt from "@/locales/it/observability.json";
import modelsIt from "@/locales/it/models.json";
import mcpIt from "@/locales/it/mcp.json";
import governanceIt from "@/locales/it/governance.json";
import configIt from "@/locales/it/config.json";
import commonRu from "@/locales/ru/common.json";
import shellRu from "@/locales/ru/shell.json";
import loginRu from "@/locales/ru/login.json";
import observabilityRu from "@/locales/ru/observability.json";
import modelsRu from "@/locales/ru/models.json";
import mcpRu from "@/locales/ru/mcp.json";
import governanceRu from "@/locales/ru/governance.json";
import configRu from "@/locales/ru/config.json";

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
	"zh-TW": {
		common: commonZhTW,
		shell: shellZhTW,
		login: loginZhTW,
		observability: observabilityZhTW,
		models: modelsZhTW,
		mcp: mcpZhTW,
		governance: governanceZhTW,
		config: configZhTW,
	},
	ja: {
		common: commonJa,
		shell: shellJa,
		login: loginJa,
		observability: observabilityJa,
		models: modelsJa,
		mcp: mcpJa,
		governance: governanceJa,
		config: configJa,
	},
	ko: {
		common: commonKo,
		shell: shellKo,
		login: loginKo,
		observability: observabilityKo,
		models: modelsKo,
		mcp: mcpKo,
		governance: governanceKo,
		config: configKo,
	},
	es: {
		common: commonEs,
		shell: shellEs,
		login: loginEs,
		observability: observabilityEs,
		models: modelsEs,
		mcp: mcpEs,
		governance: governanceEs,
		config: configEs,
	},
	pt: {
		common: commonPt,
		shell: shellPt,
		login: loginPt,
		observability: observabilityPt,
		models: modelsPt,
		mcp: mcpPt,
		governance: governancePt,
		config: configPt,
	},
	fr: {
		common: commonFr,
		shell: shellFr,
		login: loginFr,
		observability: observabilityFr,
		models: modelsFr,
		mcp: mcpFr,
		governance: governanceFr,
		config: configFr,
	},
	de: {
		common: commonDe,
		shell: shellDe,
		login: loginDe,
		observability: observabilityDe,
		models: modelsDe,
		mcp: mcpDe,
		governance: governanceDe,
		config: configDe,
	},
	it: {
		common: commonIt,
		shell: shellIt,
		login: loginIt,
		observability: observabilityIt,
		models: modelsIt,
		mcp: mcpIt,
		governance: governanceIt,
		config: configIt,
	},
	ru: {
		common: commonRu,
		shell: shellRu,
		login: loginRu,
		observability: observabilityRu,
		models: modelsRu,
		mcp: mcpRu,
		governance: governanceRu,
		config: configRu,
	},
} as const;