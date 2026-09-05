import type { MessageKey, TranslateFn } from "./translate";
import type { TopbarTitleValue } from "@/lib/contexts/topbarContext.utils";

/**
 * Maps the English titles still passed to <PageTitle> / deriveTitleFromPathname
 * onto message keys so the topbar can localize them without rewriting every page.
 */
const PAGE_TITLE_KEYS: Record<string, MessageKey> = {
	"Access Profiles": "nav.accessProfiles",
	"Adaptive Routing": "nav.adaptiveRouting",
	"Adaptive Routing Settings": "titles.adaptiveRoutingSettings",
	"Alert Channels": "titles.alertChannels",
	"Alert History": "titles.alertHistory",
	"Alert Rules": "titles.alertRules",
	Alerting: "nav.alerting",
	Approvals: "nav.approvals",
	"API Keys": "nav.apiKeys",
	"Audit Logs": "nav.auditLogs",
	Branding: "nav.branding",
	"Budgets & Limits": "nav.budgetsLimits",
	"Business Units": "nav.businessUnits",
	Caching: "nav.caching",
	Channels: "nav.channels",
	"Circuit Breaker": "nav.circuitBreaker",
	"Client Settings": "nav.clientSettings",
	"Cluster Config": "nav.clusterConfig",
	Compatibility: "nav.compatibility",
	"Complexity Router": "nav.complexityRouter",
	Connectors: "nav.connectors",
	Customers: "nav.customers",
	Dashboard: "nav.dashboard",
	Deliveries: "titles.deliveries",
	Devices: "nav.devices",
	"Edge Approvals": "titles.edgeApprovals",
	"Edge Control": "nav.edgeControl",
	"Edge Devices": "titles.edgeDevices",
	"Edge Settings": "nav.edgeSettings",
	"Feature Flags": "nav.featureFlags",
	Governance: "nav.governance",
	"Guardrail Providers": "titles.guardrailProviders",
	"Guardrail Rules": "titles.guardrailRules",
	Guardrails: "nav.guardrails",
	History: "nav.history",
	"License Info": "nav.licenseInfo",
	"LLM Logs": "nav.llmLogs",
	"Local Cache": "titles.localCache",
	"Log Connectors": "titles.logConnectors",
	"Logs Settings": "nav.logsSettings",
	"MCP Auth Sessions": "titles.mcpAuthSessions",
	"MCP Catalog": "nav.mcpCatalog",
	"MCP Gateway": "nav.mcpGateway",
	"MCP Library": "nav.mcpLibrary",
	"MCP Logs": "nav.mcpLogs",
	"MCP Registry": "nav.mcpCatalog",
	"MCP Server Catalog": "titles.mcpServerCatalog",
	"MCP Server Library": "titles.mcpServerLibrary",
	"MCP Settings": "nav.mcpSettings",
	"MCP Tool Groups": "titles.mcpToolGroups",
	"Model Catalog": "nav.modelCatalog",
	"Model Limits": "nav.budgetsLimits",
	"Model Providers": "nav.modelProviders",
	"Model Settings": "nav.modelSettings",
	Models: "nav.models",
	"OAuth Grants": "nav.oauthGrants",
	Observability: "nav.observability",
	"Observability Connectors": "titles.observabilityConnectors",
	"Performance Tuning": "nav.performanceTuning",
	Plugins: "nav.plugins",
	"Pricing Configuration": "titles.pricingConfiguration",
	"Pricing Overrides": "nav.pricingOverrides",
	Projects: "nav.projects",
	"Prompt Repository": "nav.promptRepository",
	Providers: "nav.guardrailProviders",
	Proxy: "nav.proxy",
	"Proxy Settings": "titles.proxySettings",
	"Roles & Permissions": "nav.rolesPermissions",
	"Routing Rules": "nav.routingRules",
	"Routing Tree": "titles.routingTree",
	Rules: "nav.rules",
	"Security Settings": "titles.securitySettings",
	Security: "nav.security",
	Settings: "nav.settings",
	"Skills Repository": "nav.skillsRepository",
	Teams: "nav.teams",
	"Tool Groups": "nav.toolGroups",
	"Virtual MCPs": "nav.virtualMcps",
	"User Provisioning": "nav.userProvisioning",
	Users: "nav.users",
	"Virtual Keys": "nav.virtualKeys",
	Webhooks: "nav.webhooks",
};

export function translatePageTitle(title: string, t: TranslateFn): string {
	const key = PAGE_TITLE_KEYS[title];
	return key ? t(key) : title;
}

export function translateTopbarTitle(title: TopbarTitleValue, t: TranslateFn): TopbarTitleValue {
	if (!Array.isArray(title)) {
		return translatePageTitle(title, t);
	}
	return title.map((crumb) => ({
		...crumb,
		label: translatePageTitle(crumb.label, t),
	}));
}