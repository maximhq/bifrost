import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName, RequestTypeLabels } from "@/lib/constants/logs";
import {
	getErrorMessage,
	ModelDetails,
	ModelPricingOverrideSummary,
	useGetCoreConfigQuery,
	useUpsertModelCatalogEntriesMutation,
} from "@/lib/store";
import { useT } from "@/lib/i18n";
import { KnownProvider } from "@/lib/types/config";
import { PricingOverrideScopeKind } from "@/lib/types/governance";
import { formatCharacterPriceFull, formatTokenPriceFull } from "@/lib/utils/numbers";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ExternalLink, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { fieldLabelByKey, PricingFieldKey, pricingFieldUnit } from "../../custom-pricing/overrides/pricingFields";
import OverriddenPrice from "./overriddenPrice";

const DEFAULT_PRICING_SOURCE_URL = "https://getbifrost.ai/datasheet";

// Scopes whose overrides can't be resolved from the model catalog alone — they
// only apply to requests carrying the matching virtual key, user, or provider
// key, so they are listed here but never change the displayed price.

interface AttributeSheetProps {
	model: ModelDetails;
	/** Overrides referenced by `model.pricing_override_ids`, keyed by ID. */
	overrides?: Record<string, ModelPricingOverrideSummary>;
	onClose: () => void;
}

// Local row type for the extra-attributes editor. We keep these outside any
// schema because empty rows are valid during editing — we filter them at
// submit time. The id is a render-stable identifier (not persisted) so React
// keeps DOM nodes pinned to the right row across add/remove.
interface AttributeRow {
	id: string;
	key: string;
	value: string;
}

let rowIdCounter = 0;
function newRowId(): string {
	if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
		return crypto.randomUUID();
	}
	rowIdCounter += 1;
	return `row-${rowIdCounter}`;
}

function rowsFromAttributes(attrs?: Record<string, string>): AttributeRow[] {
	if (!attrs) return [];
	return Object.entries(attrs)
		.filter(([k]) => k !== "description")
		.map(([key, value]) => ({ id: newRowId(), key, value }));
}

function isLinkableSource(url: string) {
	return url.startsWith("http://") || url.startsWith("https://");
}

function getPricingSourceUrl(configuredUrl: string | undefined, modelName: string) {
	if (configuredUrl) return configuredUrl;
	const url = new URL(DEFAULT_PRICING_SOURCE_URL);
	url.searchParams.set("model", modelName);
	return url.toString();
}

// formatPatchValue renders a patch value in its field's own unit: the way the
// pricing table does for token-priced fields, as a bare multiplier for the geo
// multiplier, and as a plain dollar amount for everything else (per-image,
// per-second, per-page, …).
function formatPatchValue(key: string, value: number): string {
	switch (pricingFieldUnit(key)) {
		case "token":
			return formatTokenPriceFull(value);
		case "character":
			return formatCharacterPriceFull(value);
		case "multiplier":
			return `${value}×`;
		default:
			return `$${value}`;
	}
}

