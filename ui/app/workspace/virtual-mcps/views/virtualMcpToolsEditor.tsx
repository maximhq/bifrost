import { MCPClientConfigEntry, MCPClientConfigsEditor } from "@/components/mcp/mcpClientConfigsEditor";
import { VirtualMCPToolSpec } from "@/lib/types/virtualMcps";
import { Trans, useTranslation } from "react-i18next";

const TOOL_WILDCARD = "*";

interface VirtualMcpToolsEditorProps {
	value: VirtualMCPToolSpec[];
	onChange: (specs: VirtualMCPToolSpec[]) => void;
}

// Adapts id-native Virtual MCP specs to the shared editor, which resolves names and tools.
// A deleted server keeps its id (name falls back to it), so edits don't drop it.
export default function VirtualMcpToolsEditor({ value, onChange }: VirtualMcpToolsEditorProps) {
	const { t } = useTranslation("mcp");
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
			label={t("virtualMcps.wizard.tools")}
			tooltip={
				<p>
					<Trans t={t} i18nKey="virtualMcps.tools.tooltip" components={{ strong: <span className="font-medium" /> }} />
				</p>
			}
			allClientTools
			emptyState={
				<div className="text-muted-foreground rounded-md border border-dashed p-6 text-center text-sm">{t("virtualMcps.tools.empty")}</div>
			}
		/>
	);
}
