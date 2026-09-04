import { createFileRoute, redirect } from "@tanstack/react-router";

// MCP Tool Groups is superseded by Virtual MCPs. Redirect the old path so
// existing links and bookmarks land on the new page.
export const Route = createFileRoute("/workspace/mcp-tool-groups")({
	beforeLoad: () => {
		throw redirect({ to: "/workspace/virtual-mcps" });
	},
});
