import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/mcp-registry/layout";

export const Route = createFileRoute("/workspace/mcp-registry")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
