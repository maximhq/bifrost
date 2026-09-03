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
import { useT, type MessageKey, type TranslateFn } from "@/lib/i18n";
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
import { toast } from "sonner";

const MATCH_TYPE_KEYS: Record<UserAgentMappingMatchType, MessageKey> = {
	contains: "configForms.userAgent.matchContains",
	starts_with: "configForms.userAgent.matchStartsWith",
	exact: "configForms.userAgent.matchExact",
	regex: "configForms.userAgent.matchRegex",
};

const matchTypeOptions: UserAgentMappingMatchType[] = ["contains", "starts_with", "exact", "regex"];

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
	const t = useT();
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
		const validated = validateDraft(draft, t);
		if (!validated) return;
		try {
			if (editingMappingId) {
				await updateMapping({ id: editingMappingId, data: validated }).unwrap();
				toast.success(t("configForms.userAgent.updated"));
			} else {
				await createMapping(validated).unwrap();
				toast.success(t("configForms.userAgent.added"));
			}
			handleSheetOpenChange(false);
		} catch (error) {
			toast.error(
				t(editingMappingId ? "configForms.userAgent.updateFailed" : "configForms.userAgent.addFailed", {
					error: getErrorMessage(error),
				}),
			);
		}
	};

	const handleDelete = async (id: string) => {
		try {
			await deleteMapping(id).unwrap();
			toast.success(t("configForms.userAgent.deleted"));
		} catch (error) {
			toast.error(t("configForms.userAgent.deleteFailed", { error: getErrorMessage(error) }));
		}
	};

	return (
		<div className="space-y-4">
			<div className="flex items-start justify-between gap-4">
				<div>
					<h3 className="text-lg font-semibold tracking-tight">{t("configForms.userAgent.title")}</h3>
					<p className="text-muted-foreground text-sm">{t("configForms.userAgent.description")}</p>
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
						{t("common.add")}
					</Button>
				</div>
			</div>

			<Sheet open={isSheetOpen} onOpenChange={handleSheetOpenChange}>
				<SheetContent className="p-0">
					<SheetHeader className="flex flex-col items-start px-4 pt-6 md:px-6">
						<SheetTitle>{isEditing ? t("configForms.userAgent.editMapping") : t("configForms.userAgent.addMappingTitle")}</SheetTitle>
						<SheetDescription>{t("configForms.userAgent.sheetDescription")}</SheetDescription>
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
							{t("common.cancel")}
						</Button>
						<Button type="button" onClick={handleSubmit} disabled={controlsDisabled} data-testid="user-agent-mapping-submit-btn">
							{isEditing ? t("common.saveChanges") : t("configForms.userAgent.addMapping")}
						</Button>
					</SheetFooter>
				</SheetContent>
			</Sheet>

			<Table containerClassName="rounded-sm border">
				<TableHeader>
					<TableRow>
						<TableHead>{t("configForms.userAgent.pattern")}</TableHead>
						<TableHead>{t("configForms.userAgent.matchType")}</TableHead>
						<TableHead>{t("configForms.userAgent.app")}</TableHead>
						<TableHead>{t("configForms.userAgent.logo")}</TableHead>
						<TableHead>{t("configForms.userAgent.active")}</TableHead>
						<TableHead className="w-[92px] text-right">{t("common.actions")}</TableHead>
					</TableRow>
				</TableHeader>
				<TableBody>
					{isLoading ? (
						<TableRow>
							<TableCell colSpan={6} className="text-muted-foreground py-6 text-center">
								{t("configForms.userAgent.loading")}
							</TableCell>
						</TableRow>
					) : mappings.length === 0 ? (
						<TableRow>
							<TableCell colSpan={6} className="text-muted-foreground py-6 text-center">
								{t("configForms.userAgent.empty")}
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
										<span className="text-sm">{t(MATCH_TYPE_KEYS[mapping.match_type] ?? "configForms.userAgent.matchContains")}</span>
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
											{mapping.is_active ? t("configForms.userAgent.active") : t("configForms.userAgent.inactive")}
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
														aria-label={t("configForms.userAgent.actions")}
														data-testid={`user-agent-mapping-actions-${mapping.id}`}
													>
														<MoreVertical className="h-4 w-4" />
													</Button>
												</DropdownMenuTrigger>
												<DropdownMenuContent align="end">
													<DropdownMenuItem onSelect={() => openEditSheet(mapping)} data-testid={`user-agent-mapping-edit-${mapping.id}`}>
														<Pencil className="h-4 w-4" />
														{t("common.edit")}
													</DropdownMenuItem>
													<AlertDialogTrigger asChild>
														<DropdownMenuItem variant="destructive" data-testid={`user-agent-mapping-delete-${mapping.id}`}>
															<Trash2 className="h-4 w-4" />
															{t("common.delete")}
														</DropdownMenuItem>
													</AlertDialogTrigger>
												</DropdownMenuContent>
											</DropdownMenu>
											<AlertDialogContent>
												<AlertDialogHeader>
													<AlertDialogTitle>{t("configForms.userAgent.deleteTitle")}</AlertDialogTitle>
													<AlertDialogDescription>{t("configForms.userAgent.deleteDescription")}</AlertDialogDescription>
												</AlertDialogHeader>
												<AlertDialogFooter>
													<AlertDialogCancel data-testid={`user-agent-mapping-delete-cancel-${mapping.id}`}>
														{t("common.cancel")}
													</AlertDialogCancel>
													<AlertDialogAction
														data-testid={`user-agent-mapping-delete-confirm-${mapping.id}`}
														onClick={() => handleDelete(mapping.id)}
													>
														{t("common.delete")}
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
	const t = useT();
	return (
		<div className="space-y-4">
			<div className="space-y-2">
				<label htmlFor="user-agent-mapping-pattern-input" className="text-sm font-medium">
					{t("configForms.userAgent.pattern")}
				</label>
				<Input
					id="user-agent-mapping-pattern-input"
					placeholder={t("configForms.userAgent.placeholder")}
					value={draft.pattern}
					onChange={(event) => onChange({ ...draft, pattern: event.target.value })}
					disabled={disabled}
					data-testid="user-agent-mapping-pattern-input"
				/>
			</div>
			<div className="space-y-2">
				<label htmlFor="user-agent-mapping-match-type-select" className="text-sm font-medium">
					{t("configForms.userAgent.matchType")}
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
					{t("configForms.userAgent.app")}
				</label>
				<Input
					id="user-agent-mapping-app-input"
					placeholder={t("configForms.userAgent.app")}
					value={draft.app}
					onChange={(event) => onChange({ ...draft, app: event.target.value })}
					disabled={disabled}
					data-testid="user-agent-mapping-app-input"
				/>
			</div>
			<div className="space-y-2">
				<label htmlFor="user-agent-mapping-logo-upload" className="text-sm font-medium">
					{t("configForms.userAgent.logo")}
				</label>
				<LogoInput draft={draft} onChange={onChange} disabled={disabled} />
			</div>
			<div className="flex items-center justify-between rounded-sm border p-3">
				<div>
					<p className="text-sm font-medium">{t("configForms.userAgent.active")}</p>
					<p className="text-muted-foreground text-xs">{t("configForms.userAgent.activeHint")}</p>
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
	const t = useT();
	return (
		<Select value={value} onValueChange={(next) => onChange(next as UserAgentMappingMatchType)} disabled={disabled}>
			<SelectTrigger id={id} className="w-full" data-testid="user-agent-mapping-match-type-select">
				<SelectValue />
			</SelectTrigger>
			<SelectContent>
				{matchTypeOptions.map((option) => (
					<SelectItem key={option} value={option}>
						{t(MATCH_TYPE_KEYS[option])}
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
	const t = useT();
	const dataUrl = draft.logo && draft.logo_mime ? `data:${draft.logo_mime};base64,${draft.logo}` : "";
	return (
		<div className="flex items-center gap-2">
			{dataUrl && <img src={dataUrl} alt="" className="size-7 rounded-sm border object-contain" />}
			<Button type="button" variant="outline" size="icon" disabled={disabled} asChild>
				<label aria-label={t("configForms.userAgent.uploadLogo")}>
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
								toast.error(t("configForms.userAgent.logoTooLarge"));
								event.target.value = "";
								return;
							}
							try {
								const logo = await fileToBase64(file);
								onChange({ ...draft, logo, logo_mime: file.type || "application/octet-stream" });
							} catch {
								toast.error(t("configForms.userAgent.logoReadFailed"));
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
				aria-label={t("configForms.userAgent.removeLogo")}
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

function validateDraft(draft: UserAgentMappingPayload | undefined, t: TranslateFn): UserAgentMappingPayload | null {
	if (!draft || !draft.pattern.trim() || !draft.app.trim()) {
		toast.error(t("configForms.userAgent.patternAppRequired"));
		return null;
	}
	if (draft.match_type === "regex") {
		try {
			new RegExp(draft.pattern);
		} catch {
			toast.error(t("configForms.userAgent.regexInvalid"));
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