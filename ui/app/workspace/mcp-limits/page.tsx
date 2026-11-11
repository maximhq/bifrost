import ModelLimitsView from "@/app/workspace/model-limits/views/modelLimitsView";

// The Models page over the same holder-level rows, showing the lines MCP tool executions are charged by.
export default function MCPLimitsPage() {
	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] min-h-0 w-full flex-col overflow-hidden p-4">
			<ModelLimitsView traffic="mcp" />
		</div>
	);
}