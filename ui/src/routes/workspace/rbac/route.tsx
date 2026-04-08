import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/rbac/layout";

export const Route = createFileRoute("/workspace/rbac")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
