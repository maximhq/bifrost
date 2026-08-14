/**
 * Routing Tree Page
 * Full-canvas read-only routing rules decision tree visualizer.
 */

import { RoutingTreeView } from "./views/routingTreeView";

export const metadata = {
	title: "路由树 | Bifrost",
	description: "路由规则的只读决策树可视化",
};

export default function RoutingTreePage() {
	return (
		<div className="no-padding-parent no-border-parent h-[calc(100dvh_)] w-full">
			<RoutingTreeView />
		</div>
	);
}