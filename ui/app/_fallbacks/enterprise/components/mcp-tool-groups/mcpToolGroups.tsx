import { ToolCase } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function MCPToolGroups() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<ToolCase className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="Unlock MCP Tool Groups"
				description="This feature is a part of the Bifrost enterprise license. Configure tool groups for MCP servers to organize your MCP tools and govern them across your organization."
				readmeLink="https://docs.getbifrost.ai/mcp/overview"
			/>
		</div>
	);
}
