/**
 * Routing Rules Page
 * Main container for routing rules management
 */

import { RoutingRulesView } from "./views/routingRulesView";

export default function RoutingRulesPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] min-h-0 w-full flex-col overflow-hidden p-4">
			<RoutingRulesView />
		</div>
	);
}