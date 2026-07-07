import { EmptyStateView } from "@/components/emptyStateView";
import { Button } from "@/components/ui/button";
import { Link } from "@tanstack/react-router";
import { Boxes, Server } from "lucide-react";

const MCP_SERVERS_DOCS_URL = "https://docs.getbifrost.ai/features/mcp/overview";

interface MCPServersEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function MCPServersEmptyState({ onAddClick, canCreate = true }: MCPServersEmptyStateProps) {
	return (
		<EmptyStateView
			icon={Server}
			title="MCP servers connect tools and context to the gateway"
			description="Add MCP servers to expose tools and resources to the MCP Tools endpoint. Configure connection type, auth, and which tools to enable."
			readmeLink={MCP_SERVERS_DOCS_URL}
			readMoreAriaLabel="Read more about MCP servers (opens in new tab)"
			readMoreTestId="mcp-registry-button-read-more"
			actions={
				<>
					<Button aria-label="Add your first MCP server" onClick={onAddClick} disabled={!canCreate} data-testid="create-mcp-client-btn">
						Add MCP Server
					</Button>
					<Button asChild aria-label="Browse the MCP server library" data-testid="mcp-library-empty-link-btn">
						<Link to="/workspace/mcp-registry/library">
							<Boxes className="h-4 w-4" />
							Browse Library
						</Link>
					</Button>
				</>
			}
		/>
	);
}
