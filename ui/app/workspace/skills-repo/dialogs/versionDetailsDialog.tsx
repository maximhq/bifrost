"use client";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogClose, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import { useGetSkillQuery, useShiftSkillVersionMutation } from "@/lib/store/apis/skillsApi";
import type { SkillFile, SkillFileEntry } from "@/lib/types/skills";
import { getApiBaseUrl } from "@/lib/utils/port";
import { Download, Loader2, RefreshCw, X } from "lucide-react";
import { toast } from "sonner";
import { composeFrontmatter } from "../components/helpers";
import { SkillHeader, SkillReadOnlyContent } from "../components/shared";

// ---------- VersionDetailDialog ----------

export function VersionDetailDialog({
	skillId,
	version,
	isServingVersion,
	open,
	onOpenChange,
}: {
	skillId: string;
	version: string;
	isServingVersion: boolean;
	open: boolean;
	onOpenChange: (open: boolean) => void;
}) {
	const { data: versionData, isLoading, isError } = useGetSkillQuery({ id: skillId, version }, { skip: !open });
	const [shiftVersion, { isLoading: isShifting }] = useShiftSkillVersionMutation();

	const skill = versionData?.skill;

	const extraFrontmatter = skill?.extra_frontmatter && Object.keys(skill.extra_frontmatter).length > 0 ? skill.extra_frontmatter : null;

	const metadata = skill?.metadata && Object.keys(skill.metadata).length > 0 ? (skill.metadata as Record<string, unknown>) : null;

	const composedSkillMd = skill
		? composeFrontmatter({
				name: skill.name,
				description: skill.description,
				license: skill.license || "",
				compatibility: skill.compatibility || "",
				allowed_tools: skill.allowed_tools || "",
				extra_frontmatter_json: skill.extra_frontmatter ? JSON.stringify(skill.extra_frontmatter) : "",
				metadata_json: skill.metadata ? JSON.stringify(skill.metadata) : "",
			}) +
			"\n\n" +
			skill.skill_md_body
		: "";

	const fileEntries: SkillFileEntry[] = skill?.files
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
		: [];

	const downloadUrl = skill
		? `${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skill.name)}/download.zip?version=${encodeURIComponent(version)}`
		: "";

	const handleShiftVersion = async () => {
		try {
			await shiftVersion({ id: skillId, version }).unwrap();
			toast.success(`Shifted to version ${version}`);
			onOpenChange(false);
		} catch (err: unknown) {
			toast.error("Failed to shift version", {
				description: getErrorMessage(err),
			});
		}
	};

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent
				showCloseButton={false}
				className="h-[90vh] max-h-[90vh] w-[95vw] max-w-[95vw] min-w-0 gap-0 overflow-hidden p-0 sm:w-[85vw] sm:max-w-[85vw] md:w-[75vw] md:max-w-[75vw]"
			>
				<div className="flex items-center justify-between border-b pr-2 pl-6">
					<DialogHeader className="p-0">
						<DialogTitle>Version {version}</DialogTitle>
					</DialogHeader>
					<div className="flex items-center gap-1.5">
						{downloadUrl && (
							<Button
								variant="ghost"
								size="icon"
								className="h-8 w-8"
								asChild
								data-testid="skill-version-download-zip"
								aria-label="Download ZIP"
							>
								<a href={downloadUrl} download>
									<Download className="h-4 w-4" />
									<span className="sr-only">Download ZIP</span>
								</a>
							</Button>
						)}
						<DialogClose asChild>
							<Button variant="ghost" size="icon" className="h-8 w-8" data-testid="skill-version-dialog-close" aria-label="Close">
								<X className="h-4 w-4" />
								<span className="sr-only">Close</span>
							</Button>
						</DialogClose>
					</div>
				</div>
				<div className="flex h-[calc(90vh-57px)] flex-col">
					{isLoading ? (
						<div className="flex flex-1 items-center justify-center">
							<Loader2 className="text-muted-foreground h-6 w-6 animate-spin" />
						</div>
					) : skill ? (
						<>
							<div className="shrink-0 px-6 pt-6 pb-3">
								<SkillHeader
									sticky={false}
									name={skill.name}
									version={version}
									description={skill.description}
									license={skill.license}
									compatibility={skill.compatibility}
									allowedTools={skill.allowed_tools}
									composedSkillMd={composedSkillMd}
									decorators={
										<>
											{isServingVersion && (
												<Badge
													variant="secondary"
													className="bg-emerald-100 text-xs text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400"
												>
													Serving
												</Badge>
											)}
										</>
									}
									actions={
										!isServingVersion ? (
											<Button size="sm" data-testid="skill-version-shift-btn" onClick={handleShiftVersion} disabled={isShifting}>
												{isShifting ? (
													<>
														<Loader2 className="h-3.5 w-3.5 animate-spin" />
														Shifting...
													</>
												) : (
													<>
														<RefreshCw className="h-3.5 w-3.5" />
														Shift to this version
													</>
												)}
											</Button>
										) : undefined
									}
								/>
							</div>

							<div className="flex min-h-0 flex-1 px-6 pb-6">
								<SkillReadOnlyContent
									skillName={skill.name}
									skillMdBody={skill.skill_md_body}
									files={fileEntries}
									extraFrontmatter={extraFrontmatter}
									metadata={metadata}
									composedSkillMd={composedSkillMd}
								/>
							</div>
						</>
					) : isError ? (
						<p className="text-muted-foreground flex flex-1 items-center justify-center text-sm">Failed to load version</p>
					) : (
						<p className="text-muted-foreground flex flex-1 items-center justify-center text-sm">Version data not found</p>
					)}
				</div>
			</DialogContent>
		</Dialog>
	);
}