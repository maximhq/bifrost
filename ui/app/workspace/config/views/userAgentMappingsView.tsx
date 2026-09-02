import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alertDialog";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import {
	getErrorMessage,
	type UserAgentMapping,
	type UserAgentMappingMatchType,
	type UserAgentMappingPayload,
	useCreateUserAgentMappingMutation,
	useDeleteUserAgentMappingMutation,
	useGetUserAgentMappingsQuery,
	useUpdateUserAgentMappingMutation,
} from "@/lib/store";
import { MoreVertical, Pencil, Plus, Trash2, Upload, X } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";
import i18n from "@/lib/i18n";

const matchTypeOptions: Array<{ value: UserAgentMappingMatchType; label: string }> = [
	{ value: "contains", label: "Contains" },
	{ value: "starts_with", label: "Starts with" },
	{ value: "exact", label: "Exact match" },
	{ value: "regex", label: "Regex" },
];

const emptyDraft: UserAgentMappingPayload = {
	pattern: "",
	match_type: "contains",
	app: "",
	logo: undefined,
	logo_mime: null,
	is_active: true,
};

// Cap logo uploads before base64 conversion to avoid freezing the UI and sending oversized payloads.
const MAX_LOGO_BYTES = 256 * 1024;

interface UserAgentMappingsViewProps {
	disabled?: boolean;
}

