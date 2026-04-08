import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/plugins/layout";

export const Route = createFileRoute("/workspace/plugins")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
