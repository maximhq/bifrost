import { Button } from "@/components/ui/button";
import { Link } from "@tanstack/react-router";
import { ArrowUpRight, Boxes, Server } from "lucide-react";

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
				<h1 className="text-muted-foreground text-xl font-medium">MCP 服务器将工具和上下文连接到网关</h1>
				<div className="text-muted-foreground mx-auto mt-2 max-w-[600px] text-sm font-normal">
					Add MCP servers to expose tools and resources to the MCP Tools endpoint. Configure connection type, auth, and which tools to
					enable.
				</div>
				<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
					<Button
						variant="outline"
						aria-label="了解更多 MCP 服务器（在新标签页打开）"
						data-testid="mcp-registry-button-read-more"
						onClick={() => {
							window.open(`${MCP_SERVERS_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
						}}
					>阅读更多<ArrowUpRight className="text-muted-foreground h-3 w-3" />
					</Button>
					<Button aria-label="添加您的第一个 MCP 服务器" onClick={onAddClick} disabled={!canCreate} data-testid="create-mcp-client-btn">添加 MCP 服务器</Button>
					<Button asChild aria-label="浏览 MCP 服务器库" data-testid="mcp-library-empty-link-btn">
						<Link to="/workspace/mcp-registry/library">
							<Boxes className="h-4 w-4" />浏览库</Link>
					</Button>
				</div>
			</div>
		</div>
	);
}