export default function AttributeSheet({ model, overrides, onClose }: AttributeSheetProps) {
	const t = useT();
	const SCOPE_CAVEATS: Partial<Record<PricingOverrideScopeKind, string>> = {
		provider_key: t("routing.modelCatalog.scopeProviderKey"),
		virtual_key: t("routing.modelCatalog.scopeVirtualKey"),
		virtual_key_provider: t("routing.modelCatalog.scopeVirtualKeyProvider"),
		virtual_key_provider_key: t("routing.modelCatalog.scopeVirtualKeyProviderKey"),
		user: t("routing.modelCatalog.scopeUser"),
		user_provider: t("routing.modelCatalog.scopeUserProvider"),
		user_provider_key: t("routing.modelCatalog.scopeUserProviderKey"),
	};
	const [isOpen, setIsOpen] = useState(true);
	const hasUpdateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });

	const [upsertEntries, { isLoading }] = useUpsertModelCatalogEntriesMutation();

	const initialDescription = model.additional_attributes?.description ?? "";
	const [description, setDescription] = useState(initialDescription);

	// Overrides arrive as IDs into a response-level index; skip any that went
	// missing (e.g. deleted in another tab before this list refreshed).
	const matchingOverrides = useMemo(
		() => (model.pricing_override_ids ?? []).map((id) => overrides?.[id]).filter((o): o is ModelPricingOverrideSummary => !!o),
		[model.pricing_override_ids, overrides],
	);
	const appliedOverrideName = model.applied_override_id ? overrides?.[model.applied_override_id]?.name : undefined;

	const initialRows = useMemo(() => rowsFromAttributes(model.additional_attributes), [model.additional_attributes]);
	const stripIds = (rows: AttributeRow[]) => rows.map(({ key, value }) => ({ key, value }));
	const [initialRowsKey] = useState(() => JSON.stringify(stripIds(initialRows)));
	const [extraRows, setExtraRows] = useState<AttributeRow[]>(initialRows);

	const rowsDirty = JSON.stringify(stripIds(extraRows)) !== initialRowsKey;
	const isDirty = description !== initialDescription || rowsDirty;
	const pricingSourceUrl = getPricingSourceUrl(bifrostConfig?.framework_config?.pricing_url, model.name);
	const canOpenPricingSource = isLinkableSource(pricingSourceUrl);

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => onClose(), 150);
	};

	const handleAddRow = () => setExtraRows((prev) => [...prev, { id: newRowId(), key: "", value: "" }]);
	const handleRowChange = (id: string, field: "key" | "value", val: string) =>
		setExtraRows((prev) => prev.map((row) => (row.id === id ? { ...row, [field]: val } : row)));
	const handleRemoveRow = (id: string) => setExtraRows((prev) => prev.filter((row) => row.id !== id));

	const handleSubmit = async () => {
		if (!hasUpdateAccess) {
			toast.error(t("routing.modelCatalog.noPermissionAction"));
			return;
		}

		// Validate that extra rows have non-empty keys when they have any value.
		// Empty rows are fine — we drop them.
		const cleaned = extraRows.map((r) => ({ key: r.key.trim(), value: r.value })).filter((r) => r.key !== "" || r.value !== "");
		const missingKey = cleaned.find((r) => r.key === "");
		if (missingKey) {
			toast.error(t("routing.modelCatalog.rowsNeedKey"));
			return;
		}
		const dupKey = cleaned.find((r, i) => cleaned.findIndex((other) => other.key === r.key) !== i);
		if (dupKey) {
			toast.error(t("routing.modelCatalog.duplicateKey", { key: dupKey.key }));
			return;
		}
		// "description" is the special-cased field above — disallow it as an extra row.
		const reservedClash = cleaned.find((r) => r.key === "description");
		if (reservedClash) {
			toast.error(t("routing.modelCatalog.useDescriptionField"));
			return;
		}

		const attributes: Record<string, string> = {};
		const desc = description.trim();
		if (desc !== "") attributes.description = desc;
		for (const r of cleaned) attributes[r.key] = r.value;

		try {
			await upsertEntries([
				{
					model: model.name,
					provider: model.provider,
					additional_attributes: Object.keys(attributes).length > 0 ? attributes : undefined,
				},
			]).unwrap();
			toast.success(t("routing.modelCatalog.attributesSaved"));
			handleClose();
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	return (
		<Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
			<SheetContent
				className="flex w-full flex-col overflow-x-hidden pt-4"
				onInteractOutside={(e) => {
					if (isDirty) e.preventDefault();
				}}
				onEscapeKeyDown={(e) => {
					if (isDirty) e.preventDefault();
				}}
				data-testid="model-catalog-attribute-sheet"
			>
				<SheetHeader className="flex flex-col items-start p-0 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10 px-4 md:px-8">
					<SheetTitle>{t("routing.modelCatalog.editAttributes")}</SheetTitle>
					<SheetDescription>{t("routing.modelCatalog.editDesc")}</SheetDescription>
				</SheetHeader>

				<div className="flex h-full flex-col gap-6">
					<div className="grow space-y-4 px-4 md:px-8">
						{/* Read-only provider / model header */}
						<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
							<div>
								<Label className="text-sm font-medium">{t("routing.modelCatalog.provider")}</Label>
								<div className="bg-muted/30 mt-2 flex items-center gap-2 rounded-sm border px-3 py-2 text-sm">
									<RenderProviderIcon provider={model.provider as KnownProvider} size="sm" className="h-4 w-4" />
									<span>{ProviderLabels[model.provider as ProviderName] || model.provider}</span>
								</div>
							</div>
							<div>
								<Label className="text-sm font-medium">{t("routing.modelCatalog.model")}</Label>
								<div className="bg-muted/30 mt-2 rounded-sm border px-3 py-2 font-mono text-sm">{model.name}</div>
							</div>
						</div>

						<DottedSeparator />

						{/* Pricing */}
						<div className="space-y-3">
							<div className="flex items-center justify-between gap-3">
								<Label className="text-sm font-medium">{t("routing.modelCatalog.pricing")}</Label>
								{canOpenPricingSource ? (
									<a
										href={pricingSourceUrl}
										target="_blank"
										rel="noreferrer"
										className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
										data-testid="model-catalog-pricing-source-link"
									>
										{t("routing.modelCatalog.source")}
										<ExternalLink className="h-3 w-3" />
									</a>
								) : (
									<span className="text-muted-foreground max-w-[260px] truncate text-right font-mono text-xs" title={pricingSourceUrl}>
										{pricingSourceUrl}
									</span>
								)}
							</div>
							<div className="grid grid-cols-1 gap-4 md:grid-cols-2">
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">{t("routing.modelCatalog.input")}</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-input-cost">
										<OverriddenPrice
											variant="full"
											base={model.input_cost_per_token}
											override={model.overridden_pricing?.input_cost_per_token}
											overrideName={appliedOverrideName}
										/>
									</p>
								</div>
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">{t("routing.modelCatalog.output")}</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-output-cost">
										<OverriddenPrice
											variant="full"
											base={model.output_cost_per_token}
											override={model.overridden_pricing?.output_cost_per_token}
											overrideName={appliedOverrideName}
										/>
									</p>
								</div>
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">{t("routing.modelCatalog.cacheWrite")}</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-cache-write-cost">
										<OverriddenPrice
											variant="full"
											base={model.cache_creation_input_token_cost}
											override={model.overridden_pricing?.cache_creation_input_token_cost}
											overrideName={appliedOverrideName}
										/>
									</p>
								</div>
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">{t("routing.modelCatalog.cacheRead")}</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-cache-read-cost">
										<OverriddenPrice
											variant="full"
											base={model.cache_read_input_token_cost}
											override={model.overridden_pricing?.cache_read_input_token_cost}
											overrideName={appliedOverrideName}
										/>
									</p>
								</div>
							</div>
						</div>

						{matchingOverrides.length > 0 && (
							<>
								<DottedSeparator />

								{/* Pricing overrides */}
								<div className="space-y-3" data-testid="model-catalog-pricing-overrides">
									<div className="flex items-center justify-between gap-3">
										<Label className="text-sm font-medium">{t("routing.modelCatalog.pricingOverrides")}</Label>
										<Link
											to="/workspace/custom-pricing/overrides"
											className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
										>
											{t("routing.modelCatalog.manage")}
											<ExternalLink className="h-3 w-3" />
										</Link>
									</div>
									{matchingOverrides.map((override) => {
										const caveat = SCOPE_CAVEATS[override.scope_kind];
										const patchEntries = Object.entries(override.patch).filter(([, value]) => value !== undefined && value !== null);
										return (
											<div
												key={override.id}
												className="bg-muted/30 space-y-2 rounded-sm border px-3 py-2"
												data-testid={`model-catalog-pricing-override-${override.id}`}
											>
												<div className="flex flex-wrap items-center gap-2">
													<span className="text-sm font-medium">{override.name || override.id}</span>
													<Badge variant="secondary">{override.scope_kind}</Badge>
													{override.id === model.applied_override_id && (
														<Badge variant="outline">{t("routing.modelCatalog.applied")}</Badge>
													)}
												</div>
												<p className="text-muted-foreground font-mono text-xs">
													{override.match_type === "wildcard"
														? t("routing.modelCatalog.matches", { pattern: override.pattern })
														: t("routing.modelCatalog.exact", { pattern: override.pattern })}
												</p>
												{caveat && <p className="text-muted-foreground text-xs">{caveat}</p>}
												{override.request_types && override.request_types.length > 0 && (
													<div className="flex flex-wrap gap-1">
														{override.request_types.map((rt) => (
															<Badge key={rt} variant="outline" className="text-[10px]">
																{RequestTypeLabels[rt as keyof typeof RequestTypeLabels] ?? rt}
															</Badge>
														))}
													</div>
												)}
												{patchEntries.length > 0 && (
													<div className="space-y-1">
														{patchEntries.map(([key, value]) => (
															<div key={key} className="flex items-baseline justify-between gap-3 text-xs">
																<span className="text-muted-foreground">{fieldLabelByKey[key as PricingFieldKey] || key}</span>
																<span className="font-mono">{formatPatchValue(key, value as number)}</span>
															</div>
														))}
													</div>
												)}
											</div>
										);
									})}
								</div>
							</>
						)}

						<DottedSeparator />

						{/* Description */}
						<div>
							<Label className="text-sm font-medium">{t("common.description")}</Label>
							<Textarea
								className="mt-2"
								value={description}
								onChange={(e) => setDescription(e.target.value)}
								rows={4}
								placeholder={t("routing.modelCatalog.descriptionPlaceholder")}
								data-testid="model-catalog-description-textarea"
							/>
						</div>

						<DottedSeparator />

						{/* Other attributes */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<Label className="text-sm font-medium">{t("routing.modelCatalog.otherAttributes")}</Label>
								<Button type="button" variant="outline" size="sm" onClick={handleAddRow} data-testid="model-catalog-add-attribute-row">
									<Plus className="mr-1 h-3 w-3" />
									{t("common.add")}
								</Button>
							</div>
							{extraRows.length === 0 ? (
								<p className="text-muted-foreground text-xs">{t("routing.modelCatalog.noExtraAttributes")}</p>
							) : (
								<div className="space-y-2">
									{extraRows.map((row, i) => (
										<div key={row.id} className="flex items-start gap-2">
											<Input
												value={row.key}
												onChange={(e) => handleRowChange(row.id, "key", e.target.value)}
												placeholder={t("routing.modelCatalog.keyPlaceholder")}
												className="flex-1"
												data-testid={`model-catalog-attribute-key-${i}`}
											/>
											<Input
												value={row.value}
												onChange={(e) => handleRowChange(row.id, "value", e.target.value)}
												placeholder={t("routing.modelCatalog.valuePlaceholder")}
												className="flex-1"
												data-testid={`model-catalog-attribute-value-${i}`}
											/>
											<Button
												type="button"
												variant="ghost"
												size="icon"
												onClick={() => handleRemoveRow(row.id)}
												data-testid={`model-catalog-attribute-remove-${i}`}
											>
												<Trash2 className="h-4 w-4" />
											</Button>
										</div>
									))}
								</div>
							)}
						</div>
					</div>

					<div className="bg-card sticky bottom-0 shrink-0 border-t px-4 py-4 md:px-8">
						<div className="flex items-center justify-end gap-3">
							{!hasUpdateAccess && <p className="text-destructive text-sm">{t("routing.modelCatalog.noPermissionAction")}</p>}
							<Button type="button" variant="outline" onClick={handleClose} data-testid="model-catalog-attribute-cancel">
								{t("common.cancel")}
							</Button>
							<Button
								type="button"
								onClick={handleSubmit}
								disabled={isLoading || !isDirty || !hasUpdateAccess}
								data-testid="model-catalog-attribute-submit"
							>
								{isLoading ? t("common.saving") : t("common.saveChanges")}
							</Button>
						</div>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}