import { useActionParam } from "@/hooks/useActionParam";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { parseAsString, useQueryStates } from "nuqs";
import { useState } from "react";
import RouterDetailView from "./views/routerDetailView";
import { RoutersTable } from "./views/routersTable";
import RouterWizard from "./views/routerWizard";
import { useRouterTraffic, useTrafficFilter } from "./views/useRouterTraffic";

// ModelRoutersPage is the Routers section: the list of model routers, a
// router's own page when the URL names one, and the full-page wizard that
// creates one. It holds the period the traffic figures cover and reads the
// routers' traffic once, handing both to the list and to a router's page, so
// they show the same period and moving between them does not read the logs
// again.
export default function ModelRoutersPage() {
	const canCreate = useRbac(RbacResource.Routers, RbacOperation.Create);
	const [urlState, setUrlState] = useQueryStates(
		{
			router: parseAsString,
			// The router page's own tab, cleared when leaving it.
			tab: parseAsString,
		},
		{ history: "push" },
	);
	const [creating, setCreating] = useState(false);
	const trafficFilter = useTrafficFilter();
	const traffic = useRouterTraffic(trafficFilter.window);
	useActionParam("create", () => setCreating(true), { isOpen: creating, enabled: canCreate });

	if (creating) {
		return (
			<RouterWizard
				onCancel={() => setCreating(false)}
				onCreated={(id) => {
					setCreating(false);
					void setUrlState({ router: id, tab: null });
				}}
			/>
		);
	}

	// The list and a router's page sit in the full-height container the
	// routing rules and projects pages use, so their spacing and the gap left
	// below them match.
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] min-h-0 w-full flex-col overflow-hidden p-4">
			{urlState.router ? (
				<RouterDetailView
					key={urlState.router}
					routerId={urlState.router}
					traffic={traffic}
					trafficFilter={trafficFilter}
					onBack={() => void setUrlState({ router: null, tab: null })}
				/>
			) : (
				<RoutersTable
					traffic={traffic}
					trafficFilter={trafficFilter}
					onOpenRouter={(id) => void setUrlState({ router: id })}
					onCreate={() => setCreating(true)}
				/>
			)}
		</div>
	);
}