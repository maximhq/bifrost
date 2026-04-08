import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/custom-pricing/layout";

export const Route = createFileRoute("/workspace/custom-pricing")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
