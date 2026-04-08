import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/pii-redactor/layout";

export const Route = createFileRoute("/workspace/pii-redactor")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
