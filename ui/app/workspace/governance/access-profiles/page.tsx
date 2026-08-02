import { NoPermissionView } from "@/components/noPermissionView";
import AccessProfilesIndexView from "@enterprise/components/access-profiles/accessProfilesIndexView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function AccessProfilesPage() {
	const hasAccessProfilesAccess = useRbac(RbacResource.AccessProfiles, RbacOperation.View);

	if (!hasAccessProfilesAccess) {
		return <NoPermissionView entity="access-profiles" />;
	}

	return (
		<div className="no-padding-parent mx-auto flex h-[calc(100dvh-1rem)] w-full flex-col p-4">
			<AccessProfilesIndexView />
		</div>
	);
}