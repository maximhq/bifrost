import { IS_ENTERPRISE } from "@/lib/constants/config";
import BrandingView from "@enterprise/components/branding/brandingView";
import { useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

export default function BrandingPage() {
	const navigate = useNavigate();

	// custom branding is enterprise-only: OSS keeps the Bifrost branding, and
	// the backend exposes no write endpoint there, so the page would be inert.
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
			<BrandingView />
		</div>
	);
}
