import { createFileRoute } from "@tanstack/react-router";
import { NoPermissionView } from "@/components/noPermissionView";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import SkillsRepoPage from "./page";

function RouteComponent() {
	const hasSkillsRepositoryAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.View);

	if (!hasSkillsRepositoryAccess) {
		return <NoPermissionView entity="skills repository" />;
	}

	return <SkillsRepoPage />;
}

export const Route = createFileRoute("/workspace/skills-repo")({
	component: RouteComponent,
});