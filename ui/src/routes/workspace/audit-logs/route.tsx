import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/audit-logs/layout";

export const Route = createFileRoute("/workspace/audit-logs")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
