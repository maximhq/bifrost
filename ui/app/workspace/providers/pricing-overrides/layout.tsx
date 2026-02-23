"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function PricingOverridesLayout({ children }: { children: React.ReactNode }) {
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	if (!hasGovernanceAccess) {
		return <NoPermissionView entity="pricing overrides" />;
	}
	return <>{children}</>;
}
