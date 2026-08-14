import { ToolCase } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function MCPToolGroups() {
	return (
		<>
			<div className="mb-4 flex items-center justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold tracking-tight">MCP 工具组</h2>
					<p className="text-muted-foreground text-sm">为 MCP 服务器配置工具组，以组织和管理工具。</p>
				</div>
			</div>
			<div className="rounded-sm border">
				<div className="flex w-full flex-col items-center justify-center py-16">
					<ContactUsView
						className="mx-auto w-full max-w-lg"
						icon={<ToolCase className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
						title="解锁 MCP 工具组"
						description="此功能属于 Bifrost 企业版许可的一部分。为 MCP 服务器配置工具组，以组织您的 MCP 工具并在整个组织中管理它们。"
						readmeLink="https://docs.getbifrost.ai/mcp/overview"
					/>
				</div>
			</div>
		</>
	);
}