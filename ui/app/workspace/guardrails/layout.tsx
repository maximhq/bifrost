"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function GuardrailsLayout({ children }: { children: React.ReactNode }) {
	const hasGuardrailsAccess = useRbac(RbacResource.GuardrailsConfig, RbacOperation.View);
	if (!hasGuardrailsAccess) {
		return <NoPermissionView entity="guardrails configuration" />;
	}
	return <div>{children}</div>;
}
