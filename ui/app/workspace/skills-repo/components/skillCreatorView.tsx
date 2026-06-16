"use client";

import { useCreateSkillMutation } from "@/lib/store/apis/skillsApi";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { toast } from "sonner";
import { useSkillForm } from "./helpers";
import { SkillEditView } from "../forms/skillEditForm";

// ---------- SkillCreateView ----------

export function SkillCreateView({ onCreated, onBack }: { onCreated: (id: string) => void; onBack: () => void }) {
	const hasCreateAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.Create);
	const [createSkill, { isLoading }] = useCreateSkillMutation();
	const form = useSkillForm();

	// Create mode always serves the new version — serve param is intentionally unused
	const handleCreate = async () => {
		if (!form.runValidation()) return;

		try {
			const result = await createSkill(form.getPayload()).unwrap();
			toast.success("Skill created successfully");
			onCreated(result.skill.id);
		} catch (err: unknown) {
			toast.error("Failed to create skill", {
				description: getErrorMessage(err),
			});
		}
	};

	if (!hasCreateAccess) {
		return (
			<div className="flex h-full items-center justify-center">
				<p className="text-muted-foreground">You do not have permission to create skills.</p>
			</div>
		);
	}

	return <SkillEditView form={form} onSave={() => handleCreate()} onCancel={onBack} onBack={onBack} isSaving={isLoading} mode="create" />;
}