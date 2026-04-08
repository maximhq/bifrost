import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/providers/layout";

export const Route = createFileRoute("/workspace/providers")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
