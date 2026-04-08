import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/mcp-settings/layout";

export const Route = createFileRoute("/workspace/mcp-settings")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
