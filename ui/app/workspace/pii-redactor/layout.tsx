"use client";

import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function PiiRedactorLayout({ children }: { children: React.ReactNode }) {
	const hasPiiRedactorAccess = useRbac(RbacResource.PIIRedactor, RbacOperation.View);
	if (!hasPiiRedactorAccess) {
		return <NoPermissionView entity="PII redactor" />;
	}
	return <div>{children}</div>;
}
