import { IS_ENTERPRISE } from "@/lib/constants/config";
import CanarySetupView from "@enterprise/components/canary/canarySetupView";
import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

export default function CanaryPage() {
	const navigate = useNavigate();

	useEffect(() => {
		if (!IS_ENTERPRISE) {
			navigate({ to: "/workspace/config/client-settings", replace: true });
		}
	}, [navigate]);

	if (!IS_ENTERPRISE) {
		return null;
	}

	return (
		<div className="mx-auto flex w-full max-w-7xl px-4 md:px-0">
			<CanarySetupView />
		</div>
	);
}