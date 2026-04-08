import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/pprof/layout";

export const Route = createFileRoute("/pprof")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
