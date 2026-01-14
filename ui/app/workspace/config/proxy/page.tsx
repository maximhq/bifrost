"use client";

import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import ProxyView from "../views/proxyView";

export default function ProxyPage() {
	const router = useRouter();

	useEffect(() => {
		if (!IS_ENTERPRISE) {
			router.replace("/workspace/config/client-settings");
		}
	}, [router]);

	if (!IS_ENTERPRISE) {
		return null;
	}

	return (
		<div className="mx-auto flex w-full max-w-7xl">
			<ProxyView />
		</div>
	);
}
