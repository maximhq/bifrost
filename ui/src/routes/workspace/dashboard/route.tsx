import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/dashboard/layout";

export const Route = createFileRoute("/workspace/dashboard")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
