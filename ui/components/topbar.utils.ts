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
 * Fallback title for a route that does not name itself, derived from the last
 * non-empty path segment: "/workspace/mcp-registry" -> "MCP Registry".
 *
 * This is only ever a guess. Several routes carry a name the slug cannot
 * produce ("/workspace/model-limits" is "Budgets & Limits"), which is why a
 * page that renders <PageTitle> must render it on every branch — including its
 * loading and empty states. Falling back there does not blank the topbar, it
 * silently shows a different title.
 */
export function deriveTitleFromPathname(pathname: string): string {
	return (pathname.split("/").filter(Boolean).at(-1) ?? "Dashboard").split("-").map(formatTitlePart).join(" ");
}