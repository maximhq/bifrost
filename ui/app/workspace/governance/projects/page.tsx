import { NoPermissionView } from "@/components/noPermissionView";
import ProjectsIndexView from "@enterprise/components/projects/projectsIndexView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";

export default function ProjectsPage() {
	const hasProjectsAccess = useRbac(RbacResource.Projects, RbacOperation.View);

	if (!hasProjectsAccess) {
		return <NoPermissionView entity="projects" />;
	}

	return (
		<div className="no-padding-parent mx-auto flex h-[calc(var(--app-content-viewport)_-_var(--app-bottom-padding))] w-full flex-col p-4">
			<ProjectsIndexView />
		</div>
	);
}