import { ENTERPRISE_NAMESPACES, enterpriseResources } from "@enterprise/lib/i18n/resources";
import commonEn from "@/locales/en/common.json";
import shellEn from "@/locales/en/shell.json";
import loginEn from "@/locales/en/login.json";
import observabilityEn from "@/locales/en/observability.json";
import modelsEn from "@/locales/en/models.json";
import mcpEn from "@/locales/en/mcp.json";
import governanceEn from "@/locales/en/governance.json";
import configEn from "@/locales/en/config.json";

export const NAMESPACES = [
	"common",
	"shell",
	"login",
	"observability",
	"models",
	"mcp",
	"governance",
	"config",
	...ENTERPRISE_NAMESPACES,
] as const;

export type I18nNamespace = (typeof NAMESPACES)[number];

// English ships in the main bundle: it is the fallback and the source of the typed keys.
export const resources = {
	en: {
		...enterpriseResources.en,
		common: commonEn,
		shell: shellEn,
		login: loginEn,
		observability: observabilityEn,
		models: modelsEn,
		mcp: mcpEn,
		governance: governanceEn,
		config: configEn,
	},
} as const;

type Catalog = Record<string, unknown>;

// Every other language is a separate chunk per namespace, fetched on first use.
const catalogLoaders = import.meta.glob<Catalog>(["/locales/*/*.json", "@enterprise/locales/*/*.json", "!**/en/*.json"], {
	import: "default",
});

const loaderByLocale = new Map<string, () => Promise<Catalog>>();
for (const [file, load] of Object.entries(catalogLoaders)) {
	const match = /\/([^/]+)\/([^/]+)\.json$/.exec(file);
	if (match) loaderByLocale.set(`${match[1]}/${match[2]}`, load);
}

export async function loadCatalog(language: string, namespace: string): Promise<Catalog> {
	const load = loaderByLocale.get(`${language}/${namespace}`);
	if (!load) throw new Error(`no ${namespace} catalog for ${language}`);
	return load();
}