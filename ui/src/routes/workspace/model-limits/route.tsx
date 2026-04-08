import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/model-limits/layout";

export const Route = createFileRoute("/workspace/model-limits")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
