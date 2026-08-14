import { Building2 } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export function BusinessUnitsView() {
	return (
		<div className="w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				testIdPrefix="business-units-governance"
				icon={<Building2 className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title="解锁业务单元与高级治理"
				description="通过我们的企业级治理管理用户和业务单元。此功能属于 Bifrost 企业版许可的一部分。"
				readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
			/>
		</div>
	);
}