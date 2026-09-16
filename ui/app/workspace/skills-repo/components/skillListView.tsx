"use client";

import PageTitle from "@/components/pageTitle";
import FullPageLoader from "@/components/fullPageLoader";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useGetCoreConfigQuery } from "@/lib/store";
import { getErrorMessage } from "@/lib/store/apis/baseApi";
import {
	useBumpAllSkillsVersionMutation,
	useDeleteSkillMutation,
	useGetAllSkillsVersionQuery,
	useListSkillsQuery,
} from "@/lib/store/apis/skillsApi";
import { AllSkillsVersionBump, SkillListItem } from "@/lib/types/skills";
import { cn } from "@/lib/utils";
import { getApiBaseUrl, getExampleBaseUrl } from "@/lib/utils/port";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import {
	ArrowDown,
	ArrowUp,
	ArrowUpDown,
	ArrowUpRight,
	BookOpenText,
	Check,
	ChevronDown,
	ChevronLeft,
	ChevronRight,
	Clipboard,
	Download,
	FileText,
	Info,
	Loader2,
	MoreHorizontal,
	Package,
	Plus,
	Search,
	Trash2,
} from "lucide-react";
import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";
import { PAGE_SIZE, formatDateShort, useDebouncedValue } from "./helpers";

const SKILLS_REPOSITORY_DOCS_URL = "https://docs.getbifrost.ai/features/skills-repository";

// ---------- MarketplacePopover ----------

function MarketplacePopover() {
	const { t } = useTranslation("config");
	const [copiedKey, setCopiedKey] = useState<string | null>(null);
	const [open, setOpen] = useState(false);
	const marketplaceBaseUrl = `${getExampleBaseUrl()}/api`;

	const items = [
		{
			key: "claude-desktop",
			label: "Claude Desktop / Cowork",
			value: `${marketplaceBaseUrl}/skills/serve/claude-code.git`,
			ariaLabel: t("skillsRepo.copyClaudeDesktop"),
		},
		{
			key: "claude",
			label: "Claude Code",
			value: `claude plugin marketplace add ${marketplaceBaseUrl}/skills/serve/claude-code/.claude-plugin/marketplace.json`,
			ariaLabel: t("skillsRepo.copyClaudeCode"),
		},
		{
			key: "codex",
			label: "Codex",
			value: `codex plugin marketplace add ${marketplaceBaseUrl}/skills/serve/codex`,
			ariaLabel: t("skillsRepo.copyCodex"),
		},
	];

	const handleCopy = (key: string, text: string) => {
		navigator.clipboard
			.writeText(text)
			.then(() => {
				setCopiedKey(key);
				setOpen(false);
				toast.success(t("skillsRepo.copied"));
				setTimeout(() => setCopiedKey(null), 2000);
			})
			.catch(() => {
				toast.error(t("skillsRepo.copyFailed"));
			});
	};

	return (
		<Popover open={open} onOpenChange={setOpen}>
			<PopoverTrigger asChild>
				<Button variant="outline" size="sm" title={t("skillsRepo.registerMarketplace")} aria-label={t("skillsRepo.registerMarketplace")}>
					<Package className="h-3.5 w-3.5" />
					<span className="hidden md:inline">{t("skillsRepo.registerMarketplace")}</span>
				</Button>
			</PopoverTrigger>
			<PopoverContent align="end" className="w-[calc(100vw-2rem)] max-w-md p-0 md:w-auto">
				<div className="border-b px-3 py-2">
					<p className="text-muted-foreground text-xs font-medium">{t("skillsRepo.marketplaceHint")}</p>
				</div>
				<div className="py-1">
					{items.map((item) => (
						<button
							key={item.key}
							data-testid={`skill-copy-marketplace-${item.key}`}
							className="hover:bg-muted/50 flex w-full cursor-pointer items-center gap-3 px-3 py-2 text-left transition-colors"
							aria-label={item.ariaLabel}
							onClick={() => handleCopy(item.key, item.value)}
						>
							<div className="min-w-0 flex-1">
								<p className="text-xs font-medium">{item.label}</p>
								<p className="text-muted-foreground mt-0.5 truncate font-mono text-xs">{item.value}</p>
							</div>
							{copiedKey === item.key ? (
								<Check className="h-3.5 w-3.5 shrink-0 text-green-500" />
							) : (
								<Clipboard className="text-muted-foreground h-3.5 w-3.5 shrink-0" />
							)}
						</button>
					))}
				</div>
			</PopoverContent>
		</Popover>
	);
}

// ---------- SortableHeader ----------

type SortColumn = "name" | "updated_at";
type SortOrder = "asc" | "desc";

function SortableHeader({
	column,
	label,
	sortBy,
	order,
	onToggle,
}: {
	column: SortColumn;
	label: string;
	sortBy: SortColumn | null;
	order: SortOrder;
	onToggle: (column: SortColumn) => void;
}) {
	const { t } = useTranslation("config");
	const isActive = sortBy === column;
	let Icon = ArrowUpDown;
	if (isActive && order === "desc") Icon = ArrowDown;
	else if (isActive) Icon = ArrowUp;
	return (
		<Button
			variant="ghost"
			onClick={() => onToggle(column)}
			className="!px-0"
			data-testid={`skill-sort-${column}`}
			aria-label={t("skillsRepo.sortBy", { label })}
		>
			{label}
			<Icon className={cn("h-4 w-4", isActive && "text-foreground")} />
		</Button>
	);
}

// ---------- SkillActionsMenu ----------

function SkillActionsMenu({
	skill,
	hasEditAccess,
	hasDeleteAccess,
	isDeleting,
	onEdit,
	onDelete,
}: {
	skill: SkillListItem;
	hasEditAccess: boolean;
	hasDeleteAccess: boolean;
	isDeleting: boolean;
	onEdit: (id: string) => void;
	onDelete: (id: string) => Promise<void>;
}) {
	const { t } = useTranslation("config");
	const { t: tc } = useTranslation("common");
	const [isOpen, setIsOpen] = useState(false);
	const [deleteOpen, setDeleteOpen] = useState(false);
	const [isDownloading, setIsDownloading] = useState(false);

	const handleDownload = async () => {
		setIsDownloading(true);
		let url: string | undefined;
		try {
			const res = await fetch(`${getApiBaseUrl()}/skills/serve/${encodeURIComponent(skill.name)}/download.zip`);
			if (!res.ok) throw new Error("Download failed");
			const blob = await res.blob();
			url = URL.createObjectURL(blob);
			const link = document.createElement("a");
			link.href = url;
			link.download = `${skill.name}.zip`;
			document.body.appendChild(link);
			link.click();
			document.body.removeChild(link);
		} catch {
			toast.error(t("skillsRepo.downloadFailed"));
		} finally {
			if (url) URL.revokeObjectURL(url);
			setIsDownloading(false);
		}
	};

	return (
		<>
			<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
				<DropdownMenuTrigger asChild>
					<Button
						variant="ghost"
						size="icon"
						className="h-8 w-8"
						data-testid={`skill-actions-menu-${skill.name}`}
						aria-label={`Actions for ${skill.name}`}
					>
						<MoreHorizontal className="h-4 w-4" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					<DropdownMenuItem
						className="cursor-pointer"
						data-testid={`skill-download-btn-${skill.name}`}
						disabled={isDownloading}
						onSelect={(e) => {
							e.preventDefault();
							handleDownload();
							setIsOpen(false);
						}}
					>
						{isDownloading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
						{isDownloading ? t("skillsRepo.downloading") : t("skillsRepo.downloadZip")}
					</DropdownMenuItem>
					<DropdownMenuItem
						variant="destructive"
						className="cursor-pointer"
						data-testid={`skill-delete-btn-${skill.name}`}
						disabled={!hasDeleteAccess || isDeleting}
						onSelect={(e) => {
							e.preventDefault();
							setDeleteOpen(true);
							setIsOpen(false);
						}}
					>
						<Trash2 className="h-4 w-4" />
						{tc("delete")}
					</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>

			<AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("skillsRepo.deleteTitle", { name: skill.name })}</AlertDialogTitle>
						<AlertDialogDescription>{t("skillsRepo.deleteConfirmation")}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel>{tc("cancel")}</AlertDialogCancel>
						<AlertDialogAction data-testid="skill-delete-confirm-btn" onClick={() => onDelete(skill.id)} disabled={isDeleting}>
							{isDeleting ? (
								<>
									<Loader2 className="h-3.5 w-3.5 animate-spin" /> {t("skillsRepo.deleting")}
								</>
							) : (
								t("skillsRepo.deleteSkill")
							)}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}

// ---------- SkillsListView ----------

export function SkillsListView({
	onSelectSkill,
	onCreateNew,
}: {
	onSelectSkill: (id: string, edit?: boolean) => void;
	onCreateNew: () => void;
}) {
	const { t: tc } = useTranslation("common");
	const { t } = useTranslation("config");
	const hasCreateAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.Create);
	const hasEditAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.SkillsRepository, RbacOperation.Delete);
	const { data: bifrostConfig } = useGetCoreConfigQuery({});
	const isGitAvailable = bifrostConfig?.is_git_available ?? false;
	const [deleteSkill, { isLoading: isDeleting }] = useDeleteSkillMutation();
	const { data: allSkillsVersionData, refetch: refetchAllSkillsVersion } = useGetAllSkillsVersionQuery();
	const [bumpAllSkillsVersion, { isLoading: isBumpingAllSkillsVersion }] = useBumpAllSkillsVersionMutation();

	const [isDownloadingAll, setIsDownloadingAll] = useState(false);
	const [search, setSearch] = useState("");
	const debouncedSearch = useDebouncedValue(search, 300);
	const [offset, setOffset] = useState(0);
	const [sortBy, setSortBy] = useState<SortColumn | null>(null);
	const [sortOrder, setSortOrder] = useState<SortOrder>("asc");

	const { data, isLoading, isFetching, isError, refetch } = useListSkillsQuery({
		limit: PAGE_SIZE,
		offset,
		search: debouncedSearch || undefined,
		sort_by: sortBy || undefined,
		order: sortBy ? sortOrder : undefined,
	});

	const skills = data?.skills || [];
	const total = data?.total || 0;
	const isSearchSettling = search !== debouncedSearch;

	const toggleSort = (column: SortColumn) => {
		setOffset(0);
		if (sortBy === column) {
			if (sortOrder === "asc") {
				setSortOrder("desc");
			} else {
				setSortBy(null);
				setSortOrder("asc");
			}
		} else {
			setSortBy(column);
			setSortOrder("asc");
		}
	};

	const handleDeleteSkill = async (id: string) => {
		try {
			await deleteSkill(id).unwrap();
			toast.success(t("skillsRepo.toastDeleted"));
		} catch (err: unknown) {
			toast.error(t("skillsRepo.toastDeleteFailed"), {
				description: getErrorMessage(err),
			});
		}
	};

	const handleBumpAllSkillsVersion = async (bump: AllSkillsVersionBump) => {
		try {
			const result = await bumpAllSkillsVersion({ bump }).unwrap();
			toast.success(t("skillsRepo.toastBumpSuccess", { version: result.version }));
			refetchAllSkillsVersion();
		} catch (err: unknown) {
			toast.error(t("skillsRepo.toastBumpFailed"), {
				description: getErrorMessage(err),
			});
		}
	};

	if (isLoading) {
		return <FullPageLoader />;
	}

	if (isError) {
		return (
			<div className="flex flex-col items-center justify-center gap-3 py-20">
				<p className="text-muted-foreground text-sm">{t("skillsRepo.loadFailed")}</p>
				<Button variant="outline" size="sm" onClick={refetch}>
					{tc("retry")}
				</Button>
			</div>
		);
	}

	// True empty state: no skills at all (not just filtered to zero)
	if (total === 0 && !search && !debouncedSearch && !isFetching) {
		return (
			<div
				className="flex h-full w-full flex-col items-center justify-center gap-4 px-2 py-10 text-center md:px-0 md:py-0"
				data-testid="skills-repo-empty-state"
			>
				<div className="text-muted-foreground">
					<BookOpenText className="h-24 w-24" strokeWidth={1} />
				</div>
				<div className="flex flex-col gap-1">
					<h1 className="text-muted-foreground text-xl font-medium">{t("skillsRepo.title")}</h1>
					<div className="text-muted-foreground mx-auto mt-2 max-w-xl text-sm font-normal">{t("skillsRepo.emptyDescription")}</div>
					<div className="mx-auto mt-6 flex flex-row flex-wrap items-center justify-center gap-2">
						<Button
							variant="outline"
							aria-label={t("skillsRepo.readMoreAria")}
							data-testid="skills-button-read-more"
							onClick={() => {
								window.open(`${SKILLS_REPOSITORY_DOCS_URL}?utm_source=bfd`, "_blank", "noopener,noreferrer");
							}}
						>
							{t("enterprise.readMore")} <ArrowUpRight className="text-muted-foreground h-3 w-3" />
						</Button>
						{hasCreateAccess && (
							<Button aria-label={t("skillsRepo.createFirstAria")} data-testid="skill-create-btn" onClick={onCreateNew}>
								<Plus className="h-4 w-4" />
								{t("skillsRepo.createSkill")}
							</Button>
						)}
					</div>
				</div>
			</div>
		);
	}

	return (
		<div className="flex w-full min-w-0 flex-1 flex-col">
			{/* Header */}
			{/* Search + All-skills version + Actions */}
			<div className="mb-4 flex shrink-0 flex-col gap-3 md:flex-row md:items-center">
				<PageTitle title={t("skillsRepo.title")} beta>
					{t("skillsRepo.description")}
				</PageTitle>
				<div className="relative w-full flex-1 md:max-w-sm">
					<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
					<Input
						data-testid="skill-search-input"
						aria-label={t("skillsRepo.searchAria")}
						placeholder={t("skillsRepo.searchPlaceholder")}
						value={search}
						onChange={(e) => {
							setSearch(e.target.value);
							setOffset(0);
						}}
						className="h-8 pl-9"
					/>
				</div>
				<div className="flex min-w-0 items-center gap-2 text-xs">
					<Tooltip>
						<TooltipTrigger asChild>
							<Info className="text-muted-foreground h-3.5 w-3.5 cursor-help" />
						</TooltipTrigger>
						<TooltipContent side="bottom" className="max-w-xs text-xs">
							{t("skillsRepo.allSkillsHelp")}
						</TooltipContent>
					</Tooltip>
					<span className="text-muted-foreground whitespace-nowrap">{t("skillsRepo.allSkillsVersion")}</span>
					{hasEditAccess ? (
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<button
									data-testid="skill-bump-version-btn"
									disabled={isBumpingAllSkillsVersion}
									className="cursor-pointer disabled:cursor-not-allowed disabled:opacity-50"
								>
									<Badge variant="secondary" className="hover:bg-muted font-mono text-xs transition-colors">
										{isBumpingAllSkillsVersion ? (
											<Loader2 className="h-3 w-3 animate-spin" />
										) : (
											<>
												{allSkillsVersionData?.version ?? "0.0.0"}
												<ChevronDown className="h-3 w-3" />
											</>
										)}
									</Badge>
								</button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end">
								{(["patch", "minor", "major"] as AllSkillsVersionBump[]).map((bump) => (
									<DropdownMenuItem
										key={bump}
										className="cursor-pointer capitalize"
										disabled={isBumpingAllSkillsVersion}
										onSelect={() => handleBumpAllSkillsVersion(bump)}
									>
										{t("skillsRepo.bumpVersion", { version: bump })}
									</DropdownMenuItem>
								))}
							</DropdownMenuContent>
						</DropdownMenu>
					) : (
						<Badge variant="secondary" className="font-mono text-xs">
							{allSkillsVersionData?.version ?? "0.0.0"}
						</Badge>
					)}
				</div>
				<div className="flex shrink-0 items-center gap-2 md:ml-auto">
					<div className="grid grid-cols-3 items-center gap-2 md:flex">
						{isGitAvailable ? (
							<MarketplacePopover />
						) : (
							<Tooltip>
								<TooltipTrigger asChild>
									<span tabIndex={0}>
										<Button
											variant="outline"
											size="sm"
											disabled
											title={t("skillsRepo.registerMarketplace")}
											aria-label={t("skillsRepo.registerMarketplace")}
										>
											<Package className="h-3.5 w-3.5" />
											<span className="hidden md:inline">{t("skillsRepo.registerMarketplace")}</span>
										</Button>
									</span>
								</TooltipTrigger>
								<TooltipContent side="bottom">
									<p className="max-w-xs text-xs">{t("skillsRepo.gitUnavailable")}</p>
								</TooltipContent>
							</Tooltip>
						)}
						<Button
							variant="outline"
							size="sm"
							data-testid="skill-download-all-btn"
							onClick={async () => {
								setIsDownloadingAll(true);
								try {
									const res = await fetch(`${getApiBaseUrl()}/skills/serve/all/download.zip`);
									if (!res.ok) throw new Error("Download failed");
									const blob = await res.blob();
									const url = URL.createObjectURL(blob);
									const link = document.createElement("a");
									link.href = url;
									link.download = "all-skills.zip";
									document.body.appendChild(link);
									link.click();
									document.body.removeChild(link);
									URL.revokeObjectURL(url);
								} catch {
									toast.error(t("skillsRepo.downloadAllFailed"));
								} finally {
									setIsDownloadingAll(false);
								}
							}}
							disabled={!skills?.length || isDownloadingAll}
							title={t("skillsRepo.downloadAllAria")}
							aria-label={t("skillsRepo.downloadAllAria")}
						>
							{isDownloadingAll ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
							<span className="hidden md:inline">{isDownloadingAll ? t("skillsRepo.downloading") : t("skillsRepo.downloadAll")}</span>
						</Button>
						{hasCreateAccess && (
							<Button
								data-testid="skill-create-btn"
								onClick={onCreateNew}
								size="sm"
								title={t("skillsRepo.newSkill")}
								aria-label={t("skillsRepo.newSkill")}
							>
								<Plus className="h-4 w-4" />
								<span className="hidden md:inline">{t("skillsRepo.newSkill")}</span>
							</Button>
						)}
					</div>
				</div>
			</div>

			{/* Table */}
			<div className="mb-2 min-h-80 grow overflow-hidden rounded-sm border md:min-h-0">
				<Table containerClassName="h-full overflow-auto" className="min-w-[58rem] table-fixed md:w-full md:min-w-0">
					<TableHeader className="bg-muted sticky top-0 z-20">
						<TableRow className="hover:bg-transparent">
							<TableHead className="w-60">
								<SortableHeader column="name" label={t("skillsRepo.name")} sortBy={sortBy} order={sortOrder} onToggle={toggleSort} />
							</TableHead>
							<TableHead>{t("skillsRepo.descriptionColumn")}</TableHead>
							<TableHead className="w-36">{t("skillsRepo.version")}</TableHead>
							<TableHead className="w-36">{t("skillsRepo.files")}</TableHead>
							<TableHead className="w-44">
								<SortableHeader
									column="updated_at"
									label={t("skillsRepo.updated")}
									sortBy={sortBy}
									order={sortOrder}
									onToggle={toggleSort}
								/>
							</TableHead>
							<TableHead className={`bg-muted sticky right-0 z-30 w-14 text-right ${PIN_SHADOW_RIGHT}`}>
								<span className="sr-only">{t("skillsRepo.actions")}</span>
							</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{skills.length === 0 && !isSearchSettling && !isFetching ? (
							<TableRow>
								<TableCell colSpan={6} className="py-12 text-center">
									<div className="flex flex-col items-center gap-2">
										<FileText className="text-muted-foreground h-8 w-8" />
										<p className="text-muted-foreground text-sm">{search ? t("skillsRepo.emptySearch") : t("skillsRepo.empty")}</p>
										{!search && hasCreateAccess && (
											<Button variant="outline" size="sm" onClick={onCreateNew} className="mt-2">
												<Plus className="h-3.5 w-3.5" />
												{t("skillsRepo.createFirstAria")}
											</Button>
										)}
									</div>
								</TableCell>
							</TableRow>
						) : (
							skills.map((skill) => {
								const fileCount = skill.file_count ?? 0;

								return (
									<TableRow
										key={skill.id}
										data-testid={`skill-row-${skill.name}`}
										className="group hover:bg-muted/50 cursor-pointer transition-colors"
										tabIndex={0}
										onClick={() => onSelectSkill(skill.id)}
										onKeyDown={(e) => {
											if (e.key === "Enter" || e.key === " ") {
												e.preventDefault();
												onSelectSkill(skill.id);
											}
										}}
									>
										<TableCell className="w-60 max-w-60 overflow-hidden text-sm font-medium">
											<div className="max-w-full truncate" title={skill.name}>
												{skill.name}
											</div>
										</TableCell>
										<TableCell className="text-muted-foreground overflow-hidden text-sm">
											<div className="truncate" title={skill.description}>
												{skill.description}
											</div>
										</TableCell>
										<TableCell>
											<Badge variant="secondary" className="px-2.5 py-1 text-xs">
												{skill.latest_version}
											</Badge>
										</TableCell>
										<TableCell>
											<span className="text-muted-foreground text-xs">
												<Trans
													ns="config"
													i18nKey="skillsRepo.fileCount"
													count={fileCount}
													components={{ count: <span className="text-foreground" /> }}
												/>
											</span>
										</TableCell>
										<TableCell className="text-muted-foreground text-sm">{formatDateShort(skill.updated_at)}</TableCell>
										<TableCell
											className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-20 bg-white text-right ${PIN_SHADOW_RIGHT}`}
											onClick={(e) => e.stopPropagation()}
										>
											<SkillActionsMenu
												skill={skill}
												hasEditAccess={hasEditAccess}
												hasDeleteAccess={hasDeleteAccess}
												isDeleting={isDeleting}
												onEdit={(id) => onSelectSkill(id, true)}
												onDelete={handleDeleteSkill}
											/>
										</TableCell>
									</TableRow>
								);
							})
						)}
					</TableBody>
				</Table>
			</div>

			{/* Pagination */}
			{total > 0 && (
				<div className="flex shrink-0 flex-col gap-2 text-xs md:flex-row md:items-center md:justify-between md:gap-0">
					<div className="text-muted-foreground flex items-center gap-2">
						{t("skillsRepo.entryRange", {
							start: (offset + 1).toLocaleString(),
							end: Math.min(offset + PAGE_SIZE, total).toLocaleString(),
							total: total.toLocaleString(),
						})}
					</div>
					<div className="flex items-center gap-2">
						<Button
							variant="ghost"
							size="sm"
							data-testid="skill-pagination-prev"
							onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
							disabled={offset === 0 || isFetching}
							aria-label={t("skillsRepo.previousPage")}
						>
							<ChevronLeft className="size-3" />
						</Button>
						<div className="flex items-center gap-1">
							{t("skillsRepo.pageCount", { page: Math.floor(offset / PAGE_SIZE) + 1, total: Math.ceil(total / PAGE_SIZE) })}
						</div>
						<Button
							variant="ghost"
							size="sm"
							data-testid="skill-pagination-next"
							onClick={() => setOffset(offset + PAGE_SIZE)}
							disabled={offset + PAGE_SIZE >= total || isFetching}
							aria-label={t("skillsRepo.nextPage")}
						>
							<ChevronRight className="size-3" />
						</Button>
					</div>
				</div>
			)}
		</div>
	);
}