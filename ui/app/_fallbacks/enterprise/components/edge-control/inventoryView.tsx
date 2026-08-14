import { ShieldCheck } from "lucide-react";
import EdgeControlFallbackView from "./fallbackWrapper";

export default function InventoryView() {
	return (
		<EdgeControlFallbackView
			icon={<ShieldCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
			title="解锁边缘控制以审批应用和 MCP 服务器"
			description="此功能属于 Bifrost 企业版许可的一部分。我们非常希望了解您的使用场景以及我们能如何帮助您。"
			readmeLink="https://docs.getbifrost.ai/edge/admin-approvals"
			testIdPrefix="edge-inventory"
		/>
	);
}