export default function UserAgentMappingsView({ disabled }: UserAgentMappingsViewProps) {
	const { t } = useTranslation("config");
	const { data, isLoading } = useGetUserAgentMappingsQuery();
	const [createMapping, { isLoading: isCreating }] = useCreateUserAgentMappingMutation();
	const [updateMapping, { isLoading: isUpdating }] = useUpdateUserAgentMappingMutation();
	const [deleteMapping, { isLoading: isDeleting }] = useDeleteUserAgentMappingMutation();
	const [draft, setDraft] = useState<UserAgentMappingPayload>(emptyDraft);
	const [editingMappingId, setEditingMappingId] = useState<string | null>(null);
	const [isSheetOpen, setIsSheetOpen] = useState(false);

	const mappings = useMemo(() => data?.mappings ?? [], [data]);
	const controlsDisabled = disabled || isCreating || isUpdating || isDeleting;
	const isEditing = Boolean(editingMappingId);

	const openAddSheet = () => {
		setEditingMappingId(null);
		setDraft(emptyDraft);
		setIsSheetOpen(true);
	};

	const openEditSheet = (mapping: UserAgentMapping) => {
		setEditingMappingId(mapping.id);
		setDraft(mappingToPayload(mapping));
		setIsSheetOpen(true);
	};

	const handleSheetOpenChange = (open: boolean) => {
		setIsSheetOpen(open);
		if (!open) {
			setEditingMappingId(null);
			setDraft(emptyDraft);
		}
	};

	const handleSubmit = async () => {
		const validated = validateDraft(draft);
		if (!validated) return;
		try {
			if (editingMappingId) {
				await updateMapping({ id: editingMappingId, data: validated }).unwrap();
				toast.success(t("userAgent.toastUpdated"));
			} else {
				await createMapping(validated).unwrap();
				toast.success(t("userAgent.toastAdded"));
			}
			handleSheetOpenChange(false);
		} catch (error) {
			toast.error(t("userAgent.toastSaveFailed", { action: editingMappingId ? t("userAgent.update") : t("userAgent.add"), error: getErrorMessage(error) }));
		}
	};

	const handleDelete = async (id: string) => {
		try {
			await deleteMapping(id).unwrap();
			toast.success(t("userAgent.toastDeleted"));
		} catch (error) {
			toast.error(t("userAgent.toastDeleteFailed", { error: getErrorMessage(error) }));
		}
	};

	return (
		<div className="space-y-4">
			<div className="flex items-start justify-between gap-4">
				<div>
					<h3 className="text-lg font-semibold tracking-tight">{t("userAgent.title")}</h3>
					<p className="text-muted-foreground text-sm">{t("userAgent.description")}</p>
				</div>
				<div className="pt-2">
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={openAddSheet}
						disabled={controlsDisabled}
						data-testid="user-agent-mapping-add-btn"
					>
						<Plus className="h-4 w-4" />
						Add
					</Button>
				</div>
			</div>

			<Sheet open={isSheetOpen} onOpenChange={handleSheetOpenChange}>
				<SheetContent className="p-0">
					<SheetHeader className="flex flex-col items-start px-4 pt-6 md:px-6">
						<SheetTitle>{isEditing ? t("userAgent.sheetEdit") : t("userAgent.sheetAdd")}</SheetTitle>
						<SheetDescription>{t("userAgent.sheetDescription")}</SheetDescription>
					</SheetHeader>
					<div className="flex-1 space-y-4 px-4 md:px-6">
						<MappingForm draft={draft} onChange={setDraft} disabled={controlsDisabled} />
					</div>
					<SheetFooter className="flex-row justify-end border-t px-4 py-4 md:px-6">
						<Button
							type="button"
							variant="outline"
							onClick={() => handleSheetOpenChange(false)}
							data-testid="user-agent-mapping-cancel-btn"
						>
							Cancel
						</Button>
						<Button type="button" onClick={handleSubmit} disabled={controlsDisabled} data-testid="user-agent-mapping-submit-btn">
							{isEditing ? t("saveChanges") : t("userAgent.addMapping")}
						</Button>
					</SheetFooter>
				</SheetContent>
			</Sheet>

			<Table containerClassName="rounded-sm border">
				<TableHeader>
					<TableRow>
						<TableHead>Pattern</TableHead>
						<TableHead>Match</TableHead>
						<TableHead>App</TableHead>
						<TableHead>Logo</TableHead>
						<TableHead>Active</TableHead>
						<TableHead className="w-[92px] text-right">Actions</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{isLoading ? (
						<TableRow>
							<TableCell colSpan={6} className="text-muted-foreground py-6 text-center">
								Loading mappings...
							</TableCell>
						</TableRow>
					) : mappings.length === 0 ? (
						<TableRow>
							<TableCell colSpan={6} className="text-muted-foreground py-6 text-center">
								No user agent mappings configured.
							</TableCell>
						</TableRow>
					) : (
						mappings.map((mapping) => {
							const logoSrc = mapping.logo && mapping.logo_mime ? `data:${mapping.logo_mime};base64,${mapping.logo}` : "";
							return (
								<TableRow key={mapping.id}>
									<TableCell className="max-w-[260px]">
										<span className="block truncate font-mono text-sm" title={mapping.pattern}>
											{mapping.pattern}
										</span>
									</TableCell>
									<TableCell>
										<span className="text-sm">{getMatchTypeLabel(mapping.match_type)}</span>
									</TableCell>
									<TableCell className="max-w-[220px]">
										<span className="block truncate text-sm" title={mapping.app}>
											{mapping.app}
										</span>
									</TableCell>
									<TableCell>
										{logoSrc ? (
											<img src={logoSrc} alt={mapping.app} className="size-7 rounded-sm border object-contain" />
										) : (
											<span className="text-muted-foreground text-sm">-</span>
										)}
									</TableCell>
									<TableCell>
										<span className={mapping.is_active ? "text-sm text-emerald-700" : "text-muted-foreground text-sm"}>
											{mapping.is_active ? t("userAgent.active") : t("userAgent.inactive")}
										</span>
									</TableCell>
									<TableCell className="text-right">
										<AlertDialog>
											<DropdownMenu>
												<DropdownMenuTrigger asChild>
													<Button
														type="button"
														variant="ghost"
														size="icon"
														disabled={controlsDisabled}
														aria-label="Mapping actions"
														data-testid={`user-agent-mapping-actions-${mapping.id}`}
													>
														<MoreVertical className="h-4 w-4" />
													</Button>
												</DropdownMenuTrigger>
												<DropdownMenuContent align="end">
													<DropdownMenuItem onSelect={() => openEditSheet(mapping)} data-testid={`user-agent-mapping-edit-${mapping.id}`}>
														<Pencil className="h-4 w-4" />
														Edit
													</DropdownMenuItem>
													<AlertDialogTrigger asChild>
														<DropdownMenuItem variant="destructive" data-testid={`user-agent-mapping-delete-${mapping.id}`}>
															<Trash2 className="h-4 w-4" />
															Delete
														</DropdownMenuItem>
													</AlertDialogTrigger>
												</DropdownMenuContent>
											</DropdownMenu>
											<AlertDialogContent>
												<AlertDialogHeader>
													<AlertDialogTitle>{t("userAgent.deleteConfirmTitle")}</AlertDialogTitle>
													<AlertDialogDescription>
														This action cannot be undone. This will permanently delete the user agent mapping.
													</AlertDialogDescription>
												</AlertDialogHeader>
												<AlertDialogFooter>
													<AlertDialogCancel data-testid={`user-agent-mapping-delete-cancel-${mapping.id}`}>Cancel</AlertDialogCancel>
													<AlertDialogAction
														data-testid={`user-agent-mapping-delete-confirm-${mapping.id}`}
														onClick={() => handleDelete(mapping.id)}
													>
														Delete
													</AlertDialogAction>
												</AlertDialogFooter>
											</AlertDialogContent>
										</AlertDialog>
									</TableCell>
								</TableRow>
							);
						})
					)}
				</TableBody>
			</Table>
		</div>
	);
}

