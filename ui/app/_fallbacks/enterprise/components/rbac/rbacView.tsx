import { UserRoundCheck } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export default function RBACView() {
	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<UserRoundCheck className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="解锁角色和权限以获得更好的安全性"
				description="此功能属于 Bifrost 企业版许可的一部分。我们非常希望了解您的使用场景以及我们能如何帮助您。"
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}