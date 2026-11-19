import { CustomerSelector } from "@/components/entitySelectors/customerSelector";
import { TeamSelector } from "@/components/entitySelectors/teamSelector";
import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
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
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelSelector } from "@/components/ui/modelSelector";
import { ProviderSelector, type ProviderSelectorOption } from "@/components/ui/providerSelector";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { getUserPicker } from "@/lib/registries/userPicker";
import { ROUTING_RULE_SCOPES, RoutingFallbackFormData, RoutingTargetFormData } from "@/lib/types/routingRules";
import { isValidRuleGroupType } from "@/lib/utils/routingRuleGroupQuery";
import type { TFunction } from "i18next";
import { Edit, MoreHorizontal, Trash2, X } from "lucide-react";
import { lazy, Suspense, useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { RuleGroupType } from "react-querybuilder";
// Side-effect import: registers the enterprise user picker (no-op in OSS builds).
import "@enterprise/lib/registrations/userPicker";

// Form pieces the model router views share: the routing condition and scope
// fields, a row's actions and delete confirmation, and the target and fallback
// rows. They follow the routing rule screens, which this section copies from
// rather than changes.

export type ConditionMode = "builder" | "cel";

/**
 * Decides which condition editor a router's routing opens in. A condition authored
 * outside the visual builder (e.g. via the API or config.json) has a CEL expression but
 * no usable `query`; it opens in CEL mode so the expression stays visible and editable
 * instead of being silently cleared.
 */
export function initialConditionMode(routing?: { query?: RuleGroupType; cel_expression?: string } | null): ConditionMode {
	if (!routing) {
		return "builder";
	}
	const hasQuery = isValidRuleGroupType(routing.query) && (routing.query.rules?.length ?? 0) > 0;
	if (hasQuery) {
		return "builder";
	}
	return routing.cel_expression?.trim() ? "cel" : "builder";
}

// Lazy-load the condition builder (heavy dependency tree).
const ConditionBuilderLazy = lazy(() =>
	import("./routerConditionBuilder").then((mod) => ({
		default: mod.RouterConditionBuilder,
	})),
);
export const ConditionBuilder = (props: React.ComponentProps<typeof ConditionBuilderLazy>) => {
	const { t } = useTranslation("models");
	return (
		<Suspense fallback={<div className="text-sm text-gray-500">{t("routing.loadingCelBuilder")}</div>}>
			<ConditionBuilderLazy {...props} />
		</Suspense>
	);
};

/** The name of the entity a scope points at, for its field label and error. */
export function scopeNoun(scope: string, t: TFunction<"models">) {
	if (scope === "team") return t("routing.team");
	if (scope === "customer") return t("routing.customer");
	if (scope === "user") return t("routing.user");
	return t("routing.scopeVirtualKey");
}

interface ScopeSelectProps {
	id: string;
	value: string;
	onChange: (scope: string) => void;
	testId?: string;
}

/** The scope a router takes requests at. User is offered only when a user picker is registered, or the router already uses it. */
export function ScopeSelect({ id, value, onChange, testId }: ScopeSelectProps) {
	const { t } = useTranslation("models");
	const UserPicker = getUserPicker();
	return (
		<div className="space-y-3">
			<Label htmlFor={id}>{t("routing.scope")}</Label>
			<Select value={value} onValueChange={onChange}>
				<SelectTrigger id={id} className="w-full" data-testid={testId}>
					<SelectValue placeholder={t("routing.selectScope")} />
				</SelectTrigger>
				<SelectContent>
					{ROUTING_RULE_SCOPES.map((scopeOption) => (
						<SelectItem key={scopeOption.value} value={scopeOption.value}>
							{scopeOption.label}
						</SelectItem>
					))}
					{(UserPicker || value === "user") && <SelectItem value="user">{t("routing.user")}</SelectItem>}
				</SelectContent>
			</Select>
		</div>
	);
}

interface ScopeEntityFieldProps {
	scope: string;
	value: string;
	onChange: (scopeId: string) => void;
	error?: string;
	userInputTestId: string;
}

/** The entity a non-global scope points at, picked with the shared entity selectors. */
export function ScopeEntityField({ scope, value, onChange, error, userInputTestId }: ScopeEntityFieldProps) {
	const { t } = useTranslation("models");
	const UserPicker = getUserPicker();
	return (
		<div className="space-y-2">
			<Label htmlFor="scope_id">
				{scopeNoun(scope, t)} <span className="text-red-500">*</span>
			</Label>
			{/* Routing stores only its scope_id, so there is no name to seed
			    these with — each selector resolves its own selection. */}
			{scope === "team" && <TeamSelector value={value} onChange={onChange} />}
			{scope === "customer" && <CustomerSelector value={value} onChange={onChange} />}
			{scope === "virtual_key" && <VirtualKeySelector value={value} onChange={onChange} />}
			{scope === "user" &&
				(UserPicker ? (
					<UserPicker value={value} onChange={onChange} />
				) : (
					// No user directory in this build: keep a plain input so
					// existing user-scoped routing remains editable.
					<Input
						id="scope_id"
						data-testid={userInputTestId}
						placeholder={t("routing.governanceUserId")}
						value={value}
						onChange={(e) => onChange(e.target.value)}
					/>
				))}
			{/* Teams, customers and virtual keys are all searched lazily inside their
			    selectors, each of which surfaces its own empty state. */}
			{error && <p className="text-destructive text-sm">{error}</p>}
		</div>
	);
}

interface RowActionsMenuProps {
	id: string;
	// Prefix of the menu's test ids, e.g. "model-router".
	testIdPrefix: string;
	canUpdate: boolean;
	canDelete: boolean;
	onEdit: () => void;
	onDelete: () => void;
	// The trigger's accessible name.
	ariaLabel: string;
}

/** The edit and delete menu on a table row, as routing rules have. */
export function RowActionsMenu({ id, testIdPrefix, canUpdate, canDelete, onEdit, onDelete, ariaLabel }: RowActionsMenuProps) {
	const { t: tc } = useTranslation("common");
	const [isOpen, setIsOpen] = useState(false);

	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()}>
				<Button variant="ghost" size="icon" className="h-8 w-8" aria-label={ariaLabel} data-testid={`${testIdPrefix}-actions-${id}-btn`}>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem
					className="cursor-pointer"
					disabled={!canUpdate}
					data-testid={`${testIdPrefix}-edit-${id}-btn`}
					onSelect={(e) => {
						e.preventDefault();
						onEdit();
						setIsOpen(false);
					}}
				>
					<Edit className="h-4 w-4" />
					{tc("edit")}
				</DropdownMenuItem>
				<DropdownMenuItem
					variant="destructive"
					className="cursor-pointer"
					disabled={!canDelete}
					data-testid={`${testIdPrefix}-delete-${id}-btn`}
					onSelect={(e) => {
						e.preventDefault();
						onDelete();
						setIsOpen(false);
					}}
				>
					<Trash2 className="h-4 w-4" />
					{tc("delete")}
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

interface DeleteConfirmDialogProps {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	title: string;
	description: string;
	isDeleting: boolean;
	onConfirm: () => void;
}

/** Confirms a delete. */
export function DeleteConfirmDialog({ open, onOpenChange, title, description, isDeleting, onConfirm }: DeleteConfirmDialogProps) {
	const { t: tc } = useTranslation("common");
	const { t } = useTranslation("models");
	return (
		<AlertDialog open={open} onOpenChange={onOpenChange}>
			<AlertDialogContent>
				<AlertDialogHeader>
					<AlertDialogTitle>{title}</AlertDialogTitle>
					<AlertDialogDescription>{description}</AlertDialogDescription>
				</AlertDialogHeader>
				<AlertDialogFooter>
					<AlertDialogCancel disabled={isDeleting}>{tc("cancel")}</AlertDialogCancel>
					<AlertDialogAction onClick={onConfirm} disabled={isDeleting} className="bg-destructive hover:bg-destructive/90">
						{isDeleting ? t("routing.deleting") : tc("delete")}
					</AlertDialogAction>
				</AlertDialogFooter>
			</AlertDialogContent>
		</AlertDialog>
	);
}

interface ProviderKeySelectProps {
	idPrefix: string;
	clearLabel: string;
	provider?: string;
	keyId?: string;
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onChange: (keyId: string) => void;
}

/** Renders nothing until a provider is chosen, since keys are scoped to one. */
function ProviderKeySelect({ idPrefix, clearLabel, provider, keyId, allKeys, onChange }: ProviderKeySelectProps) {
	const { t } = useTranslation("models");
	const availableKeys = provider ? allKeys.filter((k) => k.provider === provider).map((k) => ({ id: k.key_id, name: k.name })) : [];
	if (!provider || (availableKeys.length === 0 && !keyId)) {
		return null;
	}

	return (
		<div className="space-y-1.5">
			<Label id={`${idPrefix}-apikey-label`} className="text-xs">
				<Trans i18nKey="routing.apiKeyOptional" ns="models" components={{ muted: <span className="text-muted-foreground" /> }} />
			</Label>
			<div className="flex gap-1.5">
				<Select value={keyId || ""} onValueChange={onChange}>
					<SelectTrigger
						id={`${idPrefix}-apikey-select`}
						aria-labelledby={`${idPrefix}-apikey-label`}
						className="h-9 flex-1 text-sm"
						data-testid={`${idPrefix}-apikey-select`}
					>
						<SelectValue placeholder={t("routing.selectKeyOptional")} />
					</SelectTrigger>
					<SelectContent>
						{availableKeys.map((key) => (
							<SelectItem key={key.id} value={key.id}>
								{key.name}
							</SelectItem>
						))}
						{keyId && !availableKeys.some((k) => k.id === keyId) && (
							<SelectItem key={`pinned-${keyId}`} value={keyId}>
								{t("routing.pinnedKey", { id: keyId })}
							</SelectItem>
						)}
					</SelectContent>
				</Select>
				{keyId && (
					<Button
						type="button"
						variant="outline"
						size="sm"
						onClick={() => onChange("")}
						className="h-9 w-9 p-0"
						aria-label={clearLabel}
						data-testid={`${idPrefix}-apikey-clear`}
					>
						<X className="h-3.5 w-3.5" />
					</Button>
				)}
			</div>
		</div>
	);
}

interface FallbackRowProps {
	fallback: RoutingFallbackFormData;
	index: number;
	/** Prefix for element ids and test ids. */
	idPrefix: string;
	referencedProviderOptions: ProviderSelectorOption[];
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	onUpdate: (index: number, changes: Partial<RoutingFallbackFormData>) => void;
	onRemove: (index: number) => void;
}

export function FallbackRow({ fallback, index, idPrefix, referencedProviderOptions, allKeys, onUpdate, onRemove }: FallbackRowProps) {
	const { t } = useTranslation("models");
	const provider = fallback.provider || "";

	return (
		<div className="space-y-2 rounded-lg border p-3" data-testid={idPrefix}>
			<div className="flex items-center gap-2">
				<div className="flex-1">
					<ProviderSelector
						extraOptions={referencedProviderOptions}
						value={provider}
						// A key belongs to one provider, so switching providers invalidates the pin.
						onChange={(value: string) => onUpdate(index, { provider: value, model: "", key_id: "" })}
						placeholder={t("routing.selectProvider")}
						className="!h-9 !min-h-9"
						data-testid={`${idPrefix}-provider-select`}
						noPortal
					/>
				</div>
				<div className="flex-1" data-testid={`${idPrefix}-model-select`}>
					<ModelSelector
						provider={provider || undefined}
						value={fallback.model || ""}
						onChange={(value) => onUpdate(index, { model: value })}
						placeholder={t("routing.incomingOptional")}
						allowCustomModel
						unfiltered={true}
						disabled={!provider}
						className="!h-9 !min-h-9 w-full"
					/>
				</div>
				<Button
					type="button"
					variant="ghost"
					size="sm"
					onClick={() => onRemove(index)}
					className="h-9 px-2"
					aria-label={t("routing.removeFallback", { n: index + 1 })}
					data-testid={`${idPrefix}-remove-button`}
				>
					<Trash2 className="h-4 w-4" />
				</Button>
			</div>

			<ProviderKeySelect
				idPrefix={idPrefix}
				clearLabel={t("routing.clearFallbackApiKey", { n: index + 1 })}
				provider={provider}
				keyId={fallback.key_id}
				allKeys={allKeys}
				onChange={(value) => onUpdate(index, { key_id: value })}
			/>
		</div>
	);
}

interface TargetRowProps {
	target: RoutingTargetFormData;
	index: number;
	/** Prefix for element ids and test ids. */
	idPrefix: string;
	referencedProviderOptions: ProviderSelectorOption[];
	allKeys: Array<{ key_id: string; name: string; provider: string }>;
	showRemove: boolean;
	onUpdate: (index: number, field: keyof RoutingTargetFormData, value: string | number) => void;
	onRemove: (index: number) => void;
}

export function TargetRow({ target, index, idPrefix, referencedProviderOptions, allKeys, showRemove, onUpdate, onRemove }: TargetRowProps) {
	const { t } = useTranslation("models");
	return (
		<div className="space-y-3 rounded-lg border p-3" data-testid={idPrefix}>
			<div className="flex items-center justify-between">
				<span className="text-muted-foreground text-sm font-medium">{t("routing.targetN", { n: index + 1 })}</span>
				<div className="flex items-center gap-2">
					<div className="flex items-center gap-1.5">
						<Label htmlFor={`${idPrefix}-weight-input`} className="text-muted-foreground shrink-0 text-xs">
							{t("providers.weight")}
						</Label>
						<Input
							id={`${idPrefix}-weight-input`}
							type="number"
							min={0.001}
							max={1}
							step={0.001}
							value={target.weight}
							onChange={(e) => onUpdate(index, "weight", parseFloat(e.target.value) || 0)}
							className="h-8 w-24 text-sm"
							data-testid={`${idPrefix}-weight-input`}
						/>
					</div>
					{showRemove && (
						<Button
							type="button"
							variant="ghost"
							size="sm"
							onClick={() => onRemove(index)}
							className="h-8 w-8 p-0"
							aria-label={t("routing.removeTarget", { n: index + 1 })}
							data-testid={`${idPrefix}-remove-button`}
						>
							<Trash2 className="h-3.5 w-3.5" />
						</Button>
					)}
				</div>
			</div>

			<div className="grid grid-cols-1 gap-3 md:grid-cols-2">
				<div className="space-y-1.5">
					<Label id={`${idPrefix}-provider-label`} className="text-xs">
						{t("modelCatalog.provider")}
					</Label>
					<div className="flex gap-1.5">
						<ProviderSelector
							extraOptions={referencedProviderOptions}
							value={target.provider || ""}
							onChange={(value: string) => {
								onUpdate(index, "provider", value);
								onUpdate(index, "model", "");
								onUpdate(index, "key_id", "");
							}}
							placeholder={t("routing.incomingOptional")}
							className="!h-9 !min-h-9 flex-1 text-sm"
							data-testid={`${idPrefix}-provider-select`}
							noPortal
						/>
						{target.provider && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => {
									onUpdate(index, "provider", "");
									onUpdate(index, "model", "");
									onUpdate(index, "key_id", "");
								}}
								className="h-9 w-9 p-0"
								aria-label={t("routing.clearProvider", { n: index + 1 })}
								data-testid={`${idPrefix}-provider-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>

				<div className="space-y-1.5">
					<Label id={`${idPrefix}-model-label`} className="text-xs">
						{t("modelCatalog.model")}
					</Label>
					<div className="flex gap-1.5">
						<div className="flex-1" data-testid={`${idPrefix}-model-select`}>
							<ModelSelector
								provider={target.provider || undefined}
								value={target.model}
								onChange={(value) => onUpdate(index, "model", value)}
								placeholder={t("routing.incomingOptional")}
								allowCustomModel
								className="!h-9 !min-h-9"
								inputId={`${idPrefix}-model-input`}
								ariaLabelledBy={`${idPrefix}-model-label`}
							/>
						</div>
						{target.model && (
							<Button
								type="button"
								variant="outline"
								size="sm"
								onClick={() => onUpdate(index, "model", "")}
								className="h-9 w-9 p-0"
								aria-label={t("routing.clearModel", { n: index + 1 })}
								data-testid={`${idPrefix}-model-clear`}
							>
								<X className="h-3.5 w-3.5" />
							</Button>
						)}
					</div>
				</div>
			</div>

			<ProviderKeySelect
				idPrefix={idPrefix}
				clearLabel={t("routing.clearApiKey", { n: index + 1 })}
				provider={target.provider}
				keyId={target.key_id}
				allKeys={allKeys}
				onChange={(value) => onUpdate(index, "key_id", value)}
			/>
		</div>
	);
}