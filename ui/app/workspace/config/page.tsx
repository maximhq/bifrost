"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useRouter } from "next/navigation";
import { useEffect } from "react";

export default function ConfigPage() {
	const router = useRouter();
	// Check permission
	const hasConfigAccess = useRbac(RbacResource.Settings, RbacOperation.View);

	useEffect(() => {
		if (hasConfigAccess) {
			router.replace("/workspace/config/client-settings");
		}
	}, [hasConfigAccess, router]);

	if (!hasConfigAccess) {
		return <NoPermissionView entity="configuration" />;
	}
	return null;
}
