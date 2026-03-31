"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function ProvidersLayout({ children }: { children: React.ReactNode }) {
	const hasProvidersAccess = useRbac(RbacResource.ModelProvider, RbacOperation.View);
	if (!hasProvidersAccess) {
		return <NoPermissionView entity="model providers" />;
	}
	return <div>{children}</div>;
}
