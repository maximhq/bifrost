"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function GovernanceLayout({ children }: { children: React.ReactNode }) {
	const hasGovernanceAccess = useRbac(RbacResource.Governance, RbacOperation.View);
	if (!hasGovernanceAccess) {
		return <NoPermissionView entity="governance" />;
	}
	return <div>{children}</div>;
}
