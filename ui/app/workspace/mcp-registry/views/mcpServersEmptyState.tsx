"use client";

import { Button } from "@/components/ui/button";
import { Server } from "lucide-react";
import { ArrowUpRight } from "lucide-react";

const MCP_SERVERS_DOCS_URL = "https://docs.getbifrost.ai/features/mcp/overview";

interface MCPServersEmptyStateProps {
	onAddClick: () => void;
	canCreate?: boolean;
}

export function MCPServersEmptyState({ onAddClick, canCreate = true }: MCPServersEmptyStateProps) {
	return (
		<div className="flex min-h-[80vh] w-full flex-col items-center justify-center gap-4 py-16 text-center">
			<div className="text-muted-foreground">
				<Server className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
			</div>
			<div className="flex flex-col gap-1">
				<h1 className="text-muted-foreground text-xl font-medium">MCP servers connect tools and context to the gateway</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Add MCP servers to expose tools and resources to the MCP Tools endpoint. Configure connection type, auth, and which tools to enable.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="Read more about MCP servers (opens in new tab)"
						data-testid="mcp-registry-button-read-more"
						onClick={() => {
							window.open(`${MCP_SERVERS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>
						Read more <ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button
						aria-label="Add your first MCP server"
						onClick={onAddClick}
						disabled={!canCreate}
						data-testid="create-mcp-client-btn"
					>
						Add MCP Server
					</Button>
				</div>
			</div>
		</div>
	);
}
