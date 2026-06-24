import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import LicenseSettingsView from "@enterprise/components/license/LicenseSettingsView";

export default function LicensePage() {
	const hasAccess = useRbac(RbacResource.Settings, RbacOperation.View);
	if (!hasAccess) return <NoPermissionView entity="license settings" />;
	return (
		<div className="mx-auto flex w-full max-w-7xl">
			<LicenseSettingsView />
		</div>
	);
}
