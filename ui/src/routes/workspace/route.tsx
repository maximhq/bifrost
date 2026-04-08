import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/layout";

export const Route = createFileRoute("/workspace")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
