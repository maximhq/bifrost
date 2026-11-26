import PageTitle from "@/components/pageTitle";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { getErrorMessage, useGetVectorStoresQuery, useUpdateVectorStoreMutation } from "@/lib/store";
import type { VectorStoreInfo, VectorStoreStatus } from "@/lib/types/config";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ChevronDown, CircleAlert, Database, Info, Loader2, Share2 } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { toast } from "sonner";
import {
	buildVectorStoreUpdate,
	draftFromStore,
	followStoredValues,
	maxSizeMBError,
	retentionDaysError,
	storedValuesUnchanged,
	type StoredVectorStoreValues,
	type VectorStoreDraft,
} from "./vectorStoreSettings.utils";

const statusDotClass: Record<VectorStoreStatus, string> = {
	connected: "bg-green-500",
	failed: "bg-destructive",
	disabled: "bg-muted-foreground/40",
};

export default function VectorStoresView() {
	const { t, i18n } = useTranslation("config");
	const hasSettingsUpdateAccess = useRbac(RbacResource.Settings, RbacOperation.Update);
	const { data, isLoading, isError, error } = useGetVectorStoresQuery();
	const [updateVectorStore] = useUpdateVectorStoreMutation();
	const [isSaving, setIsSaving] = useState(false);
	// Keyed by Map: a store's name is free-form, and "constructor" would read an inherited
	// function from a plain object.
	const [drafts, setDrafts] = useState<Map<string, VectorStoreDraft>>(() => new Map());
	const storedRef = useRef<Map<string, StoredVectorStoreValues>>(new Map());
	// What each save sent, so the refetch after it can tell a draft still holding what was saved
	// from one edited while the refetch was on its way.
	const submittedRef = useRef<Map<string, VectorStoreDraft>>(new Map());

	const stores = useMemo(() => data?.vector_stores ?? [], [data]);
	const editable = !!data?.editable && hasSettingsUpdateAccess;

	// Follow the stored values, store by store: a save refetches the list, and a store whose
	// stored values did not change keeps what is being typed into it. One whose values did change
	// takes them, unless it was edited after its save was sent: that edit is kept.
	useEffect(() => {
		// Read now: the update runs at the next render, after both refs below have moved on.
		const stored = storedRef.current;
		const submitted = new Map(submittedRef.current);
		setDrafts((prev) => followStoredValues(stores, prev, stored, submitted));
		for (const store of stores) {
			if (!storedValuesUnchanged(stored.get(store.name), store)) submittedRef.current.delete(store.name);
		}
		storedRef.current = new Map(
			stores.map((store) => [store.name, { retention_days: store.retention_days, max_size_mb: store.max_size_mb }]),
		);
	}, [stores]);

	// A store that shares another's connection uses that store's settings, and the server refuses edits to it.
	const isEditable = useCallback((store: VectorStoreInfo) => editable && store.shared_with === "", [editable]);

	// Resolved in the UI's language, so a language switch recomputes them.
	const fieldErrors = useMemo(() => {
		const errors = new Map<string, { retention: string | null; maxSize: string | null }>();
		for (const store of stores) {
			const draft = drafts.get(store.name) ?? draftFromStore(store);
			errors.set(store.name, { retention: retentionDaysError(draft.retentionDays), maxSize: maxSizeMBError(draft.maxSizeMB) });
		}
		return errors;
	}, [stores, drafts, i18n.language]);

	const changes = useMemo(
		() =>
			stores.flatMap((store) => {
				const draft = drafts.get(store.name);
				const payload = draft && isEditable(store) ? buildVectorStoreUpdate(store, draft) : null;
				return draft && payload ? [{ name: store.name, draft, payload }] : [];
			}),
		[stores, drafts, isEditable],
	);
	const hasErrors = stores.some(
		(store) => isEditable(store) && (fieldErrors.get(store.name)?.retention || fieldErrors.get(store.name)?.maxSize),
	);
	const canEditAny = stores.some(isEditable);

	const handleChange = useCallback((name: string, draft: VectorStoreDraft) => {
		setDrafts((prev) => new Map(prev).set(name, draft));
	}, []);

	const handleSave = useCallback(async () => {
		if (changes.length === 0 || hasErrors) return;
		setIsSaving(true);
		try {
			for (const { name, draft, payload } of changes) {
				submittedRef.current.set(name, draft);
				try {
					await updateVectorStore({ name, data: payload }).unwrap();
					toast.success(t("vectorStores.toastSaved", { name }));
				} catch (saveError) {
					toast.error(getErrorMessage(saveError));
				}
			}
		} finally {
			setIsSaving(false);
		}
	}, [changes, hasErrors, t, updateVectorStore]);

	return (
		<div className="mx-auto w-full max-w-4xl space-y-4 px-4 py-6 md:px-0" data-testid="vector-stores-view">
			<PageTitle title={t("vectorStores.title")}>{t("vectorStores.description")}</PageTitle>

			{isLoading && (
				<div className="flex justify-center py-8" data-testid="vector-stores-loading">
					<Loader2 className="text-muted-foreground size-5 animate-spin" />
				</div>
			)}

			{isError && (
				<Alert variant="destructive" data-testid="vector-stores-load-error">
					<AlertDescription>
						{t("vectorStores.loadError")} {getErrorMessage(error)}
					</AlertDescription>
				</Alert>
			)}

			{data && !data.editable && stores.length > 0 && (
				<Alert variant="info" data-testid="vector-stores-read-only-note">
					<Info />
					<AlertDescription>{t("vectorStores.readOnlyNote")}</AlertDescription>
				</Alert>
			)}

			{data && stores.length === 0 && <EmptyState />}

			<div className="space-y-4">
				{stores.map((store) => (
					<VectorStoreCard
						key={store.name}
						store={store}
						draft={drafts.get(store.name) ?? draftFromStore(store)}
						errors={fieldErrors.get(store.name) ?? { retention: null, maxSize: null }}
						canEdit={isEditable(store)}
						disabled={isSaving}
						onChange={handleChange}
					/>
				))}
			</div>

			{canEditAny && (
				<div className="bg-card sticky bottom-0 flex justify-end py-2">
					<Button onClick={handleSave} disabled={changes.length === 0 || hasErrors || isSaving} data-testid="vector-stores-save-btn">
						{isSaving ? t("saving") : t("saveChanges")}
					</Button>
				</div>
			)}
		</div>
	);
}

