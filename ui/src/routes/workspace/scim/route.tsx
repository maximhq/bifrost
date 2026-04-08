import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/scim/layout";

export const Route = createFileRoute("/workspace/scim")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
