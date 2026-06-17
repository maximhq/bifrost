"use client";

import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { CodeEditor, type CompletionItem } from "@/components/ui/codeEditor";
import { Dialog, DialogClose, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverAnchor, PopoverContent } from "@/components/ui/popover";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { ScrollArea, ScrollBar } from "@/components/ui/scrollArea";
import { Textarea } from "@/components/ui/textarea";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import type { SkillFileEntry } from "@/lib/types/skills";
import { cn } from "@/lib/utils";
import { validateSkillForm, validateVersionBump } from "@/lib/validators/skills";
import { AlertTriangle, Check, Copy, Eye, Info, Loader2, Plus, Save, Search, Settings2, X } from "lucide-react";
import { useRef, useState } from "react";
import { FileManagerSection } from "../components/fileManagerView";
import { FilePreviewPane } from "../components/filePreview";
import { composeFrontmatter, type SkillFormReturn } from "../components/helpers";
import { MetadataTableEditor } from "../components/metadataEditorTableView";
import { FormSection, SkillMarkdown } from "../components/shared";

export function SkillEditView({
	form,
	skillName,
	previousVersion,
	onSave,
	onCancel,
	onBack,
	onNavigateToList,
	isSaving,
	mode = "edit",
}: {
	form: SkillFormReturn;
	skillName?: string;
	previousVersion?: string;
	onSave: (serve: boolean) => void;
	onCancel: () => void;
	onBack: () => void;
	/** Navigate all the way back to the skills list (the "Skills" breadcrumb crumb). Falls back to onBack. */
	onNavigateToList?: () => void;
	isSaving: boolean;
	mode?: "edit" | "create";
}) {
	const isCreate = mode === "create";
	const [bodyTab, setBodyTab] = useState<"edit" | "preview">("edit");
	const [showPreviewDialog, setShowPreviewDialog] = useState(false);
	const [fileSearchQuery, setFileSearchQuery] = useState("");
	// Two-pane workspace selection: null = SKILL.md body, otherwise a file index.
	const [selectedFileIndex, setSelectedFileIndex] = useState<number | null>(null);
	const [selectedDetailsPane, setSelectedDetailsPane] = useState<"details" | null>("details");
	const selectedFile = selectedFileIndex != null ? (form.files[selectedFileIndex] ?? null) : null;

	// The version is asked for in a popover anchored to the Save button (not in the form).
	const [versionPopover, setVersionPopover] = useState<{
		serve: boolean;
	} | null>(null);

	// Lets the file editor commit any buffered ("Save file") content before a version save.
	const flushFileEditRef = useRef<(() => void) | null>(null);

	// Which pane each validatable field lives in, so a failed save can surface
	// the error instead of silently blocking from another tab.
	const focusFieldError = (field: string): boolean => {
		switch (field) {
			case "description":
			case "metadata":
			case "extra_frontmatter":
				setSelectedDetailsPane("details");
				setSelectedFileIndex(null);
				return true;
			case "skill_md_body":
				setSelectedDetailsPane(null);
				setSelectedFileIndex(null);
				return true;
			// "name" renders in the always-visible header (create mode); "version" is
			// handled in its own dialog — neither needs pane navigation.
			default:
				return false;
		}
	};

	const openVersionPopover = (serve: boolean) => {
		flushFileEditRef.current?.();
		// Validate up front so an error hiding in another pane navigates into view
		// rather than leaving the user staring at a Save button that does nothing.
		const errors = validateSkillForm({
			name: form.name,
			description: form.description,
			version: form.version,
			skill_md_body: form.skillMdBody,
			extra_frontmatter_json: form.extraFrontmatterJson || undefined,
			metadata_json: form.metadataJson || undefined,
		});
		if (errors.length > 0) {
			// Reuse the form's validators to populate inline errors in each pane.
			form.validateField("name", form.name);
			form.validateField("description", form.description);
			form.validateField("extra_frontmatter", form.extraFrontmatterJson);
			form.validateField("metadata", form.metadataJson);
			// Navigate to the first error that maps to a pane; the inline error in
			// that pane tells the user what to fix.
			errors.find((e) => focusFieldError(e.field));
			return;
		}
		// Suggest a patch bump for edits when the version still equals the previous one.
		if (!isCreate && previousVersion) {
			const match = previousVersion.match(/^(\d+)\.(\d+)\.(\d+)$/);
			if (match && (form.version === previousVersion || !form.version.trim())) {
				const suggested = `${match[1]}.${match[2]}.${Number(match[3]) + 1}`;
				form.setVersion(suggested);
				form.validateField("version", suggested);
			}
		}
		setVersionPopover({ serve });
	};

	// Closing the popover restores a valid version so a half-typed value can't
	// make the next save attempt fail validation before the popover reopens.
	const closeVersionPopover = () => {
		if (form.errors.version) {
			const restore = !isCreate && previousVersion ? previousVersion : "1.0.0";
			form.setVersion(restore);
			form.validateField("version", restore);
		}
		setVersionPopover(null);
	};
	const { copy: copyPreviewContent, copied: copiedPreviewContent } = useCopyToClipboard({
		successMessage: "Copied raw SKILL.md",
		errorMessage: "Failed to copy raw SKILL.md",
	});

	const previewContent =
		composeFrontmatter({
			name: form.name,
			description: form.description,
			license: form.license,
			compatibility: form.compatibility,
			allowed_tools: form.allowedTools,
			extra_frontmatter_json: form.extraFrontmatterJson,
			metadata_json: form.metadataJson,
		}) +
		"\n\n" +
		form.skillMdBody;

	const filePathCompletions = buildFilePathCompletions(form.files);

	const descriptionLength = form.description.length;
	let descriptionLimitColor = "text-muted-foreground";
	if (descriptionLength > 1024) {
		descriptionLimitColor = "text-destructive";
	} else if (descriptionLength > 900) {
		descriptionLimitColor = "text-yellow-600 dark:text-yellow-500";
	}

	return (
		<div className="flex h-full min-h-0 flex-col overflow-hidden">
			<div className="shrink-0 overflow-y-auto px-4">
				{/* Breadcrumb */}
				<nav aria-label="Breadcrumb" className="py-4">
					<ol className="text-muted-foreground flex min-w-0 items-center gap-1.5 text-sm">
						<li>
							<button
								type="button"
								data-testid="skill-back-btn"
								onClick={onNavigateToList ?? onBack}
								className="hover:text-foreground cursor-pointer transition-colors"
							>
								Skills
							</button>
						</li>
						<li aria-hidden="true" className="text-muted-foreground/60">
							/
						</li>
						{isCreate ? (
							<li aria-current="page" className="text-foreground min-w-0 truncate font-medium">
								{form.name || "<new-skill>"}
							</li>
						) : (
							<>
								<li className="min-w-0 truncate">
									<button
										type="button"
										data-testid="skill-view-btn"
										onClick={onBack}
										className="hover:text-foreground min-w-0 cursor-pointer truncate transition-colors"
									>
										{skillName}
									</button>
								</li>
								<li aria-hidden="true" className="text-muted-foreground/60">
									/
								</li>
								<li aria-current="page" className="text-foreground font-medium">
									new
								</li>
							</>
						)}
					</ol>
				</nav>

				<Alert variant="info">
					<AlertTriangle aria-hidden="true" />
					<AlertDescription>
						Files added to a skill can be downloaded from marketplace URLs without logging in. Anyone who can reach this Bifrost server can
						request them directly, so do not upload secrets, credentials, private code, or other sensitive files.
					</AlertDescription>
				</Alert>

				{/* Edit sections */}
				{isCreate && (
					<div className="flex flex-col gap-8 pt-4">
						<section className="flex flex-col gap-2">
							<div className="flex items-center gap-1.5">
								<h2 className="text-foreground text-base leading-[normal] font-semibold">Name</h2>
								<Tooltip>
									<TooltipTrigger asChild>
										<button
											type="button"
											className="text-muted-foreground hover:text-foreground inline-flex h-4 w-4 items-center justify-center"
											aria-label="Skill names cannot be changed after creation"
										>
											<Info className="h-3.5 w-3.5" aria-hidden="true" />
										</button>
									</TooltipTrigger>
									<TooltipContent className="max-w-xs text-xs">Name cannot be changed after creation.</TooltipContent>
								</Tooltip>
							</div>
							<div className="flex flex-col gap-1">
								<Input
									data-testid="skill-name-input"
									value={form.name}
									onChange={(e) => {
										form.setName(e.target.value);
										form.validateField("name", e.target.value);
									}}
									placeholder="my-skill-name"
									className={cn(form.errors.name && "border-destructive")}
								/>
								{form.errors.name && (
									<p className="text-destructive text-xs" role="alert">
										{form.errors.name}
									</p>
								)}
							</div>
						</section>
					</div>
				)}

				{/* Details now render in the right pane, selected from above the file tree. */}
			</div>

			{/* Files + SKILL.md two-pane workspace */}
			<div className="min-h-0 flex-1 px-4 pt-4">
				<ResizablePanelGroup direction="horizontal" className="h-full min-h-0">
					{/* Left: files panel */}
					<ResizablePanel defaultSize="28%" minSize="18%" maxSize="50%" className="bg-card flex min-h-0 flex-col gap-2">
						<p className="text-muted-foreground/70 px-1 text-[10px] font-semibold tracking-wider uppercase">Details</p>
						<button
							type="button"
							data-testid="skill-details-pane-btn"
							onClick={() => {
								setSelectedDetailsPane("details");
								setSelectedFileIndex(null);
							}}
							className={cn(
								"flex items-center gap-2 rounded-md border px-3 py-2 text-left text-xs font-medium transition-colors",
								selectedDetailsPane === "details"
									? "border-primary/20 bg-primary/10 text-primary hover:bg-primary/10"
									: "bg-card hover:bg-muted",
							)}
						>
							<Settings2 className="h-3.5 w-3.5 shrink-0" />
							Skill Metadata
						</button>
						<p className="text-muted-foreground/70 mt-2 px-1 text-[10px] font-semibold tracking-wider uppercase">Files</p>
						<div className="bg-card flex min-h-0 flex-1 flex-col rounded-md border">
							<div className="flex h-9 items-center border-b">
								<div className="relative grow">
									<Search className="text-muted-foreground absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
									<Input
										placeholder="Search files..."
										value={fileSearchQuery}
										onChange={(e) => setFileSearchQuery(e.target.value)}
										data-testid="sidebar-search"
										className="h-9 border-none pl-8"
									/>
								</div>
							</div>
							<ScrollArea className="min-h-0 flex-1 p-1" viewportClassName="overflow-x-auto">
								<FileManagerSection
									files={form.files}
									onAddFile={form.addFile}
									onRemoveFile={form.removeFile}
									onUpdateFile={form.updateFile}
									readOnly={false}
									selectedIndex={selectedDetailsPane == null ? selectedFileIndex : null}
									onSelectFile={(index) => {
										setSelectedDetailsPane(null);
										setSelectedFileIndex(index);
									}}
									bodySelected={selectedFile == null && selectedDetailsPane == null}
									hasBodyError={!form.skillMdBody.trim()}
									onSelectBody={() => {
										setSelectedDetailsPane(null);
										setSelectedFileIndex(null);
									}}
									searchQuery={fileSearchQuery}
									onSearchChange={setFileSearchQuery}
								/>
								<ScrollBar orientation="horizontal" />
							</ScrollArea>
						</div>
					</ResizablePanel>

					<ResizableHandle className="mx-1.5 bg-transparent" />

					{/* Right: editor for the selected item */}
					<ResizablePanel defaultSize="72%" minSize="30%" className="flex min-h-0 flex-col overflow-auto">
						{selectedDetailsPane === "details" ? (
							<DetailsEditorPane form={form} descriptionLength={descriptionLength} descriptionLimitColor={descriptionLimitColor} />
						) : selectedFile ? (
							<FilePreviewPane
								key={selectedFile.path}
								file={selectedFile}
								skillName={skillName ?? form.name ?? ""}
								mode="edit"
								registerFlush={(flush) => {
									flushFileEditRef.current = flush;
								}}
								onFileUpdate={(updates) => {
									if (selectedFileIndex == null) return;
									form.updateFile(selectedFileIndex, updates);
								}}
								onContentChange={(editedText) => {
									if (selectedFileIndex == null) return;
									if (selectedFile.source_type === "dataurl") {
										// Re-encode edited text as a data URI; drop old storage refs
										// so the backend creates a new blob for the new version.
										const dataurl = `data:${selectedFile.mime_type || "text/plain"};base64,${btoa(unescape(encodeURIComponent(editedText)))}`;
										form.updateFile(selectedFileIndex, {
											dataurl,
											blob_id: undefined,
											storage_key: undefined,
										});
									} else {
										// text source: submit raw content; drop old storage refs.
										form.updateFile(selectedFileIndex, {
											content: editedText,
											blob_id: undefined,
											storage_key: undefined,
										});
									}
								}}
							/>
						) : (
							<div className="flex h-full min-h-0 flex-col overflow-hidden rounded-sm border">
								<div className="flex h-9 shrink-0 items-center gap-1 border-b px-2" role="tablist" aria-label="Body editor tabs">
									<button
										type="button"
										className={cn(
											"px-3 py-1 text-xs rounded-sm transition-colors cursor-pointer",
											bodyTab === "edit" ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground",
										)}
										data-testid="skill-body-tab-edit"
										onClick={() => setBodyTab("edit")}
										role="tab"
										aria-selected={bodyTab === "edit"}
									>
										Edit
									</button>
									<button
										type="button"
										className={cn(
											"px-3 py-1 text-xs rounded-sm transition-colors cursor-pointer",
											bodyTab === "preview" ? "bg-muted font-medium" : "text-muted-foreground hover:text-foreground",
										)}
										data-testid="skill-body-tab-preview"
										onClick={() => setBodyTab("preview")}
										role="tab"
										aria-selected={bodyTab === "preview"}
									>
										Preview
									</button>
									<span className="text-muted-foreground ml-auto pr-1 text-xs">
										Use <code className="font-mono">@</code> to reference files
									</span>
								</div>
								<div className="min-h-0 grow overflow-y-auto">
									{bodyTab === "edit" ? (
										<CodeEditor
											className="z-0 w-full"
											code={form.skillMdBody}
											lang="markdown"
											onChange={(value: string) => form.setSkillMdBody(value)}
											height="100%"
											wrap
											customCompletions={filePathCompletions}
											options={{
												showVerticalScrollbar: true,
												scrollBeyondLastLine: false,
												lineNumbers: "on",
												alwaysConsumeMouseWheel: false,
												quickSuggestions: false,
											}}
										/>
									) : (
										<div className="p-4">
											<SkillMarkdown
												content={form.skillMdBody}
												files={form.files}
												onSelectFile={(path) => {
													const index = form.files.findIndex((f) => f.path === path);
													if (index === -1) return;
													setSelectedDetailsPane(null);
													setSelectedFileIndex(index);
												}}
												className="text-sm"
											/>
										</div>
									)}
								</div>
								{(form.errors.skill_md_body || form.bodyWarning) && (
									<div className="shrink-0 border-t px-3 py-1.5">
										{form.errors.skill_md_body && (
											<p className="text-destructive text-xs" role="alert">
												{form.errors.skill_md_body}
											</p>
										)}
										{form.bodyWarning && (
											<p className="text-xs text-yellow-600 dark:text-yellow-500" role="status">
												{form.bodyWarning}
											</p>
										)}
									</div>
								)}
							</div>
						)}
					</ResizablePanel>
				</ResizablePanelGroup>
			</div>

			{/* Dims the page behind the version popover so it reads as a focused step, like a dialog. */}
			{versionPopover != null && (
				<div className="animate-in fade-in-0 fixed inset-0 z-50 bg-black/50" onClick={closeVersionPopover} aria-hidden />
			)}

			<div className="dark:bg-card/95 sticky bottom-0 z-20 mt-4 flex items-center justify-end gap-2 border-t bg-white/95 px-1 py-3 backdrop-blur">
				<Button
					variant="ghost"
					size="sm"
					data-testid="skill-cancel-btn"
					onClick={onCancel}
					className="text-muted-foreground hover:bg-transparent hover:text-red-600 dark:hover:text-red-400"
				>
					Cancel
				</Button>
				<Button variant="outline" size="sm" data-testid="skill-preview-btn" onClick={() => setShowPreviewDialog(true)}>
					<Eye className="h-3.5 w-3.5" />
					Preview Raw SKILL.md
				</Button>
				{isCreate ? (
					<Popover open={versionPopover != null} onOpenChange={(open) => !open && closeVersionPopover()}>
						<PopoverAnchor asChild>
							<Button size="sm" data-testid="skill-create-save-btn" onClick={() => openVersionPopover(true)} disabled={isSaving}>
								{isSaving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Plus className="h-3.5 w-3.5" />}
								{isSaving ? "Creating..." : "Create Skill"}
							</Button>
						</PopoverAnchor>
						<PopoverContent align="end" className="w-max">
							<VersionPopoverBody
								form={form}
								isCreate={isCreate}
								previousVersion={previousVersion}
								isSaving={isSaving}
								serve={versionPopover?.serve ?? false}
								onClose={closeVersionPopover}
								onSave={(serve) => {
									setVersionPopover(null);
									onSave(serve);
								}}
							/>
						</PopoverContent>
					</Popover>
				) : (
					<>
						<Popover open={versionPopover?.serve === false} onOpenChange={(open) => !open && closeVersionPopover()}>
							<PopoverAnchor asChild>
								<Button
									variant="outline"
									size="sm"
									data-testid="skill-save-btn"
									onClick={() => openVersionPopover(false)}
									disabled={isSaving}
								>
									{isSaving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
									{isSaving ? "Saving..." : "Save"}
								</Button>
							</PopoverAnchor>
							<PopoverContent align="end" className="w-max">
								<VersionPopoverBody
									form={form}
									isCreate={isCreate}
									previousVersion={previousVersion}
									isSaving={isSaving}
									serve={false}
									onClose={closeVersionPopover}
									onSave={(serve) => {
										setVersionPopover(null);
										onSave(serve);
									}}
								/>
							</PopoverContent>
						</Popover>
						<Popover open={versionPopover?.serve === true} onOpenChange={(open) => !open && closeVersionPopover()}>
							<PopoverAnchor asChild>
								<Button size="sm" data-testid="skill-save-serve-btn" onClick={() => openVersionPopover(true)} disabled={isSaving}>
									{isSaving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
									{isSaving ? "Saving..." : "Save & Serve"}
								</Button>
							</PopoverAnchor>
							<PopoverContent align="end" className="w-max">
								<VersionPopoverBody
									form={form}
									isCreate={isCreate}
									previousVersion={previousVersion}
									isSaving={isSaving}
									serve={true}
									onClose={closeVersionPopover}
									onSave={(serve) => {
										setVersionPopover(null);
										onSave(serve);
									}}
								/>
							</PopoverContent>
						</Popover>
					</>
				)}
			</div>

			{/* Preview SKILL.md Dialog */}
			<Dialog open={showPreviewDialog} onOpenChange={setShowPreviewDialog}>
				<DialogContent
					showCloseButton={false}
					className="h-[90vh] w-full border-0 p-0 sm:w-[85vw] sm:max-w-[85vw] md:w-[75vw] md:max-w-[75vw]"
				>
					<DialogHeader className="sr-only">
						<DialogTitle>SKILL.md Preview</DialogTitle>
					</DialogHeader>
					<div className="bg-muted relative overflow-hidden rounded-sm border shadow-lg">
						<div className="absolute top-3 right-3 z-10 flex items-center gap-1">
							<Button
								variant="ghost"
								size="icon"
								data-testid="skill-preview-copy-btn"
								className="bg-background/70 text-muted-foreground hover:bg-background/90 hover:text-foreground h-8 w-8 rounded-sm"
								onClick={() => copyPreviewContent(previewContent)}
								aria-label={copiedPreviewContent ? "Raw SKILL.md copied" : "Copy raw SKILL.md"}
							>
								{copiedPreviewContent ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}
							</Button>
							<DialogClose className="text-muted-foreground hover:bg-background/80 hover:text-foreground cursor-pointer rounded-sm p-1.5 transition-colors">
								<X className="h-4 w-4" />
								<span className="sr-only">Close</span>
							</DialogClose>
						</div>
						<ScrollArea className="h-dvh" viewportClassName="bg-muted">
							<pre className="bg-muted min-h-96 p-5 pr-24 font-mono text-xs leading-5 whitespace-pre-wrap">{previewContent}</pre>
						</ScrollArea>
					</div>
				</DialogContent>
			</Dialog>
		</div>
	);
}

/** Build autocomplete items for referencing skill files with @[name](path) syntax. */
function buildFilePathCompletions(files: SkillFileEntry[]): CompletionItem[] {
	const completions: CompletionItem[] = [];
	const folderPaths = new Set<string>();

	for (const file of files) {
		if (!file.path) continue;
		const pathParts = file.path.split("/").filter(Boolean);
		const fileName = pathParts.at(-1) ?? file.path;
		const rootRelativePath = `./${file.path}`;

		for (let i = 0; i < pathParts.length - 1; i++) {
			folderPaths.add(pathParts.slice(0, i + 1).join("/"));
		}

		completions.push({
			label: fileName,
			insertText: `@[${fileName}](${rootRelativePath})`,
			type: "object" as const,
			description: rootRelativePath,
			documentation: `Full path: ${rootRelativePath}`,
		});
	}

	for (const folderPath of folderPaths) {
		const folderName = folderPath.split("/").filter(Boolean).pop() ?? folderPath;
		const rootRelativePath = `./${folderPath}/`;
		completions.push({
			label: folderName,
			insertText: `@[${folderName}](${rootRelativePath})`,
			type: "folder" as const,
			description: rootRelativePath,
			documentation: `Full path: ${rootRelativePath}`,
		});
	}

	return completions.sort((a, b) => a.description?.localeCompare(b.description ?? "") ?? 0);
}

function DetailsEditorPane({
	form,
	descriptionLength,
	descriptionLimitColor,
}: {
	form: SkillFormReturn;
	descriptionLength: number;
	descriptionLimitColor: string;
}) {
	return (
		<div className="flex h-full min-h-0 flex-col overflow-hidden">
			<ScrollArea className="min-h-0 flex-1 rounded-sm border">
				<div className="flex flex-col gap-8 p-4">
					<FormSection title="Description">
						<div className="flex flex-col gap-2">
							<Textarea
								data-testid="skill-description-input"
								value={form.description}
								onChange={(e) => {
									form.setDescription(e.target.value);
									form.validateField("description", e.target.value);
								}}
								placeholder="What does this skill do?"
								rows={3}
								className={form.errors.description ? "border-destructive" : undefined}
							/>
							{form.errors.description ? (
								<p className="text-destructive text-xs" role="alert">
									{form.errors.description}
								</p>
							) : null}
							<p className={cn("text-right text-xs", descriptionLimitColor)}>{descriptionLength}/1024</p>
						</div>
					</FormSection>

					<FormSection title="Spec Fields">
						<div className="grid grid-cols-3 gap-4">
							<div className="flex flex-col gap-1">
								<Label className="text-muted-foreground text-xs">License</Label>
								<Input
									data-testid="skill-license-input"
									value={form.license}
									onChange={(e) => form.setLicense(e.target.value)}
									placeholder="MIT (optional)"
									className="h-8 text-sm"
								/>
							</div>
							<div className="flex flex-col gap-1">
								<Label className="text-muted-foreground text-xs">Compatibility</Label>
								<Input
									data-testid="skill-compatibility-input"
									value={form.compatibility}
									onChange={(e) => form.setCompatibility(e.target.value)}
									placeholder="Claude Code, Codex (optional)"
									className="h-8 text-sm"
								/>
							</div>
							<div className="flex flex-col gap-1">
								<Label className="text-muted-foreground text-xs">Allowed Tools</Label>
								<Input
									data-testid="skill-allowed-tools-input"
									value={form.allowedTools}
									onChange={(e) => form.setAllowedTools(e.target.value)}
									placeholder="Bash Read Grep (optional)"
									className="h-8 text-sm"
								/>
							</div>
						</div>
					</FormSection>

					<FormSection
						title="Metadata"
						helperText={
							<>
								Flat key-value pairs nested under <code className="font-mono">metadata:</code> in SKILL.md
							</>
						}
					>
						<MetadataTableEditor
							metadataJson={form.metadataJson}
							onChange={(json) => {
								form.setMetadataJson(json);
								form.validateField("metadata", json);
							}}
							error={form.errors.metadata}
						/>
					</FormSection>

					<FormSection title="Extra Frontmatter" helperText="Valid JSON merged into the SKILL.md YAML frontmatter">
						<div className="flex flex-col gap-2">
							<div className="h-64 overflow-hidden rounded-sm border">
								<CodeEditor
									className="z-0 w-full py-2"
									code={form.extraFrontmatterJson}
									lang="json"
									onChange={(value: string) => {
										form.setExtraFrontmatterJson(value);
										form.validateField("extra_frontmatter", value);
									}}
									height="100%"
									wrap
									options={{
										showVerticalScrollbar: true,
										scrollBeyondLastLine: false,
										lineNumbers: "off",
										alwaysConsumeMouseWheel: false,
										showIndentLines: false,
									}}
								/>
							</div>
							{form.errors.extra_frontmatter && (
								<p className="text-destructive text-xs" role="alert">
									{form.errors.extra_frontmatter}
								</p>
							)}
						</div>
					</FormSection>
				</div>
			</ScrollArea>
		</div>
	);
}

/** The version input + confirm/cancel inside the version dialog. */
function VersionPopoverBody({
	form,
	isCreate,
	previousVersion,
	isSaving,
	serve,
	onClose,
	onSave,
}: {
	form: SkillFormReturn;
	isCreate: boolean;
	previousVersion?: string;
	isSaving: boolean;
	serve: boolean;
	onClose: () => void;
	onSave: (serve: boolean) => void;
}) {
	const bumpError = !isCreate && previousVersion ? validateVersionBump(form.version, previousVersion) : null;
	const versionError = form.errors.version || bumpError;
	const canSave = !!form.version.trim() && !versionError && !isSaving;

	const submit = () => {
		if (!canSave) return;
		onSave(serve);
	};

	return (
		<>
			<div className="mb-3 flex flex-col gap-0.5">
				<p className="text-sm font-medium">{isCreate ? "Create skill" : "Save new version"}</p>
				<p className="text-muted-foreground text-xs">
					{isCreate ? "Set the initial version for this skill." : "Choose a new version number for these changes."}
				</p>
			</div>
			<div className="flex flex-col gap-1.5">
				<Label className="text-muted-foreground text-xs">Version</Label>
				<div className="flex items-center gap-2">
					{!isCreate && previousVersion && (
						<>
							<span className="text-muted-foreground font-mono text-sm">{previousVersion}</span>
							<span className="text-muted-foreground/50">→</span>
						</>
					)}
					<Input
						autoFocus
						data-testid="skill-version-input"
						value={form.version}
						onChange={(e) => {
							form.setVersion(e.target.value);
							form.validateField("version", e.target.value);
						}}
						onKeyDown={(e) => {
							if (e.key === "Enter") {
								e.preventDefault();
								submit();
							}
						}}
						placeholder="1.0.0"
						className={cn("max-w-44 font-mono text-sm", versionError && "border-destructive")}
					/>
				</div>
				{versionError ? (
					<p className="text-destructive text-xs" role="alert">
						{versionError}
					</p>
				) : (
					!isCreate && <p className="text-muted-foreground text-xs">Bump major (2.x.x), minor (1.1.x), or patch (1.0.1).</p>
				)}
			</div>
			<div className="mt-4 flex justify-end gap-2">
				<Button variant="ghost" size="sm" onClick={onClose}>
					Cancel
				</Button>
				<Button size="sm" data-testid="skill-version-confirm-btn" disabled={!canSave} onClick={submit}>
					{isCreate ? "Create" : serve ? "Save & Serve" : "Save"}
				</Button>
			</div>
		</>
	);
}