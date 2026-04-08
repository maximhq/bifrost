import Layout from "@/app/login/layout";
import { Outlet, createFileRoute } from "@tanstack/react-router";

export const Route = createFileRoute("/login")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
