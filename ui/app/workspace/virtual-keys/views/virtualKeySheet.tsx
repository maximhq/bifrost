import { useVirtualKeyUsage } from "@/app/workspace/virtual-keys/hooks/useVirtualKeyUsage";
import { BudgetOverrideDialog } from "@/components/budgetOverrideDialog";
import { MCPClientConfigsEditor } from "@/components/mcp/mcpClientConfigsEditor";
import { CustomerSelector } from "@/components/entitySelectors/customerSelector";
import { TeamSelector } from "@/components/entitySelectors/teamSelector";
import { Alert, AlertDescription } from "@/components/ui/alert";
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
import { Button } from "@/components/ui/button";
import { ComboboxSelect } from "@/components/ui/combobox";
import { ConfigSyncAlert } from "@/components/ui/configSyncAlert";
import { DateTimePicker } from "@/components/ui/datePickerWithRange";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import MultiBudgetLines from "@/components/ui/multibudgets";
import { MultiSelect } from "@/components/ui/multiSelect";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { ProviderConfigCard } from "@/components/ui/providerConfigCard";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import Toggle from "@/components/ui/toggle";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resetDurationOptions, supportsCalendarAlignment } from "@/lib/constants/governance";
import { ProviderIconType, RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getUserPicker } from "@/lib/registries/userPicker";
import {
	getErrorMessage,
	useAttachVirtualMCPVirtualKeyMutation,
	useCreateVirtualKeyMutation,
	useDetachVirtualMCPVirtualKeyMutation,
	useGetAllKeysQuery,
	useGetProvidersQuery,
	useGetTeamQuery,
	useGetVirtualKeyQuery,
	useRemoveVirtualKeyBudgetOverrideMutation,
	useRotateVirtualKeyMutation,
	useSetVirtualKeyBudgetOverrideMutation,
	useUpdateVirtualKeyMutation,
} from "@/lib/store";
import { VirtualMcpAssignmentsEditor } from "@/components/mcp/virtualMcpAssignmentsEditor";
import { diffVmcpAssignments, vmcpAssignmentsDirty } from "./virtualKeySheet.utils";
import { KnownProvider } from "@/lib/types/config";
import { BudgetOverrideRequest, CreateVirtualKeyRequest, UpdateVirtualKeyRequest, VirtualKey } from "@/lib/types/governance";
import {
	type BudgetComparisonEntry,
	budgetSignature,
	formatCurrency,
	getEffectiveBudgetLimit,
	hasActiveBudgetOverride,
	parseResetPeriod,
	quarterStartOf,
} from "@/lib/utils/governance";
import ManagedVirtualKeyActions from "@enterprise/components/access-profiles/managedVirtualKeyActions";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { useGetMyVKCreationPolicyQuery } from "@enterprise/lib/store/apis/accessProfileApi";
import { useAttachVirtualKeyUsersMutation, useDetachVirtualKeyUserMutation } from "@enterprise/lib/store/apis/virtualKeyUsersApi";
import { zodResolver } from "@hookform/resolvers/zod";
import { useNavigate } from "@tanstack/react-router";
import { formatDistanceToNow } from "date-fns";
import { Info, Lock, RotateCcw, Users } from "lucide-react";
import { useEffect, useRef, useState } from "react";
// Side-effect import: registers the enterprise user picker so "Assign to User"
// becomes available. Resolves to an empty module on OSS builds.
import "@enterprise/lib/registrations/userPicker";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

interface VirtualKeySheetProps {
	virtualKey?: VirtualKey | null;
	// When set, the new VK is created under this team. The entity assignment is pre-set
	// and cannot be changed (but all other fields remain editable).
	defaultTeamId?: string;
	onSave: () => void;
	onCancel: () => void;
}

// Provider configuration schema
const providerConfigSchema = z.object({
	id: z.number().optional(),
	provider: z.string().min(1, "Provider is required"),
	weight: z.number().min(0, "Weight must be at least 0").max(1, "Weight must be at most 1").optional(),
	allowed_models: z.array(z.string()).optional(),
	blacklisted_models: z.array(z.string()).optional(),
	key_ids: z.array(z.string()).optional(), // Keys associated with this provider config
	// Provider-level budget
	budgets: z
		.array(
			z.object({
				id: z.string().optional(),
				max_limit: z.number().nonnegative().optional(),
				reset_duration: z.string().optional(),
				// Zod strips unknown keys, so the fiscal quarter has to be declared
				// here or it is erased from form state on every parse.
				reset_config: z.object({ quarter_start_month: z.number().int().min(1).max(12).optional() }).optional(),
			}),
		)
		.optional(),
	// Provider-level rate limits
	rate_limit: z
		.object({
			token_max_limit: z.number().int().nonnegative().optional(),
			token_reset_duration: z.string().optional(),
			request_max_limit: z.number().int().nonnegative().optional(),
			request_reset_duration: z.string().optional(),
		})
		.optional(),
	// Per-model budgets/rate-limits under this provider
	model_budgets: z
		.array(
			z.object({
				model_name: z.string().trim().min(1, "Model name is required"),
				budgets: z
					.array(
						z.object({
							id: z.string().optional(),
							max_limit: z.number().nonnegative().optional(),
							reset_duration: z.string().optional(),
							reset_config: z.object({ quarter_start_month: z.number().int().min(1).max(12).optional() }).optional(),
						}),
					)
					.optional(),
				rate_limit: z
					.object({
						token_max_limit: z.number().int().nonnegative().optional(),
						token_reset_duration: z.string().optional(),
						request_max_limit: z.number().int().nonnegative().optional(),
						request_reset_duration: z.string().optional(),
					})
					.optional(),
			}),
		)
		.optional(),
});

const mcpConfigSchema = z.object({
	id: z.number().optional(),
	mcp_client_name: z.string().min(1, "MCP client name is required"),
	tools_to_execute: z.array(z.string()).optional(),
});

// Main form schema
const formSchema = z
	.object({
		name: z.string().min(1, "Virtual key name is required"),
		description: z.string().optional(),
		providerConfigs: z.array(providerConfigSchema).optional(),
		// When true, all providers are allowed; providerConfigs remain optional per-provider overrides.
		allowAllProviders: z.boolean(),
		mcpConfigs: z.array(mcpConfigSchema).optional(),
		entityType: z.enum(["team", "customer", "user", "none"]),
		teamId: z.string().optional(),
		customerId: z.string().optional(),
		// Enterprise-only: a VK can be attached to at most one user, via the
		// separate /virtual-keys/{id}/users endpoint rather than the VK payload.
		userId: z.string().optional(),
		isActive: z.boolean(),
		expiresAt: z.string().nullable().optional(), // ISO 8601 datetime-local string, or null to clear
		// Budget
		budgetCalendarAligned: z.boolean(),
		budgets: z
			.array(
				z.object({
					id: z.string().optional(),
					max_limit: z.number().nonnegative().optional(),
					reset_duration: z.string(),
					reset_config: z.object({ quarter_start_month: z.number().int().min(1).max(12).optional() }).optional(),
				}),
			)
			.optional(),
		// Token limits
		tokenMaxLimit: z.number().int().nonnegative().optional(),
		tokenResetDuration: z.string().optional(),
		// Request limits
		requestMaxLimit: z.number().int().nonnegative().optional(),
		requestResetDuration: z.string().optional(),
	})
	.refine(
		(data) => {
			// If entityType is "team", teamId must be provided and not empty
			if (data.entityType === "team") {
				return data.teamId && data.teamId.trim() !== "";
			}
			// If entityType is "customer", customerId must be provided and not empty
			if (data.entityType === "customer") {
				return data.customerId && data.customerId.trim() !== "";
			}
			// If entityType is "user", userId must be provided and not empty
			if (data.entityType === "user") {
				return data.userId && data.userId.trim() !== "";
			}
			return true;
		},
		{
			message: "Please select a valid team, customer, or user when assignment type is chosen",
			path: ["entityType"], // This will show the error on the entityType field
		},
	);

type FormData = z.infer<typeof formSchema>;

/**
 * Why the save dialog is asking about existing usage.
 *
 * The two cases need different copy: "over-limit" means the recorded spend already
 * meets or exceeds the new cap, while "quarter-shift" only means the reset boundary
 * moved under a live budget - which fires at any usage above zero, so labelling it
 * as over-limit would misdescribe a budget that is nowhere near its cap.
 */
type BudgetUsageWarning = {
	kind: "over-limit" | "quarter-shift";
	message: string;
};

const pad2 = (n: number) => n.toString().padStart(2, "0");

