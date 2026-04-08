import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/adaptive-routing/layout";

export const Route = createFileRoute("/workspace/adaptive-routing")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
