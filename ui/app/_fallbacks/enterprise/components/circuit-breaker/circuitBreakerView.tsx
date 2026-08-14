import { CircuitBoard } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function CircuitBreakerView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<CircuitBoard className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="解锁熔断器以实现可靠回退"
				description="此功能属于 Bifrost 企业版许可的一部分。当主端点出现故障迹象时，自动将流量重定向到备用提供商。"
				readmeLink="https://docs.getbifrost.ai/enterprise/circuit-breaker"
			/>
		</div>
	);
}