const toDatetimeLocal = (d: Date) =>
	`${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;

const presetFromNow = (offsetMs: number) => toDatetimeLocal(new Date(Date.now() + offsetMs));

const EXPIRY_PRESETS = [
	{ label: "30 min", ms: 30 * 60_000 },
	{ label: "1 hour", ms: 60 * 60_000 },
	{ label: "24 hours", ms: 24 * 60 * 60_000 },
	{ label: "7 days", ms: 7 * 24 * 60 * 60_000 },
] as const;

interface ExpiryFieldProps {
	value: string | null | undefined;
	onChange: (v: string | null) => void;
}

function ExpiryPickerField({ value, onChange }: ExpiryFieldProps) {
	// Preset timestamps are computed from Date.now() at click time, so the picked
	// preset can't be derived back from the value; track it for highlighting.
	const [selectedPreset, setSelectedPreset] = useState<string | null>(null);

	return (
		<FormItem>
			<FormLabel>Expiry</FormLabel>
			<p className="text-muted-foreground text-xs">
				{value ? `This key expires ${formatDistanceToNow(new Date(value), { addSuffix: true })}.` : "This key never expires."}
			</p>
			<div className="flex flex-wrap gap-1.5">
				<Button
					type="button"
					variant={!value ? "default" : "outline"}
					size="sm"
					onClick={() => {
						setSelectedPreset(null);
						onChange(null);
					}}
				>
					Never
				</Button>
				{EXPIRY_PRESETS.map(({ label, ms }) => (
					<Button
						key={label}
						type="button"
						variant={value && selectedPreset === label ? "default" : "outline"}
						size="sm"
						onClick={() => {
							setSelectedPreset(label);
							onChange(presetFromNow(ms));
						}}
					>
						{label}
					</Button>
				))}
				<DateTimePicker
					buttonClassName="h-8 text-sm px-3"
					buttonVariant={value && !selectedPreset ? "default" : "outline"}
					dateTime={value ? new Date(value) : undefined}
					disabledBefore={new Date()}
					onDateTimeUpdate={(dt) => {
						setSelectedPreset(null);
						onChange(toDatetimeLocal(dt));
					}}
				/>
			</div>
			<FormMessage />
		</FormItem>
	);
}

export default function VirtualKeySheet({ virtualKey, defaultTeamId, onSave, onCancel }: VirtualKeySheetProps) {
	const [isOpen, setIsOpen] = useState(true);
	const navigate = useNavigate();
	const isEditing = !!virtualKey;

	const hasCreateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.VirtualKeys, RbacOperation.Update);
	const canSubmit = isEditing ? hasUpdateAccess : hasCreateAccess;

	// Detect AP-managed status via the managing profile's virtual_key_ids, not just by the presence
	// of assignees — directly-attached users don't imply an access-profile relation.
	const { assignedUsers, isManagedByProfile: isManagedByProfileHook, managingProfile } = useVirtualKeyUsage(virtualKey);
	// On create, check whether the user's access profile will govern the new VK. If so,
	// lock the governance fields up front — the server applies the profile regardless.
	const { data: vkCreationPolicy } = useGetMyVKCreationPolicyQuery(undefined, {
		skip: isEditing,
		refetchOnMountOrArgChange: true,
	});
	const willBeGovernedOnCreate = !isEditing && !!vkCreationPolicy?.governed;
	const isManagedByProfile = (isEditing && isManagedByProfileHook) || willBeGovernedOnCreate;
	// User assignment is enterprise-only: OSS registers no picker, so the option stays hidden.
	const UserPicker = getUserPicker();
	// A VK can have at most one user (enforced by a unique index server-side).
	const assignedUserId = assignedUsers[0]?.id ?? "";
	const assignedUserLabel = assignedUsers[0]?.name || assignedUsers[0]?.email || assignedUserId;
	// Team attachment: when creating from a team context (defaultTeamId provided), the entity
	// assignment is pre-set and locked. When editing an existing VK the assignment can be changed.
	const attachedTeamId = isEditing ? virtualKey?.team_id || "" : defaultTeamId || "";
	const isTeamLocked = !isEditing && !!defaultTeamId;
	// Only the locked banner needs this name, and only when creating under a team,
	// so resolve that one team by id rather than holding the whole list.
	const { data: attachedTeamData } = useGetTeamQuery(attachedTeamId, {
		skip: !isTeamLocked || !attachedTeamId,
	});
	const attachedTeam = attachedTeamData?.team;

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => {
			onCancel();
		}, 150); // Slightly longer than the 100ms animation duration
	};

	// RTK Query hooks
	const { data: providersData, error: providersError } = useGetProvidersQuery();
	const { data: keysData, error: keysError } = useGetAllKeysQuery();
	const [createVirtualKey, { isLoading: isCreating }] = useCreateVirtualKeyMutation();
	const [updateVirtualKey, { isLoading: isUpdating }] = useUpdateVirtualKeyMutation();
	const [rotateVirtualKey, { isLoading: isRotating }] = useRotateVirtualKeyMutation();
	const [attachVirtualKeyUsers, { isLoading: isAttachingUser }] = useAttachVirtualKeyUsersMutation();
	const [detachVirtualKeyUser, { isLoading: isDetachingUser }] = useDetachVirtualKeyUserMutation();
	const [setBudgetOverride] = useSetVirtualKeyBudgetOverrideMutation();
	const [removeBudgetOverride] = useRemoveVirtualKeyBudgetOverrideMutation();
	const [attachVirtualMCPVirtualKey] = useAttachVirtualMCPVirtualKeyMutation();
	const [detachVirtualMCPVirtualKey] = useDetachVirtualMCPVirtualKeyMutation();
	// Tracks the whole attach/detach reconciliation, which spans several sequential requests whose
	// own loading states go idle between them; without it Save could re-enable mid-reconcile.
	const [isReconcilingVmcps, setIsReconcilingVmcps] = useState(false);
	const isLoading = isCreating || isUpdating || isRotating || isAttachingUser || isDetachingUser || isReconcilingVmcps;

	// Virtual MCPs this key is assigned to. The list VK carries none, so fetch the single VK (which
	// includes virtual_mcp_ids); stage edits locally and reconcile attach/detach on Save.
	const {
		data: vkDetail,
		isLoading: isVkDetailLoading,
		isError: isVkDetailError,
		refetch: refetchVkDetail,
	} = useGetVirtualKeyQuery(virtualKey?.id ?? "", { skip: !isEditing || !virtualKey?.id });
	const originalVmcpIds = vkDetail?.virtual_mcp_ids ?? [];
	const [assignedVmcpIds, setAssignedVmcpIds] = useState<number[]>([]);
	const vmcpsInitialized = useRef(false);
	useEffect(() => {
		if (isEditing && vkDetail && !vmcpsInitialized.current) {
			setAssignedVmcpIds(vkDetail.virtual_mcp_ids ?? []);
			vmcpsInitialized.current = true;
		}
	}, [isEditing, vkDetail]);
	// For an existing key the baseline isn't trustworthy until the detail query lands, so the editor
	// stays disabled and dirty stays false while it loads or errors (see gating on the editor below).
	const vmcpDetailReady = !isEditing || (!!vkDetail && !isVkDetailLoading && !isVkDetailError);
	const vmcpDirty = vmcpDetailReady && vmcpAssignmentsDirty(originalVmcpIds, assignedVmcpIds);

	// Attach the newly-assigned Virtual MCPs and detach the removed ones for this key. On any failure,
	// resync local state from the server so a later Save reconciles against what actually persisted.
	const reconcileVmcpAssignments = async (vkId: string) => {
		const { toAttach, toDetach } = diffVmcpAssignments(originalVmcpIds, assignedVmcpIds);
		if (toAttach.length === 0 && toDetach.length === 0) return;
		setIsReconcilingVmcps(true);
		try {
			for (const id of toAttach) {
				await attachVirtualMCPVirtualKey({ id, vkId }).unwrap();
			}
			for (const id of toDetach) {
				await detachVirtualMCPVirtualKey({ id, vkId }).unwrap();
			}
		} catch (error) {
			try {
				const refreshed = await refetchVkDetail().unwrap();
				setAssignedVmcpIds(refreshed.virtual_mcp_ids ?? []);
			} catch {
				// Leave staged state as-is if the resync itself fails; the outer handler surfaces the error.
			}
			throw error;
		} finally {
			setIsReconcilingVmcps(false);
		}
	};
	const persistedOverrideBudgets = [
		...(virtualKey?.budgets ?? []).map((budget) => ({
			budget,
			label: "Virtual key",
		})),
		...(virtualKey?.provider_configs ?? []).flatMap((config) =>
			(config.budgets ?? []).map((budget) => ({
				budget,
				label: ProviderLabels[config.provider as ProviderName] ?? config.provider,
			})),
		),
	];
	const saveBudgetOverride = async (budgetId: string, data: BudgetOverrideRequest) => {
		if (!virtualKey) throw new Error("Virtual key is required");
		await setBudgetOverride({ vkId: virtualKey.id, budgetId, data }).unwrap();
	};
	const clearBudgetOverride = async (budgetId: string) => {
		if (!virtualKey) throw new Error("Virtual key is required");
		await removeBudgetOverride({ vkId: virtualKey.id, budgetId }).unwrap();
	};

	const availableKeys = keysData || [];
	const availableProviders = providersData || [];

	// Form setup
	const form = useForm<z.input<typeof formSchema>, unknown, FormData>({
		resolver: zodResolver(formSchema),
		defaultValues: {
			name: virtualKey?.name || "",
			description: virtualKey?.description || "",
			providerConfigs:
				virtualKey?.provider_configs?.map((config) => ({
					id: config.id,
					provider: config.provider,
					weight: config.weight ?? undefined,
					allowed_models: config.allowed_models || [],
					blacklisted_models: config.blacklisted_models || [],
					key_ids: config.allow_all_keys ? ["*"] : config.keys?.map((key) => key.key_id) || [],
					budgets: config.budgets?.map((b) => ({
						id: b.id,
						max_limit: b.max_limit,
						reset_duration: b.reset_duration,
						reset_config: b.reset_config,
					})),
					rate_limit: config.rate_limit
						? {
								token_max_limit: config.rate_limit.token_max_limit ?? undefined,
								token_reset_duration: config.rate_limit.token_reset_duration,
								request_max_limit: config.rate_limit.request_max_limit ?? undefined,
								request_reset_duration: config.rate_limit.request_reset_duration,
							}
						: undefined,
					model_budgets: config.model_budgets?.map((mb) => ({
						model_name: mb.model_name,
						budgets: mb.budgets?.map((b) => ({
							id: b.id,
							max_limit: b.max_limit,
							reset_duration: b.reset_duration,
							reset_config: b.reset_config,
						})),
						rate_limit: mb.rate_limit
							? {
									token_max_limit: mb.rate_limit.token_max_limit ?? undefined,
									token_reset_duration: mb.rate_limit.token_reset_duration,
									request_max_limit: mb.rate_limit.request_max_limit ?? undefined,
									request_reset_duration: mb.rate_limit.request_reset_duration,
								}
							: undefined,
					})),
				})) || [],
			allowAllProviders: virtualKey?.allow_all_providers ?? false,
			mcpConfigs:
				virtualKey?.mcp_configs?.map((config) => ({
					id: config.id,
					mcp_client_name: config.mcp_client?.name || "",
					tools_to_execute: config.tools_to_execute || [],
				})) || [],
			entityType: virtualKey?.team_id ? "team" : virtualKey?.customer_id ? "customer" : !isEditing && defaultTeamId ? "team" : "none",
			teamId: virtualKey?.team_id || (!isEditing ? defaultTeamId || "" : ""),
			customerId: virtualKey?.customer_id || "",
			// The attached user arrives from a separate request; synced in below once it loads.
			userId: "",
			isActive: virtualKey?.is_active ?? true,
			expiresAt: virtualKey?.expires_at
				? (() => {
						const d = new Date(virtualKey.expires_at);
						return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
					})()
				: null,
			budgets:
				virtualKey?.budgets && virtualKey.budgets.length > 0
					? virtualKey.budgets.map((b) => ({
							id: b.id,
							max_limit: b.max_limit,
							reset_duration: b.reset_duration ?? "1M",
							reset_config: b.reset_config,
						}))
					: [],
			budgetCalendarAligned: virtualKey?.calendar_aligned ?? false,
			tokenMaxLimit: virtualKey?.rate_limit?.token_max_limit ?? undefined,
			tokenResetDuration: virtualKey?.rate_limit?.token_reset_duration || "1h",
			requestMaxLimit: virtualKey?.rate_limit?.request_max_limit ?? undefined,
			requestResetDuration: virtualKey?.rate_limit?.request_reset_duration || "1h",
		},
	});

	// Handle keys loading error
	useEffect(() => {
		if (keysError) {
			toast.error(`Failed to load available keys: ${getErrorMessage(keysError)}`);
		}
	}, [keysError]);

	// Handle providers loading error
	useEffect(() => {
		if (providersError) {
			toast.error(`Failed to load available providers: ${getErrorMessage(providersError)}`);
		}
	}, [providersError]);

	// Clear the ids that don't belong to the selected entity type
	useEffect(() => {
		const entityType = form.watch("entityType");
		if (entityType === "none") {
			form.setValue("teamId", "", { shouldDirty: true });
			form.setValue("customerId", "", { shouldDirty: true });
			form.setValue("userId", "", { shouldDirty: true });
		} else if (entityType === "team") {
			form.setValue("customerId", "", { shouldDirty: true });
			form.setValue("userId", "", { shouldDirty: true });
		} else if (entityType === "customer") {
			form.setValue("teamId", "", { shouldDirty: true });
			form.setValue("userId", "", { shouldDirty: true });
		} else if (entityType === "user") {
			form.setValue("teamId", "", { shouldDirty: true });
			form.setValue("customerId", "", { shouldDirty: true });
		}
	}, [form.watch("entityType"), form]);

	// The VK-user association is fetched separately from the VK itself, so it can't be part of
	// defaultValues. Seed it once when it arrives, and only if the user hasn't already touched the
	// assignment (their in-progress edit must win over a late-arriving response).
	const didSyncAssignedUser = useRef(false);
	useEffect(() => {
		if (didSyncAssignedUser.current || !isEditing || !assignedUserId) return;
		didSyncAssignedUser.current = true;
		if (form.formState.dirtyFields.entityType || form.getValues("entityType") !== "none") return;
		form.setValue("entityType", "user");
		form.setValue("userId", assignedUserId);
	}, [assignedUserId, isEditing, form]);

	// Provider configuration state
	const [selectedProvider, setSelectedProvider] = useState<string>("");

	// Get current provider configs from form
	const providerConfigs = form.watch("providerConfigs") || [];

	// Whether "Allow all providers" is on
	const allowAllProviders = form.watch("allowAllProviders");

	// Get current MCP configs from form
	const mcpConfigs = form.watch("mcpConfigs") || [];

	// Watch budget/rate-limit fields for conditional rendering of reset buttons
	const watchedBudgets = form.watch("budgets");
	const watchedTokenMaxLimit = form.watch("tokenMaxLimit");
	const watchedRequestMaxLimit = form.watch("requestMaxLimit");
	const watchedTokenResetDuration = form.watch("tokenResetDuration");
	const watchedRequestResetDuration = form.watch("requestResetDuration");
	const watchedBudgetCalendarAligned = form.watch("budgetCalendarAligned");

	// Calendar alignment is VK-wide and applies to both budgets and rate limits: show the
	// toggle when any configured budget or rate-limit uses a calendar-alignable duration.
	const hasAnyAlignableBudget =
		watchedBudgets &&
		watchedBudgets.length > 0 &&
		watchedBudgets.some((b) => b.max_limit !== undefined && b.max_limit !== null && supportsCalendarAlignment(b.reset_duration || "1M"));
	const hasAnyAlignableRateLimit =
		(watchedTokenMaxLimit !== undefined && watchedTokenMaxLimit !== null && supportsCalendarAlignment(watchedTokenResetDuration || "1h")) ||
		(watchedRequestMaxLimit !== undefined &&
			watchedRequestMaxLimit !== null &&
			supportsCalendarAlignment(watchedRequestResetDuration || "1h"));
	const showCalendarAlignToggle = hasAnyAlignableBudget || hasAnyAlignableRateLimit;

	// Build a default provider config row (all models, all keys, no limits)
	const makeDefaultProviderConfig = (provider: string) => ({
		provider: provider,
		weight: undefined as number | undefined, // undefined = excluded from weighted routing until user sets a weight
		allowed_models: ["*"],
		blacklisted_models: [] as string[],
		key_ids: ["*"],
	});

	// A row is "untouched" if it still carries only the auto-added defaults (no budget/limit/restriction).
	// Turning "Allow all providers" off keeps customized rows and drops these.
	const isDefaultProviderConfig = (config: (typeof providerConfigs)[number]) =>
		(config.allowed_models || []).length === 1 &&
		config.allowed_models?.[0] === "*" &&
		(config.blacklisted_models || []).length === 0 &&
		(config.key_ids || []).length === 1 &&
		config.key_ids?.[0] === "*" &&
		(config.budgets || []).length === 0 &&
		!config.rate_limit &&
		(config.model_budgets || []).length === 0 &&
		config.weight === undefined;

	// Handle adding a new provider configuration
	const handleAddProvider = (provider: string) => {
		const existingConfig = providerConfigs.find((config) => config.provider === provider);
		if (existingConfig) {
			toast.error("This provider is already configured");
			return;
		}

		form.setValue("providerConfigs", [...providerConfigs, makeDefaultProviderConfig(provider)], {
			shouldDirty: true,
		});
	};

	// Handle removing a provider configuration
	const handleRemoveProvider = (index: number) => {
		// Removing a provider while "Allow all providers" is on means "all except this one":
		// turn the flag off so the remaining rows become the explicit allowlist.
		if (form.getValues("allowAllProviders")) {
			form.setValue("allowAllProviders", false, { shouldDirty: true });
		}
		const updatedConfigs = (form.getValues("providerConfigs") || []).filter((_, i) => i !== index);
		form.setValue("providerConfigs", updatedConfigs, { shouldDirty: true });
	};

	// Handle the "Allow all providers" toggle. When turned on, every configured provider is
	// materialized as a row (via the effect below) so budgets/limits can be set or a provider
	// excluded. When turned off, untouched default rows are dropped and customized ones kept.
	const handleAllowAllProvidersChange = (checked: boolean) => {
		form.setValue("allowAllProviders", checked, { shouldDirty: true });
		if (!checked) {
			const kept = (form.getValues("providerConfigs") || []).filter((config) => !isDefaultProviderConfig(config));
			form.setValue("providerConfigs", kept, { shouldDirty: true });
		}
	};

	// While "Allow all providers" is on, keep a row for every configured provider so they are
	// visible and can be given budgets or excluded. Providers added later show up here too.
	// Reads current configs via getValues (not a dep) so this converges in one pass without looping.
	useEffect(() => {
		if (!allowAllProviders || availableProviders.length === 0) return;
		const current = form.getValues("providerConfigs") || [];
		const missing = availableProviders.filter((p) => p.name && !current.some((c) => c.provider === p.name));
		if (missing.length === 0) return;
		const additions = missing.map((p) => makeDefaultProviderConfig(p.name));
		form.setValue("providerConfigs", [...current, ...additions], { shouldDirty: false });
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [allowAllProviders, availableProviders]);

	// Handle adding a new MCP client configuration
	const handleAddMCPClient = (mcpClientName: string) => {
		const existingConfig = mcpConfigs.find((config) => config.mcp_client_name === mcpClientName);
		if (existingConfig) {
			toast.error("This MCP client is already configured");
			return;
		}

		const newConfig = {
			mcp_client_name: mcpClientName,
			tools_to_execute: ["*"],
		};

		form.setValue("mcpConfigs", [...mcpConfigs, newConfig], {
			shouldDirty: true,
		});
	};

	// Handle removing an MCP client configuration
	const handleRemoveMCPClient = (index: number) => {
		const updatedConfigs = mcpConfigs.filter((_, i) => i !== index);
		form.setValue("mcpConfigs", updatedConfigs, { shouldDirty: true });
	};

	// Handle updating MCP client configuration
	const handleUpdateMCPConfig = (index: number, field: keyof (typeof mcpConfigs)[0], value: any) => {
		const updatedConfigs = [...mcpConfigs];
		updatedConfigs[index] = { ...updatedConfigs[index], [field]: value };
		form.setValue("mcpConfigs", updatedConfigs, { shouldDirty: true });
	};

	const [showCalendarAlignWarning, setShowCalendarAlignWarning] = useState(false);
	const [showReassignTeamWarning, setShowReassignTeamWarning] = useState(false);
	const [pendingTeamId, setPendingTeamId] = useState<string | null>(null);
	const [showRotateWarning, setShowRotateWarning] = useState(false);
	const [showBudgetResetPrompt, setShowBudgetResetPrompt] = useState(false);
	const [pendingBudgetResetData, setPendingBudgetResetData] = useState<FormData | null>(null);
	const [pendingBudgetUsageWarning, setPendingBudgetUsageWarning] = useState<BudgetUsageWarning | null>(null);

	const handleCalendarAlignedChange = (checked: boolean) => {
		if (checked && isEditing) {
			// Show warning when enabling on an existing VK
			setShowCalendarAlignWarning(true);
		} else {
			form.setValue("budgetCalendarAligned", checked, { shouldDirty: true });
		}
	};

	const clearVirtualKeyBudget = () => {
		form.setValue("budgets", [], { shouldDirty: true });
		form.setValue("budgetCalendarAligned", false, { shouldDirty: true });
	};

	const clearVirtualKeyRateLimits = () => {
		form.setValue("tokenMaxLimit", undefined, { shouldDirty: true });
		form.setValue("tokenResetDuration", "1h", { shouldDirty: true });
		form.setValue("requestMaxLimit", undefined, { shouldDirty: true });
		form.setValue("requestResetDuration", "1h", { shouldDirty: true });
	};

	// Build a request rate-limit payload from the form's rate-limit fields. Returns the field
	// values when a limit is set, {} to clear an existing rate limit (removal), or undefined.
	const normalizeRateLimit = (
		rl:
			| { token_max_limit?: number; token_reset_duration?: string; request_max_limit?: number; request_reset_duration?: string }
			| undefined,
		hadExisting: boolean,
	) => {
		const hasToken = rl?.token_max_limit !== undefined;
		const hasRequest = rl?.request_max_limit !== undefined;
		if (hasToken || hasRequest) {
			return {
				token_max_limit: rl?.token_max_limit ?? null,
				token_reset_duration: hasToken ? rl?.token_reset_duration || "1h" : null,
				request_max_limit: rl?.request_max_limit ?? null,
				request_reset_duration: hasRequest ? rl?.request_reset_duration || "1h" : null,
			};
		}
		return hadExisting ? {} : undefined;
	};

	const normalizeProviderConfigs = (configs: typeof providerConfigs, existingConfigs?: VirtualKey["provider_configs"]): any[] => {
		return configs.map((config) => {
			const existingConfig = existingConfigs?.find((item) => (config.id ? item.id === config.id : item.provider === config.provider));
			return {
				...config,
				budgets: config.budgets?.filter((b): b is { id?: string; max_limit: number; reset_duration: string } => b.max_limit !== undefined),
				weight: config.weight ?? null,
				rate_limit: normalizeRateLimit(config.rate_limit, !!existingConfig?.rate_limit),
				// Full desired per-model set: drop unfilled models, keep an empty array so the
				// backend prunes any per-model budgets removed here.
				model_budgets: (config.model_budgets || [])
					.filter((mb) => mb.model_name && mb.model_name.trim() !== "")
					.map((mb) => {
						const existingMB = existingConfig?.model_budgets?.find((m) => m.model_name === mb.model_name.trim());
						return {
							model_name: mb.model_name.trim(),
							budgets: (mb.budgets || []).filter(
								(b): b is { id?: string; max_limit: number; reset_duration: string } => b.max_limit !== undefined,
							),
							rate_limit: normalizeRateLimit(mb.rate_limit, !!existingMB?.rate_limit),
						};
					})
					.filter((mb) => mb.budgets.length > 0 || mb.rate_limit !== undefined),
			};
		});
	};

	const parseResetDurationMs = (duration?: string) => {
		if (!duration) return null;
		const match = duration.match(/^(\d+(?:\.\d+)?)(ms|s|m|h|d|w|M|Q|Y)$/);
		if (!match) return null;
		const amount = Number(match[1]);
		const unit = match[2];
		const multipliers: Record<string, number> = {
			ms: 1,
			s: 1000,
			m: 60 * 1000,
			h: 60 * 60 * 1000,
			d: 24 * 60 * 60 * 1000,
			w: 7 * 24 * 60 * 60 * 1000,
			M: 30 * 24 * 60 * 60 * 1000,
			Q: 90 * 24 * 60 * 60 * 1000,
			Y: 365 * 24 * 60 * 60 * 1000,
		};
		return amount * multipliers[unit];
	};

	const formatBudgetAmount = (value: number) =>
		new Intl.NumberFormat("en-US", {
			style: "currency",
			currency: "USD",
			maximumFractionDigits: 2,
		}).format(value);

	const findBudgetUsageWarning = (
		currentBudgets: BudgetComparisonEntry[] | undefined,
		existingBudgets: BudgetComparisonEntry[] | undefined,
		scopeLabel: string,
	) => {
		const current = (currentBudgets || [])
			.filter(
				(
					budget,
				): budget is BudgetComparisonEntry & {
					max_limit: number;
					reset_duration: string;
				} => {
					return budget.max_limit !== undefined && !!budget.reset_duration;
				},
			)
			.sort((left, right) => (parseResetDurationMs(left.reset_duration) ?? 0) - (parseResetDurationMs(right.reset_duration) ?? 0));
		const existingByID = new Map((existingBudgets || []).filter((budget) => budget.id).map((budget) => [budget.id, budget]));
		const existingByDuration = new Map((existingBudgets || []).map((budget) => [budget.reset_duration, budget]));
		const reconciled: BudgetComparisonEntry[] = [];

		for (const budget of current) {
			const existing = budget.id ? existingByID.get(budget.id) : existingByDuration.get(budget.reset_duration);
			if (existing) {
				const configChanged = existing.max_limit !== budget.max_limit || existing.reset_duration !== budget.reset_duration;
				const usage = existing.current_usage ?? 0;
				if (configChanged && usage >= budget.max_limit) {
					return {
						kind: "over-limit" as const,
						message: `${scopeLabel} ${budget.reset_duration} budget has ${formatBudgetAmount(usage)} usage, which meets or exceeds the new ${formatBudgetAmount(budget.max_limit)} limit.`,
					};
				}
				// Moving the fiscal quarter moves the reset boundary under a live budget.
				// Spend is deliberately carried into the new quarter rather than forgiven,
				// so surface it before saving instead of letting the number reappear
				// unexplained against a window the operator did not think they had started.
				if (budget.reset_duration.endsWith("Q") && quarterStartOf(existing) !== quarterStartOf(budget) && usage > 0) {
					return {
						kind: "quarter-shift" as const,
						message: `${scopeLabel} quarterly budget has ${formatBudgetAmount(usage)} of usage. Changing the fiscal quarter moves the reset date and carries that spend into the new quarter.`,
					};
				}
				reconciled.push({ ...budget, current_usage: usage });
				continue;
			}

			const targetDuration = parseResetDurationMs(budget.reset_duration);
			const closestShorter = reconciled.reduce<BudgetComparisonEntry | null>((closest, candidate) => {
				const candidateDuration = parseResetDurationMs(candidate.reset_duration);
				const closestDuration = parseResetDurationMs(closest?.reset_duration);
				if (targetDuration === null || candidateDuration === null || candidateDuration >= targetDuration) {
					return closest;
				}
				if (closest === null || closestDuration === null || candidateDuration > closestDuration) {
					return candidate;
				}
				return closest;
			}, null);
			const inheritedUsage = closestShorter?.current_usage ?? 0;
			if (inheritedUsage >= budget.max_limit) {
				return {
					kind: "over-limit" as const,
					message: `${scopeLabel} ${budget.reset_duration} budget will inherit ${formatBudgetAmount(inheritedUsage)} from the ${closestShorter?.reset_duration} budget, which meets or exceeds the new ${formatBudgetAmount(budget.max_limit)} limit.`,
				};
			}
			reconciled.push({ ...budget, current_usage: inheritedUsage });
		}

		return null;
	};

	const getBudgetUsageWarning = (data: FormData) => {
		if (!isEditing || !virtualKey || isManagedByProfile) {
			return null;
		}

		const vkWarning = findBudgetUsageWarning(data.budgets, virtualKey.budgets, "Virtual key");
		if (vkWarning) {
			return vkWarning;
		}

		const existingProviderConfigs = new Map<string, NonNullable<VirtualKey["provider_configs"]>[number]>();
		(virtualKey.provider_configs || []).forEach((config) => {
			existingProviderConfigs.set(String(config.id ?? config.provider), config);
		});
		for (const config of data.providerConfigs || []) {
			const existingConfig = existingProviderConfigs.get(String(config.id ?? config.provider));
			const providerLabel = ProviderLabels[config.provider as ProviderName] ?? config.provider;
			const warning = findBudgetUsageWarning(config.budgets, existingConfig?.budgets, `${providerLabel} provider`);
			if (warning) {
				return warning;
			}
		}

		return null;
	};

	const hasBudgetResetRelevantChanges = (data: FormData) => {
		if (!isEditing || !virtualKey || isManagedByProfile) {
			return false;
		}

		const currentBudgets = (data.budgets || []).filter(
			(budget): budget is { id?: string; max_limit: number; reset_duration: string } => budget.max_limit !== undefined,
		);
		const existingBudgets = virtualKey.budgets || [];
		const hasBudgetFields =
			currentBudgets.length > 0 ||
			existingBudgets.length > 0 ||
			(data.providerConfigs || []).some((config) => (config.budgets || []).some((budget) => budget.max_limit !== undefined)) ||
			(virtualKey.provider_configs || []).some((config) => (config.budgets || []).length > 0);

		if (budgetSignature(currentBudgets) !== budgetSignature(existingBudgets)) {
			return true;
		}

		if (hasBudgetFields && data.budgetCalendarAligned !== (virtualKey.calendar_aligned ?? false)) {
			return true;
		}

		const existingProviderConfigs = new Map<string, NonNullable<VirtualKey["provider_configs"]>[number]>();
		(virtualKey.provider_configs || []).forEach((config) => {
			existingProviderConfigs.set(String(config.id ?? config.provider), config);
		});

		const currentProviderConfigs = new Map<string, NonNullable<FormData["providerConfigs"]>[number]>();
		(data.providerConfigs || []).forEach((config) => {
			currentProviderConfigs.set(String(config.id ?? config.provider), config);
		});

		const providerConfigKeys = new Set([...existingProviderConfigs.keys(), ...currentProviderConfigs.keys()]);
		for (const key of providerConfigKeys) {
			const currentSignature = budgetSignature(currentProviderConfigs.get(key)?.budgets);
			const existingSignature = budgetSignature(existingProviderConfigs.get(key)?.budgets);
			if (currentSignature !== existingSignature) {
				return true;
			}
		}

		return false;
	};

	const handleRotateVirtualKey = async () => {
		if (!virtualKey) return;
		if (!hasUpdateAccess) {
			toast.error("You don't have permission to perform this action");
			return;
		}
		try {
			const result = await rotateVirtualKey(virtualKey.id).unwrap();
			const graceUntil = result.virtual_key?.previous_value_expires_at;
			toast.success(
				graceUntil
					? `Virtual key rotated successfully. The previous key remains valid until ${new Date(graceUntil).toLocaleString()}.`
					: "Virtual key rotated successfully",
			);
			setShowRotateWarning(false);
			onSave();
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};

	const submitVirtualKeyForm = async (data: FormData, resetBudgetUsage = false) => {
		if (!canSubmit) {
			toast.error("You don't have permission to perform this action");
			return;
		}
		try {
			// Managed VKs only allow name + description updates; all other fields are owned by the access profile.
			if (isManagedByProfile && virtualKey) {
				await updateVirtualKey({
					vkId: virtualKey.id,
					data: {
						name: data.name,
						description: data.description,
					},
				}).unwrap();
				toast.success("Virtual key updated");
				onSave();
				return;
			}

			// Normalize provider configs to ensure weights are numbers and handle budget/rate limits
			const normalizedProviderConfigs = data.providerConfigs
				? normalizeProviderConfigs(data.providerConfigs, virtualKey?.provider_configs)
				: [];

			// User assignment lives on its own endpoint (POST/DELETE /virtual-keys/{id}/users) —
			// the same one the user detail sheet uses — so it is applied alongside the VK payload,
			// never inside it. Team/customer are mutually exclusive with it and get cleared.
			const targetUserId = data.entityType === "user" ? (data.userId || "").trim() : "";
			const clearsEntity = data.entityType === "none" || data.entityType === "user";

			if (isEditing && virtualKey) {
				// Update existing virtual key
				// Only include expires_at when the user actually changed the expiry field
				// (a timestamp sets it, "" clears it). Pre-filled defaultValues are not dirty,
				// so an unchanged expired key won't resend its old expired timestamp and
				// cause the backend to reject the edit.
				const expiryChanged = !!form.formState.dirtyFields.expiresAt;
				const expiryPayload = expiryChanged
					? data.expiresAt
						? { expires_at: new Date(data.expiresAt).toISOString() }
						: virtualKey?.expires_at
							? { expires_at: "" }
							: {}
					: {};

				const updateData: UpdateVirtualKeyRequest = {
					name: data.name,
					description: data.description,
					provider_configs: normalizedProviderConfigs,
					mcp_configs: data.mcpConfigs,
					team_id: data.entityType === "team" && data.teamId && data.teamId.trim() !== "" ? data.teamId : clearsEntity ? null : undefined,
					customer_id:
						data.entityType === "customer" && data.customerId && data.customerId.trim() !== ""
							? data.customerId
							: clearsEntity
								? null
								: undefined,
					is_active: data.isActive,
					calendar_aligned: data.budgetCalendarAligned,
					allow_all_providers: data.allowAllProviders,
					reset_budget_usage: resetBudgetUsage,
					...expiryPayload,
				};

				// Add budgets if enabled
				const validBudgets = (data.budgets || []).filter(
					(b): b is { id?: string; max_limit: number; reset_duration: string; reset_config?: { quarter_start_month?: number } } =>
						b.max_limit !== undefined,
				);
				const hadBudget = virtualKey.budgets && virtualKey.budgets.length > 0;
				if (validBudgets.length > 0) {
					updateData.budgets = validBudgets;
				} else if (hadBudget) {
					updateData.budgets = [];
				}

				// Add rate limit if enabled
				const hadRateLimit = !!virtualKey.rate_limit;
				const hasTokenMaxLimit = data.tokenMaxLimit !== undefined;
				const hasRequestMaxLimit = data.requestMaxLimit !== undefined;
				const hasRateLimit = hasTokenMaxLimit || hasRequestMaxLimit;
				if (hasRateLimit) {
					updateData.rate_limit = {
						token_max_limit: data.tokenMaxLimit ?? null,
						token_reset_duration: hasTokenMaxLimit ? data.tokenResetDuration || "1h" : null,
						request_max_limit: data.requestMaxLimit ?? null,
						request_reset_duration: hasRequestMaxLimit ? data.requestResetDuration || "1h" : null,
					};
				} else if (hadRateLimit) {
					updateData.rate_limit = {};
				}

				await updateVirtualKey({
					vkId: virtualKey.id,
					data: updateData,
				}).unwrap();

				// Apply the user assignment after the VK payload, so a key moving from a team to a
				// user has its team cleared before the attach lands.
				if (targetUserId !== assignedUserId) {
					if (assignedUserId) {
						await detachVirtualKeyUser({ vkId: virtualKey.id, userId: assignedUserId }).unwrap();
					}
					if (targetUserId) {
						await attachVirtualKeyUsers({
							vkId: virtualKey.id,
							data: { user_ids: [targetUserId], preserve_usage: false },
						}).unwrap();
					}
				}
				await reconcileVmcpAssignments(virtualKey.id);
				toast.success("Virtual key updated successfully");
			} else {
				// Create new virtual key
				const createData: CreateVirtualKeyRequest = {
					name: data.name,
					description: data.description || undefined,
					provider_configs: normalizedProviderConfigs,
					mcp_configs: data.mcpConfigs,
					team_id: data.entityType === "team" && data.teamId && data.teamId.trim() !== "" ? data.teamId : undefined,
					customer_id: data.entityType === "customer" && data.customerId && data.customerId.trim() !== "" ? data.customerId : undefined,
					is_active: data.isActive,
					// VK-level setting that governs both budget and rate-limit calendar alignment.
					calendar_aligned: data.budgetCalendarAligned,
					allow_all_providers: data.allowAllProviders,
					// Optional expiry: send as UTC ISO string, or omit for no expiry
					...(data.expiresAt ? { expires_at: new Date(data.expiresAt).toISOString() } : {}),
				};

				// Add budgets if enabled
				const validBudgets = (data.budgets || []).filter(
					(b): b is { id?: string; max_limit: number; reset_duration: string; reset_config?: { quarter_start_month?: number } } =>
						b.max_limit !== undefined,
				);
				if (validBudgets.length > 0) {
					createData.budgets = validBudgets;
				}

				// Add rate limit if enabled
				const hasTokenMaxLimit = data.tokenMaxLimit !== undefined;
				const hasRequestMaxLimit = data.requestMaxLimit !== undefined;
				if (hasTokenMaxLimit || hasRequestMaxLimit) {
					createData.rate_limit = {
						token_max_limit: data.tokenMaxLimit,
						token_reset_duration: hasTokenMaxLimit ? data.tokenResetDuration || "1h" : undefined,
						request_max_limit: data.requestMaxLimit,
						request_reset_duration: hasRequestMaxLimit ? data.requestResetDuration || "1h" : undefined,
					};
				}

				const created = await createVirtualKey(createData).unwrap();
				if (targetUserId) {
					// The key exists at this point; surface the assignment failure separately so the
					// user knows the key was created but is unassigned.
					try {
						await attachVirtualKeyUsers({
							vkId: created.virtual_key.id,
							data: { user_ids: [targetUserId], preserve_usage: false },
						}).unwrap();
					} catch (error) {
						toast.error("Virtual key created, but assigning it to the user failed", {
							description: getErrorMessage(error),
						});
						onSave();
						return;
					}
				}
				try {
					await reconcileVmcpAssignments(created.virtual_key.id);
				} catch (error) {
					toast.error("Virtual key created, but assigning Virtual MCPs failed", { description: getErrorMessage(error) });
					onSave();
					return;
				}
				toast.success("Virtual key created successfully");
			}

			onSave();
		} catch (error: any) {
			if (error?.status === 409) {
				form.setError("name", { message: getErrorMessage(error) });
				return;
			}
			toast.error(getErrorMessage(error));
		}
	};

	// Handle form submission
	const onSubmit = async (data: FormData) => {
		if (hasBudgetResetRelevantChanges(data)) {
			setPendingBudgetResetData(data);
			setPendingBudgetUsageWarning(getBudgetUsageWarning(data));
			setShowBudgetResetPrompt(true);
			return;
		}

		await submitVirtualKeyForm(data, false);
	};

	const handleBudgetResetChoice = async (resetBudgetUsage: boolean) => {
		if (!pendingBudgetResetData) return;
		const data = pendingBudgetResetData;
		setPendingBudgetResetData(null);
		setPendingBudgetUsageWarning(null);
		setShowBudgetResetPrompt(false);
		await submitVirtualKeyForm(data, resetBudgetUsage);
	};

	return (
		<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
			<SheetContent
				className="flex w-full flex-col gap-4 overflow-x-hidden p-0 pt-4"
				data-testid="vk-sheet-content"
				onInteractOutside={(e) => e.preventDefault()}
				onEscapeKeyDown={() => handleClose()}
			>
				<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle className="flex items-center gap-2">{isEditing ? virtualKey?.name : "Create Virtual Key"}</SheetTitle>
					<SheetDescription>
						{isEditing
							? "Update the virtual key configuration and permissions."
							: "Create a new virtual key with specific permissions, budgets, and rate limits."}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-4 md:px-8">
							{isManagedByProfile && (
								<>
									<Alert variant="info">
										<Lock className="h-4 w-4" />
										<AlertDescription>
											{isEditing ? (
												<>
													This virtual key belongs to an access profile. What it can reach, and what it can spend, are the profile&apos;s:
													the key itself carries only a name and a description.
												</>
											) : (
												<>
													This virtual key will be managed by your access profile
													{vkCreationPolicy?.profile_name ? (
														<>
															{" "}
															<span className="font-medium">{vkCreationPolicy.profile_name}</span>
														</>
													) : null}
													. Set a name and description; providers, budgets, rate limits, and MCP access are applied from the profile on
													creation.
												</>
											)}
										</AlertDescription>
									</Alert>
									{isEditing && <ManagedVirtualKeyActions managingProfile={managingProfile} />}
								</>
							)}

							{isTeamLocked && !isManagedByProfile && (
								<Alert variant="info">
									<Users className="h-4 w-4" />
									<AlertDescription>
										Creating this virtual key under team <span className="font-medium">{attachedTeam?.name ?? attachedTeamId}</span>. Team
										assignment is pre-set; all other fields are editable.
									</AlertDescription>
								</Alert>
							)}

							{/* Basic Information */}
							<div className="space-y-4">
								<FormField
									control={form.control}
									name="name"
									render={({ field }) => (
										<FormItem>
											<FormLabel>Name *</FormLabel>
											<FormControl>
												<Input placeholder="e.g., Production API Key" data-testid="vk-name-input" {...field} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>

								<FormField
									control={form.control}
									name="description"
									render={({ field }) => (
										<FormItem>
											<FormLabel>Description</FormLabel>
											<FormControl>
												<Textarea placeholder="This key is used for..." data-testid="vk-description-input" {...field} rows={3} />
											</FormControl>
											<FormMessage />
										</FormItem>
									)}
								/>
							</div>
							{!isManagedByProfile && (
								<div className="space-y-4">
									<div className="space-y-4">
										<FormField
											control={form.control}
											name="isActive"
											render={({ field }) => (
												<FormItem>
													<Toggle label="Is this key active?" val={field.value} setVal={field.onChange} data-testid="vk-is-active-toggle" />
												</FormItem>
											)}
										/>
										<FormField
											control={form.control}
											name="expiresAt"
											render={({ field }) => <ExpiryPickerField value={field.value} onChange={field.onChange} />}
										/>
									</div>
									{/* Provider Configurations */}
									<div className="space-y-2">
										<div className="flex items-center gap-2">
											<Label className="text-sm font-medium">Provider Configurations</Label>
											<TooltipProvider>
												<Tooltip>
													<TooltipTrigger asChild>
														<span>
															<Info className="text-muted-foreground h-3 w-3" />
														</span>
													</TooltipTrigger>
													<TooltipContent className="max-w-sm">
														<p>
															Configure which providers this virtual key can use and their specific settings. Leave empty to block all
															providers. Add providers to allow them.
														</p>
													</TooltipContent>
												</Tooltip>
											</TooltipProvider>
										</div>

										{/* Allow all providers */}
										<FormField
											control={form.control}
											name="allowAllProviders"
											render={({ field }) => (
												<FormItem>
													<div className="flex w-full items-center justify-between gap-2 py-2 text-sm">
														<div className="flex items-center gap-1.5">
															<span>Allow all providers</span>
															<TooltipProvider>
																<Tooltip>
																	<TooltipTrigger asChild>
																		<span>
																			<Info className="text-muted-foreground h-3 w-3" />
																		</span>
																	</TooltipTrigger>
																	<TooltipContent className="max-w-sm">
																		<p>
																			Grant access to every provider, including ones added later. Set budgets or limits on specific
																			providers below, or remove a provider to allow all except that one.
																		</p>
																	</TooltipContent>
																</Tooltip>
															</TooltipProvider>
														</div>
														<Switch
															checked={field.value}
															onCheckedChange={handleAllowAllProvidersChange}
															data-testid="vk-allow-all-providers-toggle"
														/>
													</div>
												</FormItem>
											)}
										/>

										{/* Add Provider Dropdown */}
										<div className="flex gap-2">
											<Select
												value={selectedProvider}
												onValueChange={(provider) => {
													if (provider === "__manage_providers__") {
														navigate({ to: "/workspace/providers" });
														setSelectedProvider("");
														return;
													}
													handleAddProvider(provider);
													setSelectedProvider(""); // Reset to placeholder state
												}}
											>
												<SelectTrigger className="flex-1" data-testid="vk-provider-select">
													<SelectValue placeholder="Select a provider to add" />
												</SelectTrigger>
												<SelectContent>
													{(() => {
														// Filter out already configured providers
														const unconfiguredProviders = availableProviders.filter(
															(provider) => !providerConfigs.some((config) => config.provider === provider.name),
														);

														if (unconfiguredProviders.length === 0) {
															return (
																<SelectItem
																	value="__manage_providers__"
																	className="text-muted-foreground hover:text-foreground"
																	data-testid="vk-provider-config-link"
																>
																	<span>
																		No providers left to configure. <span className="text-primary font-medium underline">Click to add</span>
																	</span>
																</SelectItem>
															);
														}

														// Separate base providers and custom providers
														const baseProviders = unconfiguredProviders.filter((provider) => !provider.custom_provider_config);
														const customProviders = unconfiguredProviders.filter((provider) => provider.custom_provider_config);

														return (
															<>
																{/* Base providers first */}
																{baseProviders
																	.filter((p) => p.name)
																	.map((provider, index) => (
																		<SelectItem key={`base-${index}`} value={provider.name}>
																			<RenderProviderIcon provider={provider.name as KnownProvider} size="sm" className="h-4 w-4" />
																			{ProviderLabels[provider.name as ProviderName]}
																		</SelectItem>
																	))}

																{/* Custom providers second */}
																{customProviders
																	.filter((p) => p.name)
																	.map((provider, index) => (
																		<SelectItem key={`custom-${index}`} value={provider.name}>
																			<RenderProviderIcon
																				provider={provider.custom_provider_config?.base_provider_type || (provider.name as KnownProvider)}
																				size="sm"
																				className="h-4 w-4"
																			/>
																			{provider.name}
																		</SelectItem>
																	))}
															</>
														);
													})()}
												</SelectContent>
											</Select>
										</div>

										{/* Provider Configurations Table */}
										{providerConfigs.length > 0 && (
											<div className="space-y-2.5">
												{providerConfigs.map((config, index) => {
													const providerConfig = availableProviders.find((provider) => provider.name === config.provider);
													const providerLabel = providerConfig?.custom_provider_config
														? providerConfig.name
														: ProviderLabels[config.provider as ProviderName] || config.provider;
													const iconProvider = (providerConfig?.custom_provider_config?.base_provider_type ||
														config.provider) as ProviderIconType;
													const providerKeys = availableKeys.filter((key) => key.provider === config.provider);
													return (
														<ProviderConfigCard
															key={config.provider}
															index={index}
															testIdPrefix="vk"
															providerLabel={providerLabel}
															iconProvider={iconProvider}
															providerKeys={providerKeys}
															showModelBudgets
															onRemove={() => handleRemoveProvider(index)}
															value={{
																providerName: config.provider,
																allowedModels: config.allowed_models || [],
																blacklistedModels: config.blacklisted_models || [],
																weight: config.weight,
																keyIds: config.key_ids || [],
																budgets: config.budgets || [],
																rateLimit: config.rate_limit ?? null,
																modelBudgets: (config.model_budgets || []).map((mb) => ({
																	model_name: mb.model_name,
																	budgets: mb.budgets || [],
																	rate_limit: mb.rate_limit,
																})),
															}}
															onChange={(next) => {
																const updated = [...providerConfigs];
																updated[index] = {
																	...config,
																	allowed_models: next.allowedModels,
																	blacklisted_models: next.blacklistedModels,
																	weight: next.weight ?? undefined,
																	key_ids: next.keyIds,
																	budgets: next.budgets.map((l) => ({
																		id: l.id,
																		max_limit: l.max_limit,
																		reset_duration: l.reset_duration,
																		reset_config: l.reset_config,
																	})),
																	rate_limit: next.rateLimit ?? undefined,
																	model_budgets: next.modelBudgets,
																};
																form.setValue("providerConfigs", updated, {
																	shouldDirty: true,
																});
															}}
														/>
													);
												})}
											</div>
										)}
										{/* Display validation errors for provider configurations */}
										{form.formState.errors.providerConfigs && (
											<div className="text-destructive text-sm">{form.formState.errors.providerConfigs.message}</div>
										)}
									</div>
									{/* MCP Server Configurations */}
									<MCPClientConfigsEditor
										value={mcpConfigs}
										onChange={(next) => form.setValue("mcpConfigs", next, { shouldDirty: true })}
										showDefaultsNote
									/>
									{/* Virtual MCP assignments: attach this key to Virtual MCPs (reconciled on Save).
									    For an existing key the editor stays gated until its detail (the assignment baseline)
									    loads, so an early edit can't be discarded and a failed load can't hide assignments. */}
									{isEditing && isVkDetailLoading ? (
										<div className="mt-6 space-y-2">
											<Label className="text-sm font-medium">Virtual MCP Server Configurations</Label>
											<div className="text-muted-foreground text-sm">Loading assigned Virtual MCPs...</div>
										</div>
									) : isEditing && isVkDetailError ? (
										<div className="mt-6 space-y-2">
											<Label className="text-sm font-medium">Virtual MCP Server Configurations</Label>
											<Alert variant="destructive">
												<AlertDescription className="flex items-center justify-between gap-2">
													<span>Could not load assigned Virtual MCPs.</span>
													<Button type="button" variant="outline" size="sm" onClick={() => refetchVkDetail()}>
														Retry
													</Button>
												</AlertDescription>
											</Alert>
										</div>
									) : (
										<VirtualMcpAssignmentsEditor value={assignedVmcpIds} onChange={setAssignedVmcpIds} />
									)}
									<DottedSeparator className="mt-6 mb-5" />
									{/* Budget Configuration */}
									<div className="space-y-4">
										<MultiBudgetLines
											data-testid="vk-budget-lines"
											label="Budget Configuration"
											lines={form.watch("budgets") ?? []}
											onChange={(lines) => {
												form.setValue("budgets", lines, { shouldDirty: true });
											}}
											onReset={clearVirtualKeyBudget}
											showReset={isEditing && !!(virtualKey?.budgets?.length || (watchedBudgets && watchedBudgets.length > 0))}
										/>

										{isEditing && !isManagedByProfile && persistedOverrideBudgets.length > 0 ? (
											<div className="space-y-3 rounded-sm border p-4" data-testid="vk-budget-overrides-section">
												<div>
													<h4 className="text-sm font-medium">Budget Overrides</h4>
													<p className="text-muted-foreground text-xs">
														Add temporary capacity without changing the configured base budgets above.
													</p>
												</div>
												<div className="divide-y">
													{persistedOverrideBudgets.map(({ budget, label }) => (
														<div key={budget.id} className="flex items-center justify-between gap-4 py-3 first:pt-0 last:pb-0">
															<div className="min-w-0">
																<p className="truncate text-sm font-medium">
																	{label} · resets every {parseResetPeriod(budget.reset_duration)}
																</p>
																<p className="text-muted-foreground text-xs">
																	Base {formatCurrency(budget.max_limit)}
																	{hasActiveBudgetOverride(budget) ? ` · effective ${formatCurrency(getEffectiveBudgetLimit(budget))}` : ""}
																</p>
															</div>
															<BudgetOverrideDialog
																budget={budget}
																onSave={(data) => saveBudgetOverride(budget.id, data)}
																onRemove={() => clearBudgetOverride(budget.id)}
																disabled={!hasUpdateAccess}
																calendarAligned={virtualKey.calendar_aligned}
															/>
														</div>
													))}
												</div>
											</div>
										) : null}

										{/* Reassign team confirmation dialog */}
										<AlertDialog
											open={showReassignTeamWarning}
											onOpenChange={(open) => {
												setShowReassignTeamWarning(open);
												if (!open) {
													setPendingTeamId(null);
												}
											}}
										>
											<AlertDialogContent>
												<AlertDialogHeader>
													<AlertDialogTitle>Reassign to a different team?</AlertDialogTitle>
													<AlertDialogDescription>
														This key is currently assigned to another team. Reassigning it will move budget tracking to this team; future
														requests through this key will count against this team’s budget, not the previous one.
													</AlertDialogDescription>
												</AlertDialogHeader>
												<AlertDialogFooter>
													<AlertDialogCancel data-testid="virtual-key-reassign-cancel" onClick={() => setPendingTeamId(null)}>
														Cancel
													</AlertDialogCancel>
													<AlertDialogAction
														data-testid="virtual-key-reassign-confirm"
														onClick={() => {
															if (pendingTeamId !== null) {
																form.setValue("teamId", pendingTeamId, {
																	shouldDirty: true,
																});
																void form.trigger("entityType");
															}
															setPendingTeamId(null);
															setShowReassignTeamWarning(false);
														}}
													>
														Reassign
													</AlertDialogAction>
												</AlertDialogFooter>
											</AlertDialogContent>
										</AlertDialog>
									</div>
									{/* Rate Limiting Configuration */}
									<div className="space-y-4">
										<div className="flex items-center justify-between gap-2">
											<Label className="text-sm font-medium">Rate Limiting Configuration</Label>
											{isEditing && (virtualKey?.rate_limit || watchedTokenMaxLimit || watchedRequestMaxLimit) && (
												<Button
													type="button"
													variant="ghost"
													size="sm"
													onClick={clearVirtualKeyRateLimits}
													data-testid="vk-rate-limit-reset-button"
												>
													<RotateCcw className="h-4 w-4" />
													Reset
												</Button>
											)}
										</div>

										<FormField
											control={form.control}
											name="tokenMaxLimit"
											render={({ field }) => (
												<FormItem>
													<NumberAndSelect
														id="tokenMaxLimit"
														labelClassName="font-normal"
														label="Maximum Tokens"
														value={field.value}
														selectValue={form.watch("tokenResetDuration") || "1h"}
														onChangeNumber={(value) => {
															field.onChange(value);
														}}
														onChangeSelect={(value) =>
															form.setValue("tokenResetDuration", value, {
																shouldDirty: true,
															})
														}
														options={resetDurationOptions}
													/>
													<FormMessage />
												</FormItem>
											)}
										/>

										<FormField
											control={form.control}
											name="requestMaxLimit"
											render={({ field }) => (
												<FormItem>
													<NumberAndSelect
														id="requestMaxLimit"
														labelClassName="font-normal"
														label="Maximum Requests"
														value={field.value}
														selectValue={form.watch("requestResetDuration") || "1h"}
														onChangeNumber={(value) => {
															field.onChange(value);
														}}
														onChangeSelect={(value) =>
															form.setValue("requestResetDuration", value, {
																shouldDirty: true,
															})
														}
														options={resetDurationOptions}
													/>
													<FormMessage />
												</FormItem>
											)}
										/>
									</div>
									{/* Calendar alignment: VK-wide setting that applies to both budgets and rate limits */}
									{showCalendarAlignToggle && (
										<div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
											<div className="space-y-0.5">
												<Label htmlFor="vk-budget-calendar-aligned-toggle" className="text-sm font-normal">
													Align to calendar cycle
												</Label>
												<p id="vk-budget-calendar-aligned-description" className="text-muted-foreground text-xs">
													Reset budgets and rate limits at the start of each period (e.g. 1st of month) instead of rolling from creation
													date. Quarterly budgets always align to fiscal quarter starts. Applies to durations of a day or longer.
												</p>
											</div>
											<Switch
												id="vk-budget-calendar-aligned-toggle"
												aria-describedby="vk-budget-calendar-aligned-description"
												checked={watchedBudgetCalendarAligned}
												onCheckedChange={handleCalendarAlignedChange}
												data-testid="vk-budget-calendar-aligned-toggle"
											/>
										</div>
									)}

									{/* Warning dialog shown when enabling calendar alignment on an existing VK */}
									<AlertDialog open={showCalendarAlignWarning} onOpenChange={setShowCalendarAlignWarning}>
										<AlertDialogContent>
											<AlertDialogHeader>
												<AlertDialogTitle>Reset budget and rate-limit usage?</AlertDialogTitle>
												<AlertDialogDescription>
													Enabling calendar alignment will reset budget usage to <span className="font-semibold">$0.00</span> and
													token/request rate-limit counters to <span className="font-semibold">0</span> for this virtual key, then snap each
													reset date to the start of its current period (e.g. start of day, week, month, or year). The usage reset cannot be
													undone, but calendar alignment can be turned off later. This will take effect when you save.
												</AlertDialogDescription>
											</AlertDialogHeader>
											<AlertDialogFooter>
												<AlertDialogCancel data-testid="vk-calendar-align-cancel-btn">Cancel</AlertDialogCancel>
												<AlertDialogAction
													data-testid="vk-calendar-align-enable-btn"
													onClick={() => {
														form.setValue("budgetCalendarAligned", true, {
															shouldDirty: true,
														});
														setShowCalendarAlignWarning(false);
													}}
												>
													Enable Calendar Alignment
												</AlertDialogAction>
											</AlertDialogFooter>
										</AlertDialogContent>
									</AlertDialog>
									<DottedSeparator className="my-6" />

									{/* Entity Assignment */}
									<div className="space-y-4">
										<Label className="text-sm font-medium">Entity Assignment</Label>

										<div className="grid grid-cols-1 items-start gap-2 md:grid-cols-2">
											<FormField
												control={form.control}
												name="entityType"
												render={({ field }) => (
													<FormItem>
														<FormLabel className="font-normal">Assignment Type</FormLabel>
														<ComboboxSelect
															options={[
																{ value: "none", label: "No Assignment" },
																{ value: "team", label: "Assign to Team" },
																{
																	value: "customer",
																	label: "Assign to Customer",
																},
																// Enterprise-only; also kept visible when the VK is already
																// user-assigned so the current state is never mislabelled.
																...(UserPicker || field.value === "user" ? [{ value: "user", label: "Assign to User" }] : []),
															]}
															value={field.value}
															onValueChange={(value) => {
																const val = value ?? "none";
																field.onChange(val);
																// Switching type clears the other ids and lets the user pick;
																// there is no entity list loaded to default from.
																// Defer validation until submit or until an entity is chosen,
																// eager trigger left a stuck refine error on entityType.
																form.setValue("teamId", "", { shouldDirty: true });
																form.setValue("customerId", "", { shouldDirty: true });
																form.setValue("userId", "", { shouldDirty: true });
																form.clearErrors(["entityType", "teamId", "customerId", "userId"]);
															}}
															disabled={isTeamLocked}
															disableSearch
															hideClear
															className="h-9"
														/>
														<FormMessage />
													</FormItem>
												)}
											/>
											{form.watch("entityType") === "team" && (
												<FormField
													control={form.control}
													name="teamId"
													render={({ field }) => (
														<FormItem>
															<FormLabel className="font-normal">Select Team</FormLabel>
															<TeamSelector
																value={field.value || ""}
																onChange={(newVal) => {
																	if (isEditing && virtualKey?.team_id && newVal && newVal !== virtualKey.team_id) {
																		setPendingTeamId(newVal);
																		setShowReassignTeamWarning(true);
																	} else {
																		field.onChange(newVal);
																		// Refine error lives on entityType; re-run so a valid
																		// team selection clears the assignment-type message.
																		void form.trigger("entityType");
																	}
																}}
																// The already-assigned team may fall outside the
																// selector's first page, so seed its label from the
																// team embedded on the virtual key itself.
																fallbackOption={
																	field.value
																		? {
																				value: field.value,
																				label: field.value === virtualKey?.team_id ? (virtualKey?.team?.name ?? field.value) : field.value,
																			}
																		: null
																}
																disabled={isTeamLocked}
																triggerClassName="h-9"
															/>
															<FormMessage />
														</FormItem>
													)}
												/>
											)}

											{form.watch("entityType") === "customer" && (
												<FormField
													control={form.control}
													name="customerId"
													render={({ field }) => (
														<FormItem>
															<FormLabel className="font-normal">Select Customer</FormLabel>
															<CustomerSelector
																value={field.value || ""}
																onChange={(val) => {
																	field.onChange(val);
																	void form.trigger("entityType");
																}}
																fallbackOption={
																	field.value
																		? {
																				value: field.value,
																				label:
																					field.value === virtualKey?.customer_id
																						? (virtualKey?.customer?.name ?? field.value)
																						: field.value,
																			}
																		: null
																}
																triggerClassName="h-9"
															/>
															<FormMessage />
														</FormItem>
													)}
												/>
											)}

											{form.watch("entityType") === "user" && UserPicker && (
												<FormField
													control={form.control}
													name="userId"
													render={({ field }) => (
														<FormItem>
															<FormLabel className="font-normal">Select User</FormLabel>
															<UserPicker
																value={field.value || ""}
																onChange={(val) => {
																	field.onChange(val);
																	void form.trigger("entityType");
																}}
																// The attached user may fall outside the picker's first
																// page; seed the label resolved from the association.
																fallbackOption={
																	field.value
																		? {
																				value: field.value,
																				label: field.value === assignedUserId ? assignedUserLabel : field.value,
																			}
																		: null
																}
																triggerClassName="h-9"
															/>
															<FormMessage />
														</FormItem>
													)}
												/>
											)}
										</div>
										{form.watch("entityType") === "user" && (
											<p className="text-muted-foreground text-xs">
												A virtual key can be assigned to only one user. If that user has an access profile, the key is adopted into it: it
												keeps working, but its providers, budgets, rate limits and MCP access are discarded and replaced by the
												profile&apos;s. This cannot be undone: a key inside a profile can be deleted, never released.
											</p>
										)}
									</div>
								</div>
							)}
						</div>
						<AlertDialog open={showRotateWarning} onOpenChange={setShowRotateWarning}>
							<AlertDialogContent>
								<AlertDialogHeader>
									<AlertDialogTitle>Rotate virtual key?</AlertDialogTitle>
									<AlertDialogDescription>
										This will replace the secret value for &quot;
										{virtualKey?.name}&quot;. The key ID, budgets, rate limits, provider permissions, MCP access, and assignments stay the
										same. The previous key value stops working immediately unless a rotation cooldown is configured, in which case it
										remains valid until the cooldown ends.
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel data-testid="vk-rotate-cancel-btn">Cancel</AlertDialogCancel>
									<AlertDialogAction onClick={handleRotateVirtualKey} disabled={isRotating} data-testid="vk-rotate-confirm-btn">
										{isRotating ? "Rotating..." : "Rotate Key"}
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
						<AlertDialog open={showBudgetResetPrompt} onOpenChange={setShowBudgetResetPrompt}>
							<AlertDialogContent data-testid="vk-budget-reset-dialog">
								<AlertDialogHeader>
									<AlertDialogTitle>
										{pendingBudgetUsageWarning?.kind === "over-limit"
											? "Preserve over-limit usage?"
											: pendingBudgetUsageWarning?.kind === "quarter-shift"
												? "Carry usage into the new quarter?"
												: "Reset budget usage?"}
									</AlertDialogTitle>
									<AlertDialogDescription>
										{pendingBudgetUsageWarning
											? `${pendingBudgetUsageWarning.message} You can preserve usage anyway, or reset usage to 0.`
											: "You changed a budget amount, reset frequency, or calendar alignment. Reset current budget usage to 0, or preserve the existing usage counters."}
									</AlertDialogDescription>
								</AlertDialogHeader>
								<AlertDialogFooter>
									<AlertDialogCancel onClick={() => handleBudgetResetChoice(false)} data-testid="vk-budget-reset-preserve-btn">
										{pendingBudgetUsageWarning ? "Preserve Anyway" : "Preserve Usage"}
									</AlertDialogCancel>
									<AlertDialogAction onClick={() => handleBudgetResetChoice(true)} data-testid="vk-budget-reset-confirm-btn">
										Reset Usage
									</AlertDialogAction>
								</AlertDialogFooter>
							</AlertDialogContent>
						</AlertDialog>
						{isEditing && virtualKey?.config_hash && (
							<div className="px-4 md:px-8">
								<ConfigSyncAlert className="mt-2" />
							</div>
						)}
						{/* Form Footer */}
						<div className="border-border bg-card sticky bottom-0 z-10 border-t px-4 py-4 md:px-8">
							<div className="flex items-center justify-between gap-2">
								{isEditing ? (
									<Button
										type="button"
										variant="outline"
										onClick={() => setShowRotateWarning(true)}
										disabled={!hasUpdateAccess || isRotating}
										data-testid="vk-rotate-btn"
									>
										<RotateCcw className="h-4 w-4" />
										{isRotating ? "Rotating..." : "Rotate Key"}
									</Button>
								) : (
									<span />
								)}
								<div className="flex justify-end gap-2">
									<Button type="button" variant="outline" onClick={handleClose} data-testid="vk-cancel-btn">
										Cancel
									</Button>
									<TooltipProvider>
										<Tooltip>
											<TooltipTrigger asChild>
												<span className="inline-block">
													<Button
														type="submit"
														disabled={isLoading || !(form.formState.isDirty || vmcpDirty) || !canSubmit}
														data-testid="vk-save-btn"
													>
														{isLoading ? "Saving..." : isEditing ? "Update" : "Create"}
													</Button>
												</span>
											</TooltipTrigger>
											{(isLoading || !(form.formState.isDirty || vmcpDirty) || !canSubmit) && (
												<TooltipContent>
													<p>
														{!canSubmit
															? "You don't have permission to perform this action"
															: isLoading
																? "Saving..."
																: !(form.formState.isDirty || vmcpDirty)
																	? "No changes made"
																	: ""}
													</p>
												</TooltipContent>
											)}
										</Tooltip>
									</TooltipProvider>
								</div>
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}