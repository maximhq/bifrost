import { NoPermissionView } from "@/components/noPermissionView";
import AccessProfilesIndexView from "@enterprise/components/access-profiles/accessProfilesIndexView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function AccessProfilesPage() {
	const hasAccessProfilesAccess = useRbac(RbacResource.AccessProfiles, RbacOperation.View);

	if (!hasAccessProfilesAccess) {
		return <NoPermissionView entity="access-profiles" />;
	}

	return (
		<div className="mx-auto w-full max-w-7xl">
			<AccessProfilesIndexView />
		</div>
	);
}