function EmptyState() {
	const { t } = useTranslation("config");
	return (
		<div className="flex flex-col items-center gap-2 rounded-sm border border-dashed p-8 text-center" data-testid="vector-stores-empty">
			<Database className="text-muted-foreground size-6" />
			<p className="text-sm font-medium">{t("vectorStores.emptyTitle")}</p>
			<p className="text-muted-foreground max-w-lg text-sm">
				<Trans
					t={t}
					i18nKey="vectorStores.emptyDescription"
					components={{ code0: <code className="text-xs" />, code1: <code className="text-xs" /> }}
				/>
			</p>
		</div>
	);
}

function StatusIndicator({ name, status }: { name: string; status: VectorStoreStatus }) {
	const { t } = useTranslation("config");
	const label =
		status === "connected"
			? t("vectorStores.statusConnected")
			: status === "failed"
				? t("vectorStores.statusFailed")
				: t("vectorStores.statusDisabled");
	return (
		<span
			className="text-muted-foreground flex shrink-0 items-center gap-1.5 text-xs font-medium"
			data-testid={`vector-store-status-${name}`}
		>
			<span className={cn("size-2 rounded-full", statusDotClass[status] ?? statusDotClass.disabled)} aria-hidden="true" />
			{label}
		</span>
	);
}

interface SettingAccordionRowProps {
	id: string;
	label: string;
	summary: string; // the value as it stands, shown while the row is closed
	help: string;
	error: string | null;
	open: boolean;
	onToggle: () => void;
	setting: "retention" | "max-size"; // with store, names the row's test IDs
	store: string;
	children: ReactNode;
}

/** One setting of a store: label and help on the left, the input on the right, as on the other settings pages. */
// One setting of a store, as the virtual key sheet shows a provider's budgets and limits: an
// indented row with the value as it stands, which opens to the field and what it means. A row
// whose value is invalid stays open, so the error that blocks the save is in view.
function SettingAccordionRow({ id, label, summary, help, error, open, onToggle, setting, store, children }: SettingAccordionRowProps) {
	const expanded = open || !!error;
	return (
		<div>
			<div
				role="button"
				tabIndex={0}
				aria-expanded={expanded}
				aria-controls={`${id}-panel`}
				onClick={onToggle}
				onKeyDown={(e) => {
					if (e.key === "Enter" || e.key === " ") {
						e.preventDefault();
						onToggle();
					}
				}}
				className={cn("hover:bg-accent/30 flex cursor-pointer items-center gap-2.5 py-2.5 pr-4 pl-10", expanded && "bg-accent/20")}
				data-testid={`vector-store-${setting}-toggle-${store}`}
			>
				{/* i18n-ignore: a tree glyph, not text */}
				<span className="text-muted-foreground/50 text-sm" aria-hidden="true">
					└
				</span>
				<span className="text-muted-foreground text-sm font-medium whitespace-nowrap">{label}</span>
				<span className="text-muted-foreground min-w-0 flex-1 truncate text-sm" data-testid={`vector-store-${setting}-summary-${store}`}>
					{summary}
				</span>
				<ChevronDown
					className={cn("text-muted-foreground/60 h-3.5 w-3.5 shrink-0 transition-transform duration-150", expanded && "rotate-180")}
					aria-hidden="true"
				/>
			</div>
			{expanded && (
				<div id={`${id}-panel`} className="bg-muted/30 space-y-2 border-t py-3 pr-4 pl-16">
					<Label htmlFor={id} className="sr-only">
						{label}
					</Label>
					<p id={`${id}-help`} className="text-muted-foreground text-sm">
						{help}
					</p>
					{children}
					{error && (
						<p className="text-destructive text-xs" role="alert" data-testid={`vector-store-${setting}-error-${store}`}>
							{error}
						</p>
					)}
				</div>
			)}
		</div>
	);
}

interface VectorStoreCardProps {
	store: VectorStoreInfo;
	draft: VectorStoreDraft;
	errors: { retention: string | null; maxSize: string | null };
	canEdit: boolean; // a config store is present, the user may update settings, and the store owns its connection
	disabled: boolean; // a save is running
	onChange: (name: string, draft: VectorStoreDraft) => void;
}

function VectorStoreCard({ store, draft, errors, canEdit, disabled, onChange }: VectorStoreCardProps) {
	const { t } = useTranslation("config");
	const fieldId = useId();
	const name = store.name;
	const sharesConnection = store.shared_with !== "";
	const retentionId = `${fieldId}-retention`;
	const maxSizeId = `${fieldId}-max-size`;
	const [retentionOpen, setRetentionOpen] = useState(false);
	const [maxSizeOpen, setMaxSizeOpen] = useState(false);

	return (
		<section className="rounded-sm border" data-testid={`vector-store-card-${name}`}>
			<div className="flex items-start justify-between gap-4 border-b px-4 py-3">
				<div className="min-w-0 space-y-1">
					<div className="flex flex-wrap items-center gap-2">
						<Database className="text-muted-foreground size-4 shrink-0" aria-hidden="true" />
						<h3 className="text-sm font-semibold" data-testid={`vector-store-name-${name}`}>
							{name}
						</h3>
						<Badge variant="outline" data-testid={`vector-store-type-${name}`}>
							{store.type}
						</Badge>
						{store.primary && (
							<Badge variant="default" data-testid={`vector-store-primary-badge-${name}`}>
								{t("vectorStores.primary")}
							</Badge>
						)}
						{sharesConnection && (
							<Badge variant="secondary" data-testid={`vector-store-shared-with-${name}`}>
								<Share2 />
								{t("vectorStores.sharesConnectionWith", { name: store.shared_with })}
							</Badge>
						)}
					</div>
					{store.endpoint && (
						<p
							className="text-muted-foreground truncate font-mono text-xs"
							title={store.endpoint}
							data-testid={`vector-store-endpoint-${name}`}
						>
							{store.endpoint}
						</p>
					)}
				</div>
				<StatusIndicator name={name} status={store.status} />
			</div>

			{store.status === "failed" && store.error && (
				<div
					className="bg-destructive/5 text-destructive flex items-start gap-2 border-b px-4 py-2.5 text-xs break-words"
					role="alert"
					data-testid={`vector-store-error-${name}`}
				>
					<CircleAlert className="mt-px size-3.5 shrink-0" aria-hidden="true" />
					<span>{store.error}</span>
				</div>
			)}

			{sharesConnection && (
				<div
					className="bg-muted/40 text-muted-foreground flex items-center gap-2 border-b px-4 py-2.5 text-sm"
					data-testid={`vector-store-shared-settings-note-${name}`}
				>
					<Info className="size-3.5 shrink-0" aria-hidden="true" />
					{t("vectorStores.usesOwnerSettings", { owner: store.shared_with })}
				</div>
			)}

			<div className="divide-y">
				<SettingAccordionRow
					id={retentionId}
					label={t("vectorStores.retentionDays")}
					summary={draft.retentionDays.trim() || t("vectorStores.retentionPlaceholder")}
					help={t("vectorStores.retentionDaysHelp")}
					error={canEdit ? errors.retention : null}
					open={retentionOpen}
					onToggle={() => setRetentionOpen((open) => !open)}
					setting="retention"
					store={name}
				>
					<Input
						id={retentionId}
						data-testid={`vector-store-retention-input-${name}`}
						type="number"
						inputMode="numeric"
						min={0}
						step={1}
						placeholder={t("vectorStores.retentionPlaceholder")}
						aria-invalid={canEdit && errors.retention ? true : undefined}
						aria-describedby={`${retentionId}-help`}
						value={draft.retentionDays}
						disabled={!canEdit || disabled}
						onChange={(e) => onChange(name, { ...draft, retentionDays: e.target.value })}
						className={cn("w-36", canEdit && errors.retention && "border-destructive")}
					/>
				</SettingAccordionRow>
				<SettingAccordionRow
					id={maxSizeId}
					label={t("vectorStores.maxSizeMB")}
					summary={draft.maxSizeMB.trim() || t("vectorStores.maxSizePlaceholder")}
					help={t("vectorStores.maxSizeMBHelp")}
					error={canEdit ? errors.maxSize : null}
					open={maxSizeOpen}
					onToggle={() => setMaxSizeOpen((open) => !open)}
					setting="max-size"
					store={name}
				>
					<Input
						id={maxSizeId}
						data-testid={`vector-store-max-size-input-${name}`}
						type="number"
						inputMode="numeric"
						min={0}
						step={1}
						placeholder={t("vectorStores.maxSizePlaceholder")}
						aria-invalid={canEdit && errors.maxSize ? true : undefined}
						aria-describedby={`${maxSizeId}-help`}
						value={draft.maxSizeMB}
						disabled={!canEdit || disabled}
						onChange={(e) => onChange(name, { ...draft, maxSizeMB: e.target.value })}
						className={cn("w-36", canEdit && errors.maxSize && "border-destructive")}
					/>
				</SettingAccordionRow>
			</div>
		</section>
	);
}