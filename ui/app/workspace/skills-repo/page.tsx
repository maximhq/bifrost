"use client";

import { parseAsBoolean, parseAsString, useQueryStates } from "nuqs";
import { SkillCreateView } from "./components/skillCreatorView";
import { SkillDetailView } from "./components/skillDetailsView";
import { SkillsListView } from "./components/skillListView";
export default function SkillsRepoPage() {
	const [urlState, setUrlState] = useQueryStates(
		{
			skillId: parseAsString,
			edit: parseAsBoolean.withDefault(false),
			create: parseAsBoolean.withDefault(false),
		},
		{ history: "push" },
	);

	const handleSelectSkill = (id: string, edit = false) => {
		setUrlState({ skillId: id, edit, create: false });
	};

	const handleBack = () => {
		setUrlState({ skillId: null, edit: false, create: false });
	};

	const handleCreated = (id: string) => {
		setUrlState({ skillId: id, edit: false, create: false });
	};

	const setIsEditing = (editing: boolean) => {
		setUrlState({ edit: editing });
	};

	// Create view
	if (urlState.create) {
		return (
			<div className="no-padding-parent flex h-full w-full min-w-0 flex-col p-0">
				<SkillCreateView onCreated={handleCreated} onBack={handleBack} />
			</div>
		);
	}

	// Detail view when skillId is set
	if (urlState.skillId) {
		return (
			<div
				className={
					urlState.edit
						? "no-padding-parent flex h-full w-full min-w-0 flex-col p-0"
						: "no-padding-parent flex h-full w-full min-w-0 flex-col p-4 pt-0"
				}
			>
				<SkillDetailView skillId={urlState.skillId} isEditing={urlState.edit} setIsEditing={setIsEditing} onBack={handleBack} />
			</div>
		);
	}

	// List view
	return (
		<div className="no-padding-parent flex min-h-full w-full min-w-0 flex-col p-4 md:h-[calc(var(--app-content-viewport)_-_16px)] md:min-h-0">
			<SkillsListView onSelectSkill={handleSelectSkill} onCreateNew={() => setUrlState({ create: true, skillId: null, edit: false })} />
		</div>
	);
}