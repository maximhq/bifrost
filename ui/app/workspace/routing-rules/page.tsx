/**
 * Routing Rules Page
 * Main container for routing rules management
 */

import { WorkspacePageShell } from "@/components/workspacePageShell";
import { RoutingRulesView } from "./views/routingRulesView";

export default function RoutingRulesPage() {
	return (
		<WorkspacePageShell className="overflow-hidden">
			<RoutingRulesView />
		</WorkspacePageShell>
	);
}
