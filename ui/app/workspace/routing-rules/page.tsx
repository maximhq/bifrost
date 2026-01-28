/**
 * Routing Rules Page
 * Main container for routing rules management
 */

import { RoutingRulesView } from "./views/routingRulesView";

export default function RoutingRulesPage() {
	return (
		<div className="mx-auto w-full max-w-7xl">
			<RoutingRulesView />
		</div>
	);
}
