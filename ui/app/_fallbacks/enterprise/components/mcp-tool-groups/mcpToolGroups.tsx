import { ToolCase } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function MCPToolGroups() {
	return (
		<FeaturePlaceholderView
			icon={ToolCase}
			title="Unlock MCP Tool Groups"
			description="This feature is a part of the Bifrost enterprise license. Configure tool groups for MCP servers to organize your MCP tools and govern them across your organization."
			readmeLink="https://docs.getbifrost.ai/mcp/overview"
		/>
	);
}
