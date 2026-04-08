import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/logs/layout";

export const Route = createFileRoute("/workspace/logs")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
