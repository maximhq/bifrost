import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/cluster/layout";

export const Route = createFileRoute("/workspace/cluster")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
