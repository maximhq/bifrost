import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";
import ProxyView from "../views/proxyView";

export default function ProxyPage() {
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
		<div className="mx-auto flex w-full max-w-7xl">
			<ProxyView />
		</div>
	);
}