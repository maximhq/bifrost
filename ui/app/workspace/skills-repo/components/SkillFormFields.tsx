"use client";

import { Skill, SkillFileEntry } from "@/lib/types/skills";
import { composeFrontmatter } from "./helpers";
import { SkillReadOnlyContent } from "./shared";

// ---------- SkillFormFields ----------

export function SkillFormFields({ skill }: { skill: Skill }) {
  const files: SkillFileEntry[] = (skill.files ?? []).map((file) => ({
    path: file.path,
    source_type: file.source_type,
    source_url: file.source_url,
    filepath: file.source_filepath,
    storage_key: file.storage_key,
    blob_id: file.blob_id,
    mime_type: file.mime_type,
    file_size_bytes: file.file_size_bytes,
  }));

  return (
    <SkillReadOnlyContent
      skillName={skill.name}
      skillMdBody={skill.skill_md_body}
      files={files}
      extraFrontmatter={
        skill.extra_frontmatter && Object.keys(skill.extra_frontmatter).length > 0
          ? skill.extra_frontmatter
          : null
      }
      metadata={
        skill.metadata && Object.keys(skill.metadata).length > 0
          ? skill.metadata
          : null
      }
      composedSkillMd={
        composeFrontmatter({
          name: skill.name,
          description: skill.description,
          license: skill.license || "",
          compatibility: skill.compatibility || "",
          allowed_tools: skill.allowed_tools || "",
          extra_frontmatter_json:
            skill.extra_frontmatter &&
            Object.keys(skill.extra_frontmatter).length > 0
              ? JSON.stringify(skill.extra_frontmatter, null, 2)
              : "",
          metadata_json:
            skill.metadata && Object.keys(skill.metadata).length > 0
              ? JSON.stringify(skill.metadata, null, 2)
              : "",
        }) +
        "\n\n" +
        skill.skill_md_body
      }
    />
  );
}