function MappingForm({
	draft,
	onChange,
	disabled,
}: {
	draft: UserAgentMappingPayload;
	onChange: (next: UserAgentMappingPayload) => void;
	disabled?: boolean;
}) {
	return (
		<div className="space-y-4">
			<div className="space-y-2">
				<label htmlFor="user-agent-mapping-pattern-input" className="text-sm font-medium">
					Pattern
				</label>
				<Input
					id="user-agent-mapping-pattern-input"
					placeholder="User-Agent string or regex"
					value={draft.pattern}
					onChange={(event) => onChange({ ...draft, pattern: event.target.value })}
					disabled={disabled}
					data-testid="user-agent-mapping-pattern-input"
				/>
			</div>
			<div className="space-y-2">
				<label htmlFor="user-agent-mapping-match-type-select" className="text-sm font-medium">
					Match type
				</label>
				<MatchTypeSelect
					id="user-agent-mapping-match-type-select"
					value={draft.match_type}
					onChange={(matchType) => onChange({ ...draft, match_type: matchType })}
					disabled={disabled}
				/>
			</div>
			<div className="space-y-2">
				<label htmlFor="user-agent-mapping-app-input" className="text-sm font-medium">
					App
				</label>
				<Input
					id="user-agent-mapping-app-input"
					placeholder="App"
					value={draft.app}
					onChange={(event) => onChange({ ...draft, app: event.target.value })}
					disabled={disabled}
					data-testid="user-agent-mapping-app-input"
				/>
			</div>
			<div className="space-y-2">
				<label htmlFor="user-agent-mapping-logo-upload" className="text-sm font-medium">
					Logo
				</label>
				<LogoInput draft={draft} onChange={onChange} disabled={disabled} />
			</div>
			<div className="flex items-center justify-between rounded-sm border p-3">
				<div>
					<p className="text-sm font-medium">Active</p>
					<p className="text-muted-foreground text-xs">Inactive mappings are saved but ignored by detection.</p>
				</div>
				<Switch
					checked={draft.is_active}
					onCheckedChange={(checked) => onChange({ ...draft, is_active: checked })}
					disabled={disabled}
					data-testid="user-agent-mapping-active-switch"
				/>
			</div>
		</div>
	);
}

