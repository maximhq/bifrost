import { createFileRoute, Outlet, redirect } from "@tanstack/react-router";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { ClientLayout } from "../clientLayout";

function WorkspaceLayout({ children }: { children: React.ReactNode }) {
	return <ClientLayout>{children}</ClientLayout>;
}

function RouteComponent() {
	return (
		<WorkspaceLayout>
			<Outlet />
		</WorkspaceLayout>
	);
}

export const Route = createFileRoute("/workspace")({
	beforeLoad: ({ location }) => {
		if (location.pathname === "/workspace" || location.pathname === "/workspace/") {
			// Enterprise lands on the personal Home page; OSS keeps the dashboard.
			throw redirect({ to: IS_ENTERPRISE ? "/workspace/home" : "/workspace/dashboard", replace: true });
		}
	},
	component: RouteComponent,
});