"use client";

import { Skill } from "@/lib/types/skills";
import { composeFrontmatter } from "../components/helpers";
import { SkillReadOnlyContent } from "../components/shared";

function hasKeys(obj: Record<string, unknown> | null | undefined): obj is Record<string, unknown> {
	return obj != null && Object.keys(obj).length > 0;
}

export function SkillFormFields({ skill }: { skill: Skill }) {
	const extraFrontmatter = hasKeys(skill.extra_frontmatter) ? skill.extra_frontmatter : null;
	const metadata = hasKeys(skill.metadata) ? skill.metadata : null;

	return (
		<SkillReadOnlyContent
			className="min-h-0 flex-1"
			skillName={skill.name}
			skillMdBody={skill.skill_md_body}
			files={skill.files ?? []}
			extraFrontmatter={extraFrontmatter}
			metadata={metadata}
			composedSkillMd={
				composeFrontmatter({
					name: skill.name,
					description: skill.description,
					license: skill.license || "",
					compatibility: skill.compatibility || "",
					allowed_tools: skill.allowed_tools || "",
					extra_frontmatter_json: extraFrontmatter ? JSON.stringify(extraFrontmatter, null, 2) : "",
					metadata_json: metadata ? JSON.stringify(metadata, null, 2) : "",
				}) +
				"\n\n" +
				skill.skill_md_body
			}
		/>
	);
}