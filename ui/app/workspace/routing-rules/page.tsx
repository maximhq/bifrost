/**
 * Routing Rules Page
 * Main container for routing rules management
 */

import { RoutingRulesView } from "./views/routingRulesView";

export default function RoutingRulesPage() {
	return (
		<div className="h-full w-full">
			<div className="mx-auto max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
				<RoutingRulesView />
			</div>
		</div>
	);
}
