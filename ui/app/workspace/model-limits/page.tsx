"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";
import ModelLimitsView from "./views/modelLimitsView";

export default function ModelLimitsPage() {
	const router = useRouter();

	useEffect(() => {
		// Temporarily disable this page and redirect to workspace logs
		router.push("/workspace/logs");
	}, [router]);

	return <ModelLimitsView />;
}
