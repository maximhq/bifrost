/**
 * Gap between a topbar trigger and the menu it opens, in pixels. One tighter
 * than the app-wide default of 4 that dropdownMenu/popover/hoverCard ship.
 *
 * Shared rather than repeated because Radix measures the offset from the
 * trigger's bounding box: the topbar's triggers are all size-8 so that one
 * offset lands every menu on the same line, and a lone override would visibly
 * break the row.
 */
export const TOPBAR_MENU_SIDE_OFFSET = 3;

/** Path segments that are shouted rather than capitalised. */
const titleAcronyms: Record<string, string> = {
	ai: "AI",
	api: "API",
	llm: "LLM",
	mcp: "MCP",
	oauth: "OAuth",
	rbac: "RBAC",
	scim: "SCIM",
	sdk: "SDK",
	sso: "SSO",
};

function formatTitlePart(part: string) {
	return titleAcronyms[part.toLowerCase()] ?? part.charAt(0).toUpperCase() + part.slice(1);
}

/**
 * Routes whose slug does not produce the name the product actually uses.
 *
 * Only routes that have not yet moved to <PageTitle> belong here; a page that
 * renders <PageTitle> always wins over this table, so migrating a page means
 * deleting its row. Titles are taken from the sidebar label so the nav and the
 * topbar agree, with one adjustment: where a sub-item's label is only
 * meaningful under its parent ("Rules", "Approvals", "Connectors", "Settings"),
 * the parent is folded in, because the topbar shows the title alone.
 */
const routeTitleOverrides: Record<string, string> = {
	"/workspace/adaptive-routing/settings": "Adaptive Routing Settings",
	"/workspace/alerting/channels": "Alert Channels",
	"/workspace/alerting/history": "Alert History",
	"/workspace/alerting/rules": "Alert Rules",
	"/workspace/cluster": "Cluster Config",
	"/workspace/config": "Settings",
	"/workspace/config/license": "License Info",
	"/workspace/edge-control/config": "Edge Settings",
	"/workspace/edge-control/devices": "Edge Devices",
	"/workspace/edge-control/inventory": "Edge Approvals",
	"/workspace/governance/rbac": "Roles & Permissions",
	"/workspace/guardrails/configuration": "Guardrail Rules",
	"/workspace/guardrails/providers": "Guardrail Providers",
	"/workspace/logs": "LLM Logs",
	"/workspace/logs/connectors": "Log Connectors",
	"/workspace/observability": "Observability Connectors",
	"/workspace/prompt-repo": "Prompt Repository",
	"/workspace/providers": "Model Providers",
	"/workspace/routing-rules/tree": "Routing Tree",
	"/workspace/scim": "User Provisioning",
};

/**
 * Title for a route that does not name itself: an entry from
 * routeTitleOverrides, else the last non-empty path segment
 * ("/workspace/mcp-registry" -> "MCP Registry").
 *
 * The slug half is only ever a guess, which is why a page that renders
 * <PageTitle> must render it on every branch, including its loading and empty
 * states. Falling back there does not blank the topbar, it silently shows a
 * different title.
 */
export function deriveTitleFromPathname(pathname: string): string {
	// Trailing slashes and query-free paths both have to hit the same row.
	const segments = pathname.split("?")[0].split("/").filter(Boolean);
	const override = routeTitleOverrides[`/${segments.join("/")}`];
	if (override) return override;
	return (segments.at(-1) ?? "Dashboard").split("-").map(formatTitlePart).join(" ");
}