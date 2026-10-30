// Kept free of store and component imports so pure helpers and their tests can use it.

// One entry in the workspace navigation, shared by the sidebar and the command palette.
export interface NavItem {
	id: string;
	title: string;
	url: string;
	icon: React.ComponentType<{ className?: string }>;
	description?: string;
	isAllowed?: boolean;
	hasAccess: boolean;
	subItems?: NavItem[];
	tag?: string;
	new?: boolean;
	isExternal?: boolean;
	queryParam?: string; // Optional: for tab-based subitems (e.g., "client-settings")
	testId?: string; // Optional: pin the data-testid slug across a rename (else derived from title)
	enterpriseOnly?: boolean; // OSS renders an enterprise upsell here; the command palette hides it outside enterprise
}

export const getNavItemHref = (item: Pick<NavItem, "url" | "queryParam">) => {
	return item.queryParam ? `${item.url}?tab=${item.queryParam}` : item.url;
};

// Carries the selected time range across the pages that share one, for the sidebar and the command palette alike.
const TimeFilterPages = new Set([
	"/workspace/dashboard",
	"/workspace/logs",
	"/workspace/mcp-logs",
	"/workspace/agent-logs",
	"/workspace/relay-logs",
]);

export const preserveTimeFilters = (baseHref: string, subItemUrl: string, pathname: string, search: string): string => {
	if (TimeFilterPages.has(subItemUrl) && TimeFilterPages.has(pathname)) {
		const currentParams = new URLSearchParams(search);
		const startTime = currentParams.get("start_time");
		const endTime = currentParams.get("end_time");
		const period = currentParams.get("period");
		if ((startTime && endTime) || period) {
			const params = new URLSearchParams();
			if (startTime) params.set("start_time", startTime);
			if (endTime) params.set("end_time", endTime);
			if (period) params.set("period", period);
			const sep = baseHref.includes("?") ? "&" : "?";
			return `${baseHref}${sep}${params.toString()}`;
		}
	}
	return baseHref;
};