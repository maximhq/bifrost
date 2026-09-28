import { CircuitBoard } from "lucide-react";
import { useTranslation } from "react-i18next";
import ContactUsView from "../views/contactUsView";

export default function CircuitBreakerView() {
	const { t } = useTranslation("models");

	return (
		<div className="h-full w-full">
			<ContactUsView
				className="mx-auto min-h-[80vh]"
				icon={<CircuitBoard className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
				title={t("routing.circuitBreakerUnlockTitle")}
				description={t("routing.circuitBreakerUnlockDescription")}
				readmeLink="https://docs.getbifrost.ai/enterprise/circuit-breaker"
			/>
		</div>
	);
}
