import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/virtual-keys/layout";

export const Route = createFileRoute("/workspace/virtual-keys")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
