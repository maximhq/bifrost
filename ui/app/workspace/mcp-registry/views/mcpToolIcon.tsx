import { MCPClientTool } from "@/lib/types/mcp";

// Icons are served by the MCP server; only https and inline image sources are shown.
function iconSource(tool: MCPClientTool): string | undefined {
	return tool.icons?.find((icon) => icon.src.startsWith("https://") || icon.src.startsWith("data:image/"))?.src;
}

export function MCPToolIconImage({ tool }: { tool: MCPClientTool }) {
	const src = iconSource(tool);
	if (!src) {
		return null;
	}
	return (
		<img
			src={src}
			alt=""
			className="mt-0.5 h-4 w-4 shrink-0 rounded-sm object-contain"
			referrerPolicy="no-referrer"
			data-testid={`mcp-tool-icon-${tool.name}`}
		/>
	);
}