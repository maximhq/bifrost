import { MCPClientConfigEntry, MCPClientConfigsEditor } from "@/components/mcp/mcpClientConfigsEditor";
import { VirtualMCPToolSpec } from "@/lib/types/virtualMcps";

const TOOL_WILDCARD = "*";

const TOOLS_TOOLTIP = (
	<p>
		Pick which of your MCP servers this Virtual MCP exposes and, for each, which tools. After adding a server, select specific tools or
		choose <span className="font-medium">Allow All Tools</span> to expose all of them.
	</p>
);

interface VirtualMcpToolsEditorProps {
	value: VirtualMCPToolSpec[];
	onChange: (specs: VirtualMCPToolSpec[]) => void;
}

// Adapts id-native Virtual MCP specs to the shared editor, which resolves names and tools.
// A deleted server keeps its id (name falls back to it), so edits don't drop it.
export default function VirtualMcpToolsEditor({ value, onChange }: VirtualMcpToolsEditorProps) {
	const editorValue: MCPClientConfigEntry[] = value.map((spec) => ({
		mcp_client_id: spec.mcp_client_id,
		mcp_client_name: spec.mcp_client_id,
		tools_to_execute: spec.tool_names,
	}));

	const handleChange = (entries: MCPClientConfigEntry[]) => {
		onChange(
			entries.map((entry) => ({
				mcp_client_id: entry.mcp_client_id ?? entry.mcp_client_name,
				tool_names: entry.tools_to_execute ?? [TOOL_WILDCARD],
			})),
		);
	};

	return (
		<MCPClientConfigsEditor
			value={editorValue}
			onChange={handleChange}
			label="Tools"
			tooltip={TOOLS_TOOLTIP}
			allClientTools
			emptyState={
				<div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">
					No tools attached yet. Add an MCP server above to expose its tools through this Virtual MCP.
				</div>
			}
		/>
	);
}