import { Outlet, createFileRoute } from "@tanstack/react-router";
import Layout from "@/app/workspace/guardrails/layout";

export const Route = createFileRoute("/workspace/guardrails")({
	component: LayoutRoute,
});

function LayoutRoute() {
	return (
		<Layout>
			<Outlet />
		</Layout>
	);
}