function MatchTypeSelect({
	value,
	onChange,
	disabled,
	id,
}: {
	value: UserAgentMappingMatchType;
	onChange: (value: UserAgentMappingMatchType) => void;
	disabled?: boolean;
	id?: string;
}) {
	return (
		<Select value={value} onValueChange={(next) => onChange(next as UserAgentMappingMatchType)} disabled={disabled}>
			<SelectTrigger id={id} className="w-full" data-testid="user-agent-mapping-match-type-select">
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				{matchTypeOptions.map((option) => (
					<SelectItem key={option.value} value={option.value}>
						{option.label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

function LogoInput({
	draft,
	onChange,
	disabled,
}: {
	draft: UserAgentMappingPayload;
	onChange: (next: UserAgentMappingPayload) => void;
	disabled?: boolean;
}) {
	const { t } = useTranslation("config");
	const dataUrl = draft.logo && draft.logo_mime ? `data:${draft.logo_mime};base64,${draft.logo}` : "";
	return (
		<div className="flex items-center gap-2">
			{dataUrl && <img src={dataUrl} alt="" className="size-7 rounded-sm border object-contain" />}
			<Button type="button" variant="outline" size="icon" disabled={disabled} asChild>
				<label aria-label="Upload logo">
					<Upload className="h-4 w-4" />
					<input
						id="user-agent-mapping-logo-upload"
						type="file"
						accept="image/*"
						className="hidden"
						data-testid="user-agent-mapping-logo-upload"
						onChange={async (event) => {
							const file = event.target.files?.[0];
							if (!file) return;
							if (file.size > MAX_LOGO_BYTES) {
								toast.error(t("userAgent.toastLogoTooLarge"));
								event.target.value = "";
								return;
							}
							try {
								const logo = await fileToBase64(file);
								onChange({ ...draft, logo, logo_mime: file.type || "application/octet-stream" });
							} catch {
								toast.error(t("userAgent.toastLogoReadFailed"));
							}
							event.target.value = "";
						}}
					/>
				</label>
			</Button>
			<Button
				type="button"
				variant="ghost"
				size="icon"
				disabled={disabled || !draft.logo}
				onClick={() => onChange({ ...draft, logo: undefined, logo_mime: null })}
				aria-label="Remove logo"
				data-testid="user-agent-mapping-logo-remove"
			>
				<X className="h-4 w-4" />
			</Button>
		</div>
	);
}

function mappingToPayload(mapping: UserAgentMapping): UserAgentMappingPayload {
	return {
		pattern: mapping.pattern,
		match_type: mapping.match_type,
		app: mapping.app,
		logo: mapping.logo,
		logo_mime: mapping.logo_mime ?? null,
		is_active: mapping.is_active,
	};
}

function getMatchTypeLabel(matchType: UserAgentMappingMatchType): string {
	return matchTypeOptions.find((option) => option.value === matchType)?.label ?? matchType;
}

function validateDraft(draft?: UserAgentMappingPayload): UserAgentMappingPayload | null {
	if (!draft || !draft.pattern.trim() || !draft.app.trim()) {
		toast.error(i18n.t("userAgent.toastRequired", { ns: "config" }));
		return null;
	}
	if (draft.match_type === "regex") {
		try {
			new RegExp(draft.pattern);
		} catch {
			toast.error(i18n.t("userAgent.toastInvalidRegex", { ns: "config" }));
			return null;
		}
	}
	return {
		...draft,
		pattern: draft.pattern.trim(),
		app: draft.app.trim(),
	};
}

function fileToBase64(file: File): Promise<string> {
	return new Promise((resolve, reject) => {
		const reader = new FileReader();
		reader.onload = () => {
			const value = String(reader.result ?? "");
			resolve(value.includes(",") ? value.split(",")[1] : value);
		};
		reader.onerror = () => reject(reader.error);
		reader.readAsDataURL(file);
	});
}