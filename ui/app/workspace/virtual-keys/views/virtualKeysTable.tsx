import PageTitle from "@/components/pageTitle";
import { DisabledReasonMenuItem } from "@/components/ui/disabledReason";
import { BudgetDisplay } from "@/components/budgetDisplay";
import { CustomerSelector } from "@/components/entitySelectors/customerSelector";
import { TeamSelector } from "@/components/entitySelectors/teamSelector";
import { RateLimitDisplay } from "@/components/rateLimitDisplay";
import { PIN_SHADOW_RIGHT } from "@/components/table/columnPinning";
import { TruncatedBadge } from "@/components/truncatedBadge";
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
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { resetDurationLabels } from "@/lib/constants/governance";
import { getUserPicker } from "@/lib/registries/userPicker";
import { useVirtualKeyAccessAudit } from "@enterprise/lib/hooks/useVirtualKeyAccessAudit";
import {
	getErrorMessage,
	useBulkRotateVirtualKeysMutation,
	useDeleteVirtualKeyMutation,
	useGetCoreConfigQuery,
	useGetVirtualKeyQuery,
	useLazyGetVirtualKeysQuery,
	useUpdateVirtualKeyMutation,
} from "@/lib/store";
import { VirtualKey } from "@/lib/types/governance";
import { cn } from "@/lib/utils";
import { actionDisabledReason, formatCurrency, getEffectiveBudgetLimit } from "@/lib/utils/governance";
import { virtualKeyMetadataKeyError } from "@/lib/utils/virtualKeyMetadata";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import {
	ArrowDown,
	ArrowUp,
	ArrowUpDown,
	Building2,
	ChevronLeft,
	ChevronRight,
	Copy,
	Download,
	Edit,
	Eye,
	EyeOff,
	Loader2,
	MoreHorizontal,
	Plus,
	RotateCcw,
	ScrollText,
	Search,
	ShieldCheck,
	Tag,
	Trash2,
	UserRound,
	Users,
	X,
} from "lucide-react";
import { useQueryState } from "nuqs";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { useVirtualKeyUsage } from "../hooks/useVirtualKeyUsage";
import VirtualKeyDetailSheet from "./virtualKeyDetailsSheet";
import { VirtualKeysEmptyState } from "./virtualKeysEmptyState";
import VirtualKeySheet from "./virtualKeySheet";
import { assignedToLabel, csvAssignedToCell, latestGraceDeadline } from "./virtualKeysTable.utils";

// Registers the enterprise user picker as a side effect; a no-op in OSS builds,
// where the user filter stays hidden because no picker is registered.
import "@enterprise/lib/registrations/userPicker";
import { useTranslation } from "react-i18next";
import i18n from "@/lib/i18n";
import { useActionParam } from "@/hooks/useActionParam";

const formatResetDuration = (duration: string) =>
	i18n.t(`virtualKeys.resetDurations.${duration}`, { ns: "models", defaultValue: resetDurationLabels[duration] || duration });

type ExportScope = "current_page" | "all";

function virtualKeysToCSV(vks: VirtualKey[]): string {
	const t = i18n.t.bind(i18n);
	const headers = [
		t("virtualKeys.name", { ns: "models" }),
		t("virtualKeys.status", { ns: "models" }),
		t("virtualKeys.assignedTo", { ns: "models" }),
		t("virtualKeys.assignedUserEmail", { ns: "models" }),
		t("virtualKeys.budgetLimit", { ns: "models" }),
		t("virtualKeys.budgetSpent", { ns: "models" }),
		t("virtualKeys.budgetReset", { ns: "models" }),
		t("virtualKeys.description", { ns: "models" }),
		t("virtualKeys.createdAt", { ns: "models" }),
	];
	const rows = vks.map((vk) => {
		const isExhausted =
			vk.budgets?.some((b) => b.current_usage >= getEffectiveBudgetLimit(b)) ||
			(vk.rate_limit?.token_current_usage &&
				vk.rate_limit?.token_max_limit &&
				vk.rate_limit.token_current_usage >= vk.rate_limit.token_max_limit) ||
			(vk.rate_limit?.request_current_usage &&
				vk.rate_limit?.request_max_limit &&
				vk.rate_limit.request_current_usage >= vk.rate_limit.request_max_limit);
		const isExpired = !!vk.expires_at && Date.now() >= new Date(vk.expires_at).getTime();
		const status = !vk.is_active
			? t("virtualKeys.inactive", { ns: "models" })
			: isExpired
				? t("virtualKeys.expired", { ns: "models" })
				: isExhausted
					? t("virtualKeys.exhausted", { ns: "models" })
					: t("virtualKeys.active", { ns: "models" });
		const assignedTo = csvAssignedToCell(vk);
		const budgetLimit = vk.budgets?.length ? vk.budgets.map((b) => formatCurrency(getEffectiveBudgetLimit(b))).join("; ") : "";
		const budgetSpent = vk.budgets?.length ? vk.budgets.map((b) => formatCurrency(b.current_usage)).join("; ") : "";
		const budgetReset = vk.budgets?.length ? vk.budgets.map((b) => formatResetDuration(b.reset_duration)).join("; ") : "";
		return [
			vk.name,
			status,
			assignedTo,
			vk.assigned_user?.email ?? "",
			budgetLimit,
			budgetSpent,
			budgetReset,
			vk.description || "",
			vk.created_at,
		];
	});
	return [headers, ...rows].map((row) => row.map((cell) => `"${String(cell).replace(/"/g, '""')}"`).join(",")).join("\n");
}

function downloadCSV(content: string) {
	const blob = new Blob([content], { type: "text/csv;charset=utf-8;" });
	const url = URL.createObjectURL(blob);
	const link = document.createElement("a");
	link.href = url;
	link.download = `virtual-keys-${new Date().toISOString().split("T")[0]}.csv`;
	link.click();
	URL.revokeObjectURL(url);
}

function VKBudgetCell({ vk }: { vk: VirtualKey }) {
	const { displayBudgets } = useVirtualKeyUsage(vk);
	return <BudgetDisplay budgets={displayBudgets} calendarAligned={vk.calendar_aligned} />;
}

// Entity selectors only ever set a value, so a filter built on one needs its own
// reset back to "all" — this restores the affordance ComboboxSelect gave for free.
// Filters show only their icon until the toolbar has room for the full labelled dropdown.
const FILTER_WRAPPER_CLASS =
	"flex shrink-0 items-center gap-1 @6xl/vk-toolbar:max-w-[250px] @6xl/vk-toolbar:min-w-[150px] @6xl/vk-toolbar:shrink @6xl/vk-toolbar:grow @6xl/vk-toolbar:basis-[150px]";
const FILTER_SELECTOR_CLASS = "w-auto min-w-0 @6xl/vk-toolbar:w-full";
const FILTER_TRIGGER_CLASS =
	"size-9 justify-center px-0 [&_[data-slot=entity-selector-chevron]]:hidden [&_[data-slot=entity-selector-label]]:sr-only @6xl/vk-toolbar:w-full @6xl/vk-toolbar:justify-between @6xl/vk-toolbar:px-3 @6xl/vk-toolbar:[&_[data-slot=entity-selector-chevron]]:block @6xl/vk-toolbar:[&_[data-slot=entity-selector-label]]:not-sr-only";
const FILTER_CONTENT_CLASS = "w-(--radix-popover-trigger-width) min-w-64";
const FILTER_ICON_CLASS = "size-4 shrink-0 @6xl/vk-toolbar:hidden";

function FilterClearButton({
	show,
	label,
	onClear,
	"data-testid": dataTestId,
}: {
	show: boolean;
	label: string;
	onClear: () => void;
	"data-testid"?: string;
}) {
	if (!show) return null;
	return (
		<Button
			type="button"
			variant="ghost"
			size="icon"
			className="h-9 w-7 shrink-0"
			aria-label={label}
			onClick={onClear}
			data-testid={dataTestId}
		>
			<X className="h-3.5 w-3.5" />
		</Button>
	);
}

// MetadataFilter narrows the list to keys whose metadata has one key set to exactly one value. The
// key is checked against the server's rule before it is applied, since the API refuses a bad key.
function MetadataFilter({
	metadataKey,
	metadataValue,
	onChange,
}: {
	metadataKey: string;
	metadataValue: string;
	onChange: (key: string, value: string) => void;
}) {
	const { t } = useTranslation("models");
	const [open, setOpen] = useState(false);
	const [draftKey, setDraftKey] = useState(metadataKey);
	const [draftValue, setDraftValue] = useState(metadataValue);
	const keyError = draftKey ? virtualKeyMetadataKeyError(draftKey) : undefined;
	const active = !!metadataKey;
	const apply = () => {
		if (!draftKey || keyError) return;
		onChange(draftKey, draftValue);
		setOpen(false);
	};
	return (
		<Popover
			open={open}
			onOpenChange={(next) => {
				if (next) {
					setDraftKey(metadataKey);
					setDraftValue(metadataValue);
				}
				setOpen(next);
			}}
		>
			<PopoverTrigger asChild>
				<Button
					type="button"
					variant="outline"
					className="h-9 max-w-[250px] gap-2 px-3"
					aria-label={
						active
							? t("virtualKeys.metadataFilter.activeAria", { key: metadataKey, value: metadataValue })
							: t("virtualKeys.metadataFilter.filterAria")
					}
					data-testid="vk-metadata-filter-trigger"
				>
					<Tag className="size-4 shrink-0" />
					<span className="truncate text-sm font-normal">
						{active ? `${metadataKey}=${metadataValue}` : t("virtualKeys.metadataFilter.label")}
					</span>
				</Button>
			</PopoverTrigger>
			<PopoverContent className="w-72 space-y-3" align="start">
				<form
					className="space-y-3"
					onSubmit={(e) => {
						e.preventDefault();
						apply();
					}}
				>
					<div className="space-y-1.5">
						<Label htmlFor="vk-metadata-filter-key">{t("virtualKeys.key")}</Label>
						<Input
							id="vk-metadata-filter-key"
							placeholder={t("virtualKeys.metadataFilter.keyPlaceholder")}
							value={draftKey}
							onChange={(e) => setDraftKey(e.target.value)}
							data-testid="vk-metadata-filter-key-input"
						/>
						{keyError && <p className="text-destructive text-xs">{keyError}</p>}
					</div>
					<div className="space-y-1.5">
						<Label htmlFor="vk-metadata-filter-value">{t("virtualKeys.metadataFilter.value")}</Label>
						<Input
							id="vk-metadata-filter-value"
							placeholder={t("virtualKeys.metadataFilter.valuePlaceholder")}
							value={draftValue}
							onChange={(e) => setDraftValue(e.target.value)}
							data-testid="vk-metadata-filter-value-input"
						/>
					</div>
					<Button type="submit" size="sm" className="w-full" disabled={!draftKey || !!keyError} data-testid="vk-metadata-filter-apply-btn">
						{t("virtualKeys.metadataFilter.apply")}
					</Button>
				</form>
			</PopoverContent>
		</Popover>
	);
}

function VKAssignedToCell({ vk }: { vk: VirtualKey }) {
	// A resolved row (assigned_user present, user or null) is read straight off the row:
	// the list endpoint resolves the whole page at once, so the hook skips its request and
	// this costs nothing per row. Only a row whose assignee could not be resolved upstream
	// falls back to the per-key lookup, so "-" never stands in for "we do not know".
	const { assignedUsers } = useVirtualKeyUsage(vk);
	const label = assignedToLabel({ ...vk, assigned_user: assignedUsers[0] ?? null });

	if (!label) {
		return <span className="text-muted-foreground max-w-full truncate text-left text-sm">-</span>;
	}

	return <TruncatedBadge label={label} dataTestId={`vk-assigned-to-tooltip-${vk.name}`} />;
}

function VKRateLimitCell({ vk }: { vk: VirtualKey }) {
	const { displayRateLimit } = useVirtualKeyUsage(vk);
	return <RateLimitDisplay rateLimits={displayRateLimit} calendarAligned={vk.calendar_aligned} />;
}

function VKActiveSwitch({
	vk,
	hasUpdateAccess,
	onToggle,
}: {
	vk: VirtualKey;
	hasUpdateAccess: boolean;
	onToggle: (vk: VirtualKey, checked: boolean) => Promise<void>;
}) {
	const { t } = useTranslation("models");
	const { isUserProfileManaged } = useVirtualKeyUsage(vk);
	// A user's managed key can't be toggled here even by a caller who does have update access. A
	// key its owner's access profile governs is still the owner's key, toggled like any other.
	const disabledReason = isUserProfileManaged
		? "This virtual key is managed by an access profile. Enable or disable it from the profile."
		: !hasUpdateAccess
			? "You don't have permission to update virtual keys."
			: undefined;

	const control = (
		<Switch
			checked={vk.is_active}
			disabled={!!disabledReason}
			aria-label={vk.is_active ? t("virtualKeys.disableAria", { name: vk.name }) : t("virtualKeys.enableAria", { name: vk.name })}
			data-testid={`vk-active-switch-${vk.name}`}
			onAsyncCheckedChange={(checked) => onToggle(vk, checked)}
		/>
	);

	if (!disabledReason) return control;

	// A disabled control emits no pointer events, so the tooltip has to hang off a wrapper.
	// tabIndex keeps the reason reachable by keyboard, since the switch itself is unfocusable.
	return (
		<Tooltip>
			<TooltipTrigger asChild>
				<div tabIndex={0} className="inline-flex" data-testid={`vk-active-switch-tooltip-trigger-${vk.name}`}>
					{control}
				</div>
			</TooltipTrigger>
			<TooltipContent data-testid={`vk-active-switch-tooltip-content-${vk.name}`}>{disabledReason}</TooltipContent>
		</Tooltip>
	);
}

function VKActionsMenu({
	vk,
	hasUpdateAccess,
	hasDeleteAccess,
	isDeleting,
	onEdit,
	onDelete,
}: {
	vk: VirtualKey;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	isDeleting: boolean;
	onEdit: (vk: VirtualKey) => void;
	onDelete: (vkId: string) => void;
}) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	const [isOpen, setIsOpen] = useState(false);
	// Only a user's managed key is locked against delete; a key its owner's profile governs is the
	// owner's key, deleted like any other.
	const { isUserProfileManaged } = useVirtualKeyUsage(vk);
	const [deleteOpen, setDeleteOpen] = useState(false);

	return (
		<>
			<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
				<DropdownMenuTrigger asChild>
					<Button
						variant="ghost"
						size="icon"
						className="h-8 w-8"
						aria-label={t("virtualKeys.actionsAria")}
						data-testid={`vk-actions-btn-${vk.name}`}
					>
						<MoreHorizontal className="h-4 w-4" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					<DisabledReasonMenuItem
						reason={actionDisabledReason(hasUpdateAccess, "edit", "virtual keys")}
						className="cursor-pointer"
						data-testid={`vk-edit-btn-${vk.name}`}
						onSelect={(e) => {
							e.preventDefault();
							onEdit(vk);
							setIsOpen(false);
						}}
					>
						<Edit className="h-4 w-4" />
						{tc("edit")}
					</DisabledReasonMenuItem>
					<DropdownMenuItem asChild className="cursor-pointer" data-testid={`vk-view-logs-btn-${vk.name}`}>
						<Link to="/workspace/logs" search={{ virtual_key_ids: [vk.id] }} onClick={() => setIsOpen(false)}>
							<ScrollText className="h-4 w-4" />
							{t("virtualKeys.viewLogs")}
						</Link>
					</DropdownMenuItem>
					<DisabledReasonMenuItem
						reason={actionDisabledReason(
							hasDeleteAccess,
							"delete",
							"virtual keys",
							isUserProfileManaged ? t("virtualKeys.managedCantDelete") : undefined,
						)}
						variant="destructive"
						className="cursor-pointer"
						data-testid={`vk-delete-btn-${vk.name}`}
						onSelect={(e) => {
							e.preventDefault();
							setDeleteOpen(true);
							setIsOpen(false);
						}}
					>
						<Trash2 className="h-4 w-4" />
						{tc("delete")}
					</DisabledReasonMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
			<AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("virtualKeys.deleteTitle")}</AlertDialogTitle>
						<AlertDialogDescription>
							{t("virtualKeys.deleteConfirm", {
								name: vk.name.length > 20 ? `${vk.name.slice(0, 20)}...` : vk.name,
							})}
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid={`vk-delete-cancel-${vk.name}`}>{tc("cancel")}</AlertDialogCancel>
						<AlertDialogAction
							onClick={() => onDelete(vk.id)}
							disabled={isDeleting}
							className="bg-destructive hover:bg-destructive/90"
							data-testid={`vk-delete-confirm-${vk.name}`}
						>
							{isDeleting ? t("virtualKeys.deleting") : tc("delete")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>
		</>
	);
}

interface VirtualKeysTableProps {
	virtualKeys: VirtualKey[];
	totalCount: number;
	search: string;
	debouncedSearch: string;
	onSearchChange: (value: string) => void;
	customerFilter: string;
	onCustomerFilterChange: (value: string) => void;
	teamFilter: string;
	onTeamFilterChange: (value: string) => void;
	userFilter: string;
	onUserFilterChange: (value: string) => void;
	metadataFilterKey: string;
	metadataFilterValue: string;
	onMetadataFilterChange: (key: string, value: string) => void;
	offset: number;
	limit: number;
	onOffsetChange: (offset: number) => void;
	sortBy?: string;
	order?: string;
	onSortChange: (sortBy: string, order: string) => void;
	selectedVkId: string;
	onSelectedVkChange: (id: string, options?: { offset?: number }) => void;
}

export default function VirtualKeysTable({
	virtualKeys,
	totalCount,
	search,
	debouncedSearch,
	onSearchChange,
	customerFilter,
	onCustomerFilterChange,
	teamFilter,
	onTeamFilterChange,
	userFilter,
	onUserFilterChange,
	metadataFilterKey,
	metadataFilterValue,
	onMetadataFilterChange,
	offset,
	limit,
	onOffsetChange,
	sortBy,
	order,
	onSortChange,
	selectedVkId,
	onSelectedVkChange,
}: VirtualKeysTableProps) {
	const { t } = useTranslation("models");
	const { t: tc } = useTranslation("common");
	// The metadata filter as the list endpoint takes it; one key/value pair in this UI.
	const metadataFilterParam = metadataFilterKey ? { [metadataFilterKey]: metadataFilterValue } : undefined;
	const [showVirtualKeySheet, setShowVirtualKeySheet] = useState(false);
	const [editingVirtualKeyId, setEditingVirtualKeyId] = useState<string | null>(null);
	// Keys without their own delete_after_expire follow this client-wide setting.
	const { data: coreConfig } = useGetCoreConfigQuery({ fromDB: true });
	const deleteExpiredByDefault = coreConfig?.client_config?.delete_expired_virtual_keys ?? false;
	const [revealedKeys, setRevealedKeys] = useState<Set<string>>(new Set());
	const [showExportDialog, setShowExportDialog] = useState(false);
	const [exportScope, setExportScope] = useState<ExportScope>("current_page");
	const [exportMaxLimit, setExportMaxLimit] = useState("");
	const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
	const [showBulkRotateDialog, setShowBulkRotateDialog] = useState(false);
	const [fetchVirtualKeys, { isFetching: isExporting }] = useLazyGetVirtualKeysQuery();

	// Derive objects from props so they stay in sync with RTK cache updates
	const editingVirtualKey = useMemo(
		() => (editingVirtualKeyId ? (virtualKeys.find((vk) => vk.id === editingVirtualKeyId) ?? null) : null),
		[editingVirtualKeyId, virtualKeys],
	);
	const selectedVkInList = useMemo(
		() => (selectedVkId ? (virtualKeys.find((vk) => vk.id === selectedVkId) ?? null) : null),
		[selectedVkId, virtualKeys],
	);
	// Deep-link support: another page (e.g. Model Limits) can open a VK via ?vk=<id>.
	// The target may not be on the current page/filter, so fetch it by id as a fallback.
	const [vkParam, setVkParam] = useQueryState("vk");
	const needsVkFetch = !!selectedVkId && !selectedVkInList;
	const { data: fetchedVkData } = useGetVirtualKeyQuery(selectedVkId ?? "", {
		skip: !needsVkFetch,
	});
	const selectedVirtualKey = selectedVkInList ?? (needsVkFetch ? (fetchedVkData?.virtual_key ?? null) : null);

	useEffect(() => {
		if (!vkParam) return;
		onSelectedVkChange(vkParam);
		setVkParam(null); // consume the param; selection is held in parent state from here
	}, [vkParam, setVkParam, onSelectedVkChange]);

	const hasCreateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Delete);

	const [deleteVirtualKey, { isLoading: isDeleting }] = useDeleteVirtualKeyMutation();
	const [updateVirtualKey] = useUpdateVirtualKeyMutation();
	const [bulkRotateVirtualKeys, { isLoading: isBulkRotating }] = useBulkRotateVirtualKeysMutation();

	const visibleIds = useMemo(() => virtualKeys.map((vk) => vk.id), [virtualKeys]);
	const selectedVisibleIds = useMemo(() => visibleIds.filter((id) => selectedIds.has(id)), [selectedIds, visibleIds]);
	const selectedCount = selectedIds.size;
	const allVisibleSelected = visibleIds.length > 0 && selectedVisibleIds.length === visibleIds.length;
	const someVisibleSelected = selectedVisibleIds.length > 0 && selectedVisibleIds.length < visibleIds.length;

	const toggleSelectAllVisible = (checked: boolean) => {
		setSelectedIds((prev) => {
			const next = new Set(prev);
			for (const id of visibleIds) {
				if (checked) {
					next.add(id);
				} else {
					next.delete(id);
				}
			}
			return next;
		});
	};

	const toggleSelectVirtualKey = (vkId: string, checked: boolean) => {
		setSelectedIds((prev) => {
			const next = new Set(prev);
			if (checked) {
				next.add(vkId);
			} else {
				next.delete(vkId);
			}
			return next;
		});
	};

	const handleDelete = async (vkId: string) => {
		try {
			await deleteVirtualKey(vkId).unwrap();
			toast.success(t("virtualKeys.deletedSuccess"));
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleToggleActive = async (vk: VirtualKey, checked: boolean) => {
		try {
			await updateVirtualKey({
				vkId: vk.id,
				data: { is_active: checked },
			}).unwrap();
			toast.success(checked ? t("virtualKeys.toggledEnabled") : t("virtualKeys.toggledDisabled"));
		} catch (error) {
			toast.error(getErrorMessage(error));
			throw error;
		}
	};

	const handleBulkRotate = async () => {
		const ids = Array.from(selectedIds);
		if (ids.length === 0) return;

		try {
			const result = await bulkRotateVirtualKeys({ ids }).unwrap();
			const rotatedIds = new Set(result.virtual_keys.map((vk) => vk.id));
			setSelectedIds((prev) => {
				const next = new Set(prev);
				for (const id of rotatedIds) {
					next.delete(id);
				}
				return next;
			});
			setRevealedKeys((prev) => {
				const next = new Set(prev);
				for (const id of rotatedIds) {
					next.delete(id);
				}
				return next;
			});
			setShowBulkRotateDialog(false);

			const failureCount = result.errors ? Object.keys(result.errors).length : 0;
			const graceUntil = latestGraceDeadline(result.virtual_keys);
			const graceNote = graceUntil ? ` ${t("virtualKeys.rotateGraceNote", { date: new Date(graceUntil).toLocaleString() })}` : "";
			if (failureCount > 0) {
				toast.warning(t("virtualKeys.rotatedBulkPartial", { count: result.virtual_keys.length, failed: failureCount, graceNote }));
			} else {
				toast.success(t("virtualKeys.rotatedBulk", { count: result.virtual_keys.length, graceNote }));
			}
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const handleAddVirtualKey = () => {
		setEditingVirtualKeyId(null);
		setShowVirtualKeySheet(true);
	};
	useActionParam("create", handleAddVirtualKey, { isOpen: showVirtualKeySheet, enabled: hasCreateAccess });

	const handleEditVirtualKey = (vk: VirtualKey) => {
		setEditingVirtualKeyId(vk.id);
		setShowVirtualKeySheet(true);
	};

	const handleVirtualKeySaved = () => {
		setShowVirtualKeySheet(false);
		setEditingVirtualKeyId(null);
	};

	const handleRowClick = (vk: VirtualKey) => {
		onSelectedVkChange(vk.id);
	};

	const handleDetailSheetClose = () => {
		onSelectedVkChange("");
	};

	const selectedVirtualKeyIndex = useMemo(
		() => (selectedVkId ? virtualKeys.findIndex((vk) => vk.id === selectedVkId) : -1),
		[selectedVkId, virtualKeys],
	);

	const handleDetailNavigate = (direction: "prev" | "next") => {
		const currentVkId = selectedVkId;
		if (direction === "prev") {
			if (selectedVirtualKeyIndex > 0) {
				onSelectedVkChange(virtualKeys[selectedVirtualKeyIndex - 1].id);
			} else if (offset > 0) {
				const newOffset = Math.max(0, offset - limit);
				onSelectedVkChange("", { offset: newOffset });
				fetchVirtualKeys({
					limit,
					offset: newOffset,
					search: debouncedSearch || undefined,
					customer_id: customerFilter || undefined,
					team_id: teamFilter || undefined,
					user_id: userFilter || undefined,
					metadata: metadataFilterParam,
					sort_by: (sortBy as "name" | "budget_spent" | "created_at" | "status") || undefined,
					order: (order as "asc" | "desc") || undefined,
				}).then((result) => {
					if (result.data?.virtual_keys?.length) {
						const lastVk = result.data.virtual_keys[result.data.virtual_keys.length - 1];
						onSelectedVkChange(lastVk.id);
					} else if (result.error) {
						onSelectedVkChange(currentVkId, { offset });
					}
				});
			}
		} else {
			if (selectedVirtualKeyIndex >= 0 && selectedVirtualKeyIndex < virtualKeys.length - 1) {
				onSelectedVkChange(virtualKeys[selectedVirtualKeyIndex + 1].id);
			} else if (offset + limit < totalCount) {
				const newOffset = offset + limit;
				onSelectedVkChange("", { offset: newOffset });
				fetchVirtualKeys({
					limit,
					offset: newOffset,
					search: debouncedSearch || undefined,
					customer_id: customerFilter || undefined,
					team_id: teamFilter || undefined,
					user_id: userFilter || undefined,
					metadata: metadataFilterParam,
					sort_by: (sortBy as "name" | "budget_spent" | "created_at" | "status") || undefined,
					order: (order as "asc" | "desc") || undefined,
				}).then((result) => {
					if (result.data?.virtual_keys?.length) {
						const firstVk = result.data.virtual_keys[0];
						onSelectedVkChange(firstVk.id);
					} else if (result.error) {
						onSelectedVkChange(currentVkId, { offset });
					}
				});
			}
		}
	};

	// Enterprise records reveals and copies in the audit log; a no-op in OSS builds.
	const reportVkAccess = useVirtualKeyAccessAudit();

	const toggleKeyVisibility = (vkId: string) => {
		const newRevealed = new Set(revealedKeys);
		if (newRevealed.has(vkId)) {
			newRevealed.delete(vkId);
		} else {
			newRevealed.add(vkId);
			// Only the reveal edge is a disclosure; re-hiding is not.
			reportVkAccess(vkId, "reveal");
		}
		setRevealedKeys(newRevealed);
	};

	const maskKey = (key: string, revealed: boolean) => {
		if (revealed) return key;
		return key.substring(0, 8) + "•".repeat(Math.max(0, key.length - 8));
	};

	const { copy: copyToClipboard } = useCopyToClipboard();

	const hasActiveFilters = debouncedSearch || customerFilter || teamFilter || userFilter || metadataFilterKey;

	// Registered by the downstream build at module load; undefined in builds
	// without a user directory, which hides the user filter entirely.
	const UserPicker = getUserPicker();
	// Server-side search matches the key name, its team, its customer and its metadata, plus
	// the assigned user where there is a user directory to match against. Same signal as
	// the user filter below, so the placeholder never promises what OSS cannot do.
	const searchHint = UserPicker ? t("virtualKeys.searchHintWithUser") : t("virtualKeys.searchHintWithoutUser");

	const toggleSort = (column: string) => {
		if (sortBy === column) {
			if (order === "asc") {
				onSortChange(column, "desc");
			} else {
				// Clicking again clears sort
				onSortChange("", "");
			}
		} else {
			onSortChange(column, "asc");
		}
	};

	const handleExportCSV = async () => {
		if (exportScope === "current_page") {
			downloadCSV(virtualKeysToCSV(virtualKeys));
			toast.success(t("virtualKeys.exportedCount", { count: virtualKeys.length }));
			setShowExportDialog(false);
			return;
		}

		// Fetch all with same filters/sort applied
		const maxLimit = exportMaxLimit ? parseInt(exportMaxLimit, 10) : undefined;
		const fetchLimit = maxLimit && maxLimit > 0 ? maxLimit : 10000;

		try {
			const result = await fetchVirtualKeys({
				limit: fetchLimit,
				offset: 0,
				search: debouncedSearch || undefined,
				customer_id: customerFilter || undefined,
				team_id: teamFilter || undefined,
				user_id: userFilter || undefined,
				metadata: metadataFilterParam,
				sort_by: (sortBy as "name" | "budget_spent" | "created_at" | "status") || undefined,
				order: (order as "asc" | "desc") || undefined,
				export: true,
			}).unwrap();

			downloadCSV(virtualKeysToCSV(result.virtual_keys));
			toast.success(t("virtualKeys.exportedCount", { count: result.virtual_keys.length }));
			setShowExportDialog(false);
		} catch (error) {
			toast.error(t("virtualKeys.exportFailed", { message: getErrorMessage(error) }));
		}
	};

	const openExportDialog = () => {
		setExportScope("current_page");
		setExportMaxLimit("");
		setShowExportDialog(true);
	};

	const SortableHeader = ({ column, label }: { column: string; label: string }) => {
		const isActive = sortBy === column;
		const Icon = isActive ? (order === "desc" ? ArrowDown : ArrowUp) : ArrowUpDown;
		return (
			<Button variant="ghost" onClick={() => toggleSort(column)} data-testid={`vk-sort-${column}`} className="!px-0">
				{label}
				<Icon className={cn("ml-2 h-4 w-4", isActive && "text-foreground")} />
			</Button>
		);
	};

	// True empty state: no VKs at all (not just filtered to zero)
	if (totalCount === 0 && !hasActiveFilters) {
		return (
			<>
				{showVirtualKeySheet && (
					<VirtualKeySheet virtualKey={editingVirtualKey} onSave={handleVirtualKeySaved} onCancel={() => setShowVirtualKeySheet(false)} />
				)}
				<VirtualKeysEmptyState onAddClick={handleAddVirtualKey} canCreate={hasCreateAccess} />
			</>
		);
	}

	return (
		<>
			{showVirtualKeySheet && (
				<VirtualKeySheet virtualKey={editingVirtualKey} onSave={handleVirtualKeySaved} onCancel={() => setShowVirtualKeySheet(false)} />
			)}

			{!!selectedVkId && selectedVirtualKey && (
				<VirtualKeyDetailSheet
					virtualKey={selectedVirtualKey}
					onClose={handleDetailSheetClose}
					onNavigate={handleDetailNavigate}
					hasPrev={selectedVirtualKeyIndex > 0 || (selectedVirtualKeyIndex !== -1 && offset > 0)}
					hasNext={selectedVirtualKeyIndex !== -1 && (selectedVirtualKeyIndex < virtualKeys.length - 1 || offset + limit < totalCount)}
				/>
			)}

			{/* Export Dialog */}
			<Dialog open={showExportDialog} onOpenChange={setShowExportDialog}>
				<DialogContent className="sm:max-w-[425px]">
					<DialogHeader className="pb-0">
						<DialogTitle>{t("virtualKeys.exportTitle")}</DialogTitle>
						<DialogDescription>{t("virtualKeys.exportDialogDescription")}</DialogDescription>
					</DialogHeader>
					<div className="space-y-4">
						<div className="space-y-2">
							<Label className="text-sm">{t("virtualKeys.exportScope")}</Label>
							<div className="grid grid-cols-1 gap-2 sm:grid-cols-2" data-testid="vk-export-scope">
								<button
									type="button"
									onClick={() => setExportScope("current_page")}
									className={cn(
										"flex cursor-pointer flex-col items-center gap-1 rounded-md border px-3 py-3 text-sm transition-colors",
										exportScope === "current_page"
											? "border-primary bg-primary/5 text-foreground"
											: "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground",
									)}
								>
									<span className="font-medium">{t("virtualKeys.currentPage")}</span>
									<span className="text-muted-foreground text-xs">{t("virtualKeys.entriesCount", { count: virtualKeys.length })}</span>
								</button>
								<button
									type="button"
									onClick={() => setExportScope("all")}
									className={cn(
										"flex cursor-pointer flex-col items-center gap-1 rounded-md border px-3 py-3 text-sm transition-colors",
										exportScope === "all"
											? "border-primary bg-primary/5 text-foreground"
											: "border-border text-muted-foreground hover:border-primary/50 hover:text-foreground",
									)}
								>
									<span className="font-medium">{t("virtualKeys.allEntries")}</span>
									<span className="text-muted-foreground text-xs">{t("virtualKeys.entriesTotal", { count: totalCount })}</span>
								</button>
							</div>
						</div>

						{exportScope === "all" && (
							<div className="space-y-2">
								<Label htmlFor="export-max-limit" className="text-sm">
									{t("virtualKeys.maxEntries")} <span className="text-muted-foreground font-normal">{t("virtualKeys.optional")}</span>
								</Label>
								<Input
									id="export-max-limit"
									type="number"
									min="1"
									placeholder={t("virtualKeys.leaveBlankForAll", { count: totalCount })}
									value={exportMaxLimit}
									onChange={(e) => setExportMaxLimit(e.target.value)}
									data-testid="vk-export-max-limit"
								/>
							</div>
						)}

						{hasActiveFilters && (
							<p className="text-muted-foreground text-xs">
								{t("virtualKeys.filtersApplied")}{" "}
								{[
									debouncedSearch && t("virtualKeys.filterSearch", { query: debouncedSearch }),
									customerFilter && t("virtualKeys.filterCustomer"),
									teamFilter && t("virtualKeys.filterTeam"),
									userFilter && t("virtualKeys.filterUser"),
									metadataFilterKey && t("virtualKeys.filterMetadata"),
								]
									.filter(Boolean)
									.join(", ")}
							</p>
						)}

						<div className="text-muted-foreground flex items-center gap-2">
							<ShieldCheck className="h-3.5 w-3.5 shrink-0" />
							<p className="text-xs">{t("virtualKeys.exportNote")}</p>
						</div>
					</div>
					<DialogFooter className="pt-0">
						<Button variant="outline" onClick={() => setShowExportDialog(false)} disabled={isExporting}>
							{tc("cancel")}
						</Button>
						<Button onClick={handleExportCSV} disabled={isExporting} data-testid="vk-export-confirm-btn">
							{isExporting ? (
								<>
									<Loader2 className="h-4 w-4 animate-spin" />
									{t("virtualKeys.exporting")}
								</>
							) : (
								<>
									<Download className="h-4 w-4" />
									{t("virtualKeys.exportCsv")}
								</>
							)}
						</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>

			<AlertDialog open={showBulkRotateDialog} onOpenChange={setShowBulkRotateDialog}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>{t("virtualKeys.rotateSelectedTitle")}</AlertDialogTitle>
						<AlertDialogDescription>{t("virtualKeys.rotateSelectedDescription", { count: selectedCount })}</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="vk-bulk-rotate-cancel-btn">{tc("cancel")}</AlertDialogCancel>
						<AlertDialogAction
							onClick={handleBulkRotate}
							disabled={isBulkRotating || selectedCount === 0}
							data-testid="vk-bulk-rotate-confirm-btn"
						>
							{isBulkRotating ? t("providers.rotating") : t("virtualKeys.rotateSelected")}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<div className="flex min-h-0 w-full grow flex-col overflow-hidden">
				{/* Toolbar: Search + Filters + Actions */}
				<div className="@container/vk-toolbar mb-4 flex shrink-0 flex-wrap items-center gap-3">
					<PageTitle title={t("virtualKeys.title")}>{t("virtualKeys.subtitle")}</PageTitle>
					<div className="relative max-w-sm min-w-0 grow basis-40">
						<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
						<Input
							aria-label={t("virtualKeys.searchAriaByHint", { hint: searchHint })}
							placeholder={t("virtualKeys.searchByHint", { hint: searchHint })}
							value={search}
							onChange={(e) => onSearchChange(e.target.value)}
							className="pl-9"
							data-testid="vk-search-input"
						/>
					</div>
					{/* Both filters search server-side and resolve their own label for a
					    value restored from the URL, so the page fetches no entity lists. */}
					<div className={FILTER_WRAPPER_CLASS} data-testid="vk-customer-filter">
						<CustomerSelector
							value={customerFilter}
							onChange={onCustomerFilterChange}
							placeholder={t("virtualKeys.allCustomers")}
							triggerIcon={<Building2 className={FILTER_ICON_CLASS} />}
							triggerClassName={FILTER_TRIGGER_CLASS}
							contentClassName={FILTER_CONTENT_CLASS}
							className={FILTER_SELECTOR_CLASS}
						/>
						<FilterClearButton
							show={!!customerFilter}
							label={t("virtualKeys.clearCustomerFilter")}
							onClear={() => onCustomerFilterChange("")}
							data-testid="vk-customer-filter-clear-btn"
						/>
					</div>
					{customerFilter && teamFilter && <span className="text-muted-foreground text-xs font-medium">{t("virtualKeys.or")}</span>}
					<div className={FILTER_WRAPPER_CLASS} data-testid="vk-team-filter">
						<TeamSelector
							value={teamFilter}
							onChange={onTeamFilterChange}
							placeholder={t("virtualKeys.allTeams")}
							triggerIcon={<Users className={FILTER_ICON_CLASS} />}
							triggerClassName={FILTER_TRIGGER_CLASS}
							contentClassName={FILTER_CONTENT_CLASS}
							className={FILTER_SELECTOR_CLASS}
						/>
						<FilterClearButton
							show={!!teamFilter}
							label={t("virtualKeys.clearTeamFilter")}
							onClear={() => onTeamFilterChange("")}
							data-testid="vk-team-filter-clear-btn"
						/>
					</div>
					{UserPicker && (customerFilter || teamFilter) && userFilter && (
						<span className="text-muted-foreground text-xs font-medium">{t("virtualKeys.or")}</span>
					)}
					{UserPicker && (
						<div className={FILTER_WRAPPER_CLASS} data-testid="vk-user-filter">
							<UserPicker
								value={userFilter}
								onChange={onUserFilterChange}
								placeholder={t("virtualKeys.allUsers")}
								triggerIcon={<UserRound className={FILTER_ICON_CLASS} />}
								triggerClassName={FILTER_TRIGGER_CLASS}
								contentClassName={FILTER_CONTENT_CLASS}
								className={FILTER_SELECTOR_CLASS}
							/>
							<FilterClearButton
								show={!!userFilter}
								label={t("virtualKeys.clearUserFilter")}
								onClear={() => onUserFilterChange("")}
								data-testid="vk-user-filter-clear-btn"
							/>
						</div>
					)}

					<div className="flex shrink-0 items-center gap-1" data-testid="vk-metadata-filter">
						<MetadataFilter metadataKey={metadataFilterKey} metadataValue={metadataFilterValue} onChange={onMetadataFilterChange} />
						<FilterClearButton
							show={!!metadataFilterKey}
							label={t("virtualKeys.metadataFilter.clear")}
							onClear={() => onMetadataFilterChange("", "")}
							data-testid="vk-metadata-filter-clear-btn"
						/>
					</div>

					<div className="ml-auto flex shrink-0 items-center gap-2">
						{selectedCount > 0 && (
							<Tooltip>
								<TooltipTrigger asChild>
									{/* A disabled button emits no pointer events, so the span carries the hover. */}
									<span tabIndex={hasUpdateAccess ? undefined : 0} className="inline-flex">
										<Button
											variant="outline"
											className="size-9 px-0 @5xl/vk-toolbar:w-auto @5xl/vk-toolbar:px-4"
											onClick={() => setShowBulkRotateDialog(true)}
											disabled={!hasUpdateAccess || isBulkRotating}
											aria-label={t("virtualKeys.rotateSelectedCount", { count: selectedCount })}
											data-testid="vk-bulk-rotate-btn"
										>
											<RotateCcw className="h-4 w-4" />
											<span className="hidden @5xl/vk-toolbar:inline">
												{t("virtualKeys.rotateSelectedCount", { count: selectedCount })}
											</span>
										</Button>
									</span>
								</TooltipTrigger>
								<TooltipContent>
									{actionDisabledReason(hasUpdateAccess, "rotate", "virtual keys") ??
										t("virtualKeys.rotateSelectedCount", { count: selectedCount })}
								</TooltipContent>
							</Tooltip>
						)}
						<Tooltip>
							<TooltipTrigger asChild>
								<Button
									variant="outline"
									className="size-9 px-0 @5xl/vk-toolbar:w-auto @5xl/vk-toolbar:px-4"
									onClick={openExportDialog}
									disabled={virtualKeys.length === 0}
									aria-label={t("virtualKeys.exportCsv")}
									data-testid="vk-export-btn"
								>
									<Download className="h-4 w-4" />
									<span className="hidden @5xl/vk-toolbar:inline">{t("virtualKeys.exportCsv")}</span>
								</Button>
							</TooltipTrigger>
							<TooltipContent>{t("virtualKeys.exportCsv")}</TooltipContent>
						</Tooltip>
						<Tooltip>
							<TooltipTrigger asChild>
								<span tabIndex={hasCreateAccess ? undefined : 0} className="inline-flex">
									<Button
										className="size-9 px-0 @5xl/vk-toolbar:w-auto @5xl/vk-toolbar:px-4"
										onClick={handleAddVirtualKey}
										disabled={!hasCreateAccess}
										aria-label={t("virtualKeys.addVirtualKey")}
										data-testid="create-vk-btn"
									>
										<Plus className="h-4 w-4" />
										<span className="hidden @5xl/vk-toolbar:inline">{t("virtualKeys.addVirtualKey")}</span>
									</Button>
								</span>
							</TooltipTrigger>
							<TooltipContent>
								{actionDisabledReason(hasCreateAccess, "create", "virtual keys") ?? t("virtualKeys.addVirtualKey")}
							</TooltipContent>
						</Tooltip>
					</div>
				</div>

				<div className="mb-2 min-h-0 grow overflow-hidden rounded-sm border">
					<Table containerClassName="h-full overflow-auto" className="w-full min-w-[1588px] table-fixed" data-testid="vk-table">
						<TableHeader className="bg-muted sticky top-0 z-20">
							<TableRow>
								<TableHead className="w-[48px]">
									<Checkbox
										checked={allVisibleSelected || (someVisibleSelected ? "indeterminate" : false)}
										onCheckedChange={(checked) => toggleSelectAllVisible(checked === true)}
										aria-label={t("virtualKeys.selectAllAria")}
										data-testid="vk-select-all-checkbox"
									/>
								</TableHead>
								<TableHead className="w-[250px]">
									<SortableHeader column="name" label={t("virtualKeys.name")} />
								</TableHead>
								<TableHead className="w-[160px]">{t("virtualKeys.assignedTo")}</TableHead>
								<TableHead className="w-[440px]">{t("virtualKeys.key")}</TableHead>
								<TableHead className="w-[200px]">
									<SortableHeader column="budget_spent" label={t("virtualKeys.budget")} />
								</TableHead>
								<TableHead className="w-[200px]">{t("virtualKeys.rateLimits")}</TableHead>
								<TableHead className="w-[180px]">
									<SortableHeader column="status" label={t("virtualKeys.status")} />
								</TableHead>
								<TableHead className={`bg-muted sticky right-0 z-30 w-[56px] text-right ${PIN_SHADOW_RIGHT}`}></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{virtualKeys.length === 0 ? (
								<TableRow>
									<TableCell colSpan={8} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">{t("virtualKeys.noMatching")}</span>
									</TableCell>
								</TableRow>
							) : (
								virtualKeys.map((vk) => {
									const isRevealed = revealedKeys.has(vk.id);
									const isExpired = !!vk.expires_at && Date.now() >= new Date(vk.expires_at).getTime();
									const showExpiredBadge = vk.is_active && isExpired;

									return (
										<TableRow
											key={vk.id}
											data-testid={`vk-row-${vk.name}`}
											className="group hover:bg-muted/50 cursor-pointer transition-colors"
											onClick={() => handleRowClick(vk)}
										>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<Checkbox
													checked={selectedIds.has(vk.id)}
													onCheckedChange={(checked) => toggleSelectVirtualKey(vk.id, checked === true)}
													aria-label={t("virtualKeys.selectRowAria", { name: vk.name })}
													data-testid={`vk-select-checkbox-${vk.name}`}
												/>
											</TableCell>
											<TableCell className="max-w-[200px]">
												<div className="truncate font-medium">{vk.name}</div>
											</TableCell>
											<TableCell>
												<VKAssignedToCell vk={vk} />
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												<div className="flex items-center gap-2">
													<code className="cursor-default py-1 font-mono text-sm" data-testid="vk-key-value">
														{maskKey(vk.value, isRevealed)}
													</code>
													<div className="flex items-center">
														<Button
															variant="ghost"
															size="sm"
															onClick={() => toggleKeyVisibility(vk.id)}
															data-testid={`vk-visibility-btn-${vk.name}`}
														>
															{isRevealed ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
														</Button>
														<Button
															variant="ghost"
															size="sm"
															onClick={() => {
																// Not awaited: the clipboard write must not wait on the beacon.
																reportVkAccess(vk.id, "copy");
																copyToClipboard(vk.value);
															}}
															data-testid={`vk-copy-btn-${vk.name}`}
														>
															<Copy className="h-4 w-4" />
														</Button>
													</div>
												</div>
											</TableCell>
											<TableCell>
												<VKBudgetCell vk={vk} />
											</TableCell>
											<TableCell>
												<VKRateLimitCell vk={vk} />
											</TableCell>
											<TableCell onClick={(e) => e.stopPropagation()}>
												{showExpiredBadge ? (
													<Badge
														variant="destructive"
														className="text-xs"
														title={(vk.delete_after_expire ?? deleteExpiredByDefault) ? t("virtualKeys.expiredAutoDeleteTitle") : undefined}
													>
														{(vk.delete_after_expire ?? deleteExpiredByDefault)
															? t("virtualKeys.expiredAutoDelete")
															: t("virtualKeys.expired")}
													</Badge>
												) : (
													<VKActiveSwitch vk={vk} hasUpdateAccess={hasUpdateAccess} onToggle={handleToggleActive} />
												)}
											</TableCell>
											<TableCell
												className={`group-hover:bg-muted dark:bg-card dark:group-hover:bg-muted sticky right-0 z-20 bg-white text-right ${PIN_SHADOW_RIGHT}`}
												onClick={(e) => e.stopPropagation()}
											>
												<VKActionsMenu
													vk={vk}
													hasUpdateAccess={hasUpdateAccess}
													hasDeleteAccess={hasDeleteAccess}
													isDeleting={isDeleting}
													onEdit={handleEditVirtualKey}
													onDelete={handleDelete}
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
				{totalCount > 0 && (
					<div className="flex shrink-0 items-center justify-between text-xs" data-testid="pagination">
						<div className="text-muted-foreground flex items-center gap-2">
							{t("virtualKeys.rangeOf", {
								from: (offset + 1).toLocaleString(),
								to: Math.min(offset + limit, totalCount).toLocaleString(),
								total: totalCount.toLocaleString(),
							})}
						</div>

						<div className="flex items-center gap-2">
							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(Math.max(0, offset - limit))}
								disabled={offset === 0}
								data-testid="vk-pagination-prev-btn"
								aria-label={t("virtualKeys.previousPage")}
							>
								<ChevronLeft className="size-3" />
							</Button>

							<div className="flex items-center gap-1">
								<span>{t("virtualKeys.page")}</span>
								<span>{Math.floor(offset / limit) + 1}</span>
								<span>{t("virtualKeys.pageOf", { total: Math.ceil(totalCount / limit) })}</span>
							</div>

							<Button
								variant="ghost"
								size="sm"
								onClick={() => onOffsetChange(offset + limit)}
								disabled={offset + limit >= totalCount}
								data-testid="vk-pagination-next-btn"
								aria-label={t("virtualKeys.nextPage")}
							>
								<ChevronRight className="size-3" />
							</Button>
						</div>
					</div>
				)}
			</div>
		</>
	);
}