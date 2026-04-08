import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/routing-rules/layout";

export const Route = createFileRoute("/workspace/routing-rules")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
