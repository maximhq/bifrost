import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/workspace/config/large-payload")({
	beforeLoad: () => {
		throw redirect({ to: "/workspace/config/client-settings" });
	},
});