import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/logs/dashboard/layout";

export const Route = createFileRoute("/workspace/logs/dashboard")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
