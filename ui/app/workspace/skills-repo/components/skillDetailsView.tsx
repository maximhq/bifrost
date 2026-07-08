"use client";

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import FullPageLoader from "@/components/fullPageLoader";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { SplitButton } from "@/components/ui/splitButton";
import { useGetSkillQuery, useUpdateSkillMutation, useDeleteSkillMutation } from "@/lib/store/apis/skillsApi";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { SkillFile, SkillVersionSummary } from "@/lib/types/skills";
import { validateVersionBump } from "@/lib/validators/skills";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ArrowLeft, Download, MoreHorizontal, Plus, Loader2, Trash2 } from "lucide-react";
import { getApiBaseUrl } from "@/lib/utils/port";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { type SkillFormState, composeFrontmatter, useSkillForm } from "./helpers";
import { SkillHeader } from "./shared";
import { SkillFormFields } from "../forms/skillEditFormFields";
import { SkillEditView } from "../forms/skillEditForm";
import { SkillVersionsList } from "../dialogs/skillVersionDialog";
import { VersionDetailDialog } from "../dialogs/versionDetailsDialog";

// ---------- SkillDetailView ----------

export function SkillDetailView({
	skillId,
	isEditing,
	setIsEditing,
	onBack,
}: {
	skillId: string;
	isEditing: boolean;
	setIsEditing: (editing: boolean) => void;
	onBack: () => void;
}) {
	const hasEditAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.Delete);

	const { data: skillData, isLoading } = useGetSkillQuery(skillId);
	const [updateSkill, { isLoading: isUpdating }] = useUpdateSkillMutation();
	const [deleteSkill, { isLoading: isDeleting }] = useDeleteSkillMutation();

	const [selectedVersion, setSelectedVersion] = useState<SkillVersionSummary | null>(null);
	const [versionsOpen, setVersionsOpen] = useState(false);
	const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);

	const form = useSkillForm();
	const skill = skillData?.skill;

	const highestVersion = skill?.highest_version || skill?.latest_version || "0.0.0";

	/** Build a SkillFormState from the current skill data. Returns null if skill isn't loaded yet. */
	function buildFormState(): SkillFormState | null {
		if (!skill) return null;
		return {
			name: skill.name,
			description: skill.description,
			license: skill.license || "",
			compatibility: skill.compatibility || "",
			allowedTools: skill.allowed_tools || "",
			extraFrontmatterJson:
				skill.extra_frontmatter && Object.keys(skill.extra_frontmatter).length > 0 ? JSON.stringify(skill.extra_frontmatter, null, 2) : "",
			metadataJson: skill.metadata && Object.keys(skill.metadata).length > 0 ? JSON.stringify(skill.metadata, null, 2) : "",
			skillMdBody: skill.skill_md_body,
			version: highestVersion || "1.0.0",
			files: skill.files
				? skill.files.map((f: SkillFile) => ({
						path: f.path,
						source_type: f.source_type,
						content: f.content,
						source_url: f.source_url,
						dataurl: f.dataurl,
						storage_key: f.storage_key,
						blob_id: f.blob_id,
						mime_type: f.mime_type,
						file_size_bytes: f.file_size_bytes,
					}))
				: [],
		};
	}

	// Tracks the skill we last seeded the form from, so switching to a different
	// skill still resets even while another skill was being edited.
	const lastResetSkillIdRef = useRef<string | null>(null);

	// Populate form when skill data loads or changes. Skip resets during an
	// active edit of the same skill so a background refetch can't wipe unsaved
	// changes.
	// eslint-disable-next-line react-hooks/exhaustive-deps
	useEffect(() => {
		const state = buildFormState();
		const isNewSkill = lastResetSkillIdRef.current !== skillId;
		if (state && (!isEditing || isNewSkill)) {
			form.reset(state);
			lastResetSkillIdRef.current = skillId;
		}
	}, [skill, highestVersion, isEditing, skillId]);

	const handleSave = async (serve: boolean) => {
		if (!form.runValidation()) return;
		const bumpErr = validateVersionBump(form.version, highestVersion);
		if (bumpErr) {
			toast.error("Invalid version", { description: bumpErr });
			return;
		}

		try {
			const { name: _name, ...payload } = form.getPayload();
			await updateSkill({
				id: skillId,
				data: { ...payload, serve },
			}).unwrap();
			toast.success(serve ? "Skill saved and now serving this version" : "Version saved successfully");
			setIsEditing(false);
		} catch (err: unknown) {
			toast.error("Failed to update skill", {
				description: getErrorMessage(err),
			});
		}
	};

	const handleDelete = async () => {
		try {
			await deleteSkill(skillId).unwrap();
			toast.success("Skill deleted");
			onBack();
		} catch (err: unknown) {
			toast.error("Failed to delete skill", {
				description: getErrorMessage(err),
			});
		}
	};

	const handleCancelEdit = () => {
		const state = buildFormState();
		if (state) form.reset(state);
		setIsEditing(false);
	};

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (!skill) {
		return (
			<div className="flex w-full flex-1 flex-col items-center justify-center p-4">
				<p className="text-muted-foreground text-sm">Skill not found</p>
				<Button variant="outline" size="sm" className="mt-3" onClick={onBack}>
					<ArrowLeft className="h-3.5 w-3.5" />
					Back to list
				</Button>
			</div>
		);
	}

	return (
		<div className="relative flex min-h-0 w-full flex-1 flex-col">
			{isEditing ? (
				<SkillEditView
					form={form}
					skillName={skill.name}
					previousVersion={highestVersion}
					onSave={handleSave}
					onCancel={handleCancelEdit}
					onBack={handleCancelEdit}
					onNavigateToList={onBack}
					isSaving={isUpdating}
				/>
			) : (
				<>
					<SkillHeader
						name={skill.name}
						version={skill.latest_version}
						description={skill.description}
						license={skill.license}
						compatibility={skill.compatibility}
						allowedTools={skill.allowed_tools}
						composedSkillMd={
							composeFrontmatter({
								name: skill.name,
								description: skill.description,
								license: skill.license || "",
								compatibility: skill.compatibility || "",
								allowed_tools: skill.allowed_tools || "",
								extra_frontmatter_json:
									skill.extra_frontmatter && Object.keys(skill.extra_frontmatter).length > 0
										? JSON.stringify(skill.extra_frontmatter, null, 2)
										: "",
								metadata_json: skill.metadata && Object.keys(skill.metadata).length > 0 ? JSON.stringify(skill.metadata, null, 2) : "",
							}) +
							"\n\n" +
							skill.skill_md_body
						}
						onBack={onBack}
						actions={
							<>
								<SplitButton
									onClick={() => setIsEditing(true)}
									variant="outline"
									disabledCheck
									dropdownTrigger={{
										className: "bg-transparent",
										dataTestId: "skill-versions-popover-trigger",
										"aria-label": `Versions for ${skill.name}`,
									}}
									button={{
										dataTestId: "skill-add-version-btn",
										className: "bg-transparent",
										disabled: !hasEditAccess,
									}}
									dropdownContent={{
										align: "end",
										className: "w-72 p-0",
										open: versionsOpen,
										onOpenChange: setVersionsOpen,
										children: (
											<SkillVersionsList
												skillId={skillId}
												servingVersion={skill.latest_version}
												open={versionsOpen}
												onSelectVersion={(v) => {
													setSelectedVersion(v);
													setVersionsOpen(false);
												}}
											/>
										),
									}}
								>
									<Plus className="h-3.5 w-3.5" />
									Add New Version
								</SplitButton>
								<DropdownMenu>
									<DropdownMenuTrigger asChild>
										<Button variant="ghost" size="icon" className="h-8 w-8" aria-label={`Actions for ${skill.name}`}>
											<MoreHorizontal className="h-4 w-4" />
										</Button>
									</DropdownMenuTrigger>
									<DropdownMenuContent align="end">
										<DropdownMenuItem className="cursor-pointer" asChild>
											<a href={`${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skill.name)}/download.zip`} download>
												<Download className="h-4 w-4" />
												Download ZIP
											</a>
										</DropdownMenuItem>
										{hasDeleteAccess && (
											<DropdownMenuItem
												variant="destructive"
												className="cursor-pointer"
												disabled={isDeleting}
												onSelect={(event) => {
													event.preventDefault();
													setDeleteDialogOpen(true);
												}}
											>
												<Trash2 className="h-4 w-4" />
												Delete
											</DropdownMenuItem>
										)}
									</DropdownMenuContent>
								</DropdownMenu>
								<AlertDialog open={deleteDialogOpen} onOpenChange={setDeleteDialogOpen}>
									<AlertDialogContent>
										<AlertDialogHeader>
											<AlertDialogTitle>Delete {skill.name}?</AlertDialogTitle>
											<AlertDialogDescription>
												This action cannot be undone. The skill, its files, and version history will be permanently deleted.
											</AlertDialogDescription>
										</AlertDialogHeader>
										<AlertDialogFooter>
											<AlertDialogCancel>Cancel</AlertDialogCancel>
											<AlertDialogAction onClick={handleDelete} disabled={isDeleting}>
												{isDeleting ? (
													<>
														<Loader2 className="h-3.5 w-3.5 animate-spin" /> Deleting...
													</>
												) : (
													"Delete skill"
												)}
											</AlertDialogAction>
										</AlertDialogFooter>
									</AlertDialogContent>
								</AlertDialog>
							</>
						}
					/>

					<div className="mt-3 flex min-h-0 flex-1 flex-col">
						<SkillFormFields skill={skill} />
					</div>
				</>
			)}

			{/* Version Detail Dialog */}
			{selectedVersion && skill && (
				<VersionDetailDialog
					skillId={skillId}
					version={selectedVersion.version}
					isServingVersion={selectedVersion.version === skill.latest_version}
					open={true}
					onOpenChange={(open) => {
						if (!open) setSelectedVersion(null);
					}}
				/>
			)}
		</div>
	);
}