import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/config/layout";

export const Route = createFileRoute("/workspace/config")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
