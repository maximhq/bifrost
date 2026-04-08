import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/governance/layout";

export const Route = createFileRoute("/workspace/governance")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
