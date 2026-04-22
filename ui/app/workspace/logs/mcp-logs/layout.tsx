import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/workspace/logs/mcp-logs")({
	beforeLoad: () => {
		throw redirect({ to: "/workspace/mcp-logs", replace: true });
	},
});