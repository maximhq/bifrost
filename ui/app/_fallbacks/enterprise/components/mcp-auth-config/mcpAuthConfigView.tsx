import { ShieldUser } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function MCPAuthConfigView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<ShieldUser className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="解锁 MCP 认证配置"
				description="此功能属于 Bifrost 企业版许可的一部分。为 MCP 服务器配置认证以保护 MCP 连接安全。"
				readmeLink="https://docs.getbifrost.ai/mcp/overview"
			/>
		</div>
	);
}