import { useQueryState } from "nuqs";
import { useEffect, useRef } from "react";

export const ACTION_PARAM = "action";

// Links straight into a page's flow, e.g. /workspace/governance/virtual-keys?action=create.
export const withActionParam = (href: string, action: string) =>
	`${href}${href.includes("?") ? "&" : "?"}${ACTION_PARAM}=${encodeURIComponent(action)}`;

// For the page already open: keeps the view's own params (search, paging) so closing the flow returns to it.
export const withActionParamOnPage = (href: string, currentSearch: string, action: string) => {
	const [path, hrefQuery = ""] = href.split("?");
	const params = new URLSearchParams(hrefQuery);
	new URLSearchParams(currentSearch).forEach((value, key) => params.set(key, value));
	params.delete(ACTION_PARAM);
	const query = params.toString();
	return withActionParam(query ? `${path}?${query}` : path, action);
};

interface ActionParamOptions {
	isOpen: boolean; // whether the flow `run` opens is showing, so closing it can drop the param
	enabled?: boolean;
}

/**
 * Runs `run` when the URL arrives with ?action=<action>. The param stays while the flow is open, so a
 * reload reopens it, and is dropped when the flow closes. Without permission it is dropped unrun.
 */
export function useActionParam(action: string, run: () => void, { isOpen, enabled = true }: ActionParamOptions) {
	const [param, setParam] = useQueryState(ACTION_PARAM);
	const runRef = useRef(run);
	runRef.current = run;
	const wasOpen = useRef(isOpen);

	// Keyed on the param alone: other params changing (opening a row, paging) must not rerun it.
	useEffect(() => {
		if (param !== action) return;
		if (enabled) runRef.current();
		else void setParam(null);
	}, [param, action, enabled, setParam]);

	useEffect(() => {
		if (wasOpen.current && !isOpen && param === action) void setParam(null);
		wasOpen.current = isOpen;
	}, [isOpen, param, action, setParam]);
}