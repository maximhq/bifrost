import { ShieldUser } from "lucide-react";
import FeaturePlaceholderView from "../views/featurePlaceholderView";

export default function MCPAuthConfigView() {
	return (
		<FeaturePlaceholderView
			icon={ShieldUser}
			title="Unlock MCP Auth Config"
			description="This feature is a part of the Bifrost enterprise license. Configure authentication for MCP servers to secure your MCP connections."
			readmeLink="https://docs.getbifrost.ai/mcp/overview"
		/>
	);
}
