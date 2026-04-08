import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/observability/layout";

export const Route = createFileRoute("/workspace/observability")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
