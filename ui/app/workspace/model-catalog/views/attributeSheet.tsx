import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import { getErrorMessage, ModelDetails, useGetCoreConfigQuery, useUpsertModelCatalogEntriesMutation } from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { formatTokenPriceFull } from "@/lib/utils/numbers";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { ExternalLink, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";

const DEFAULT_PRICING_SOURCE_URL = "https://getbifrost.ai/datasheet";

interface AttributeSheetProps {
	model: ModelDetails;
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

export default function AttributeSheet({ model, onClose }: AttributeSheetProps) {
	const [isOpen, setIsOpen] = useState(true);
	const hasUpdateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });

	const [upsertEntries, { isLoading }] = useUpsertModelCatalogEntriesMutation();

	const initialDescription = model.additional_attributes?.description ?? "";
	const [description, setDescription] = useState(initialDescription);

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
			toast.error("You don't have permission to perform this action");
			return;
		}

		// Validate that extra rows have non-empty keys when they have any value.
		// Empty rows are fine — we drop them.
		const cleaned = extraRows.map((r) => ({ key: r.key.trim(), value: r.value })).filter((r) => r.key !== "" || r.value !== "");
		const missingKey = cleaned.find((r) => r.key === "");
		if (missingKey) {
			toast.error("Attribute rows must have a key");
			return;
		}
		const dupKey = cleaned.find((r, i) => cleaned.findIndex((other) => other.key === r.key) !== i);
		if (dupKey) {
			toast.error(`Duplicate attribute key: ${dupKey.key}`);
			return;
		}
		// "description" is the special-cased field above — disallow it as an extra row.
		const reservedClash = cleaned.find((r) => r.key === "description");
		if (reservedClash) {
			toast.error("Use the Description field instead of a 'description' attribute row");
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
			toast.success("Attributes saved");
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
				<SheetHeader className="flex flex-col items-start p-0 px-8 py-4" headerClassName="mb-0 sticky -top-4 bg-card z-10">
					<SheetTitle>Edit Model Attributes</SheetTitle>
					<SheetDescription>
						Update the description and other attributes for this model. These attributes are stored on the pricing row and preserved across
						the pricing sync.
					</SheetDescription>
				</SheetHeader>

				<div className="flex h-full flex-col gap-6">
					<div className="grow space-y-4 px-8">
						{/* Read-only provider / model header */}
						<div className="grid grid-cols-2 gap-4">
							<div>
								<Label className="text-sm font-medium">Provider</Label>
								<div className="bg-muted/30 mt-2 flex items-center gap-2 rounded-sm border px-3 py-2 text-sm">
									<RenderProviderIcon provider={model.provider as KnownProvider} size="sm" className="h-4 w-4" />
									<span>{ProviderLabels[model.provider as ProviderName] || model.provider}</span>
								</div>
							</div>
							<div>
								<Label className="text-sm font-medium">Model</Label>
								<div className="bg-muted/30 mt-2 rounded-sm border px-3 py-2 font-mono text-sm">{model.name}</div>
							</div>
						</div>

						<DottedSeparator />

						{/* Pricing */}
						<div className="space-y-3">
							<div className="flex items-center justify-between gap-3">
								<Label className="text-sm font-medium">Pricing</Label>
								{canOpenPricingSource ? (
									<a
										href={pricingSourceUrl}
										target="_blank"
										rel="noreferrer"
										className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
										data-testid="model-catalog-pricing-source-link"
									>
										Source
										<ExternalLink className="h-3 w-3" />
									</a>
								) : (
									<span className="text-muted-foreground max-w-[260px] truncate text-right font-mono text-xs" title={pricingSourceUrl}>
										{pricingSourceUrl}
									</span>
								)}
							</div>
							<div className="grid grid-cols-2 gap-4">
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">Input</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-input-cost">
										{formatTokenPriceFull(model.input_cost_per_token)}
									</p>
								</div>
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">Output</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-output-cost">
										{formatTokenPriceFull(model.output_cost_per_token)}
									</p>
								</div>
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">Cache Write</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-cache-write-cost">
										{formatTokenPriceFull(model.cache_creation_input_token_cost)}
									</p>
								</div>
								<div className="bg-muted/30 rounded-sm border px-3 py-2">
									<p className="text-muted-foreground text-xs">Cache Read</p>
									<p className="mt-1 font-mono text-sm" data-testid="model-catalog-cache-read-cost">
										{formatTokenPriceFull(model.cache_read_input_token_cost)}
									</p>
								</div>
							</div>
						</div>

						<DottedSeparator />

						{/* Description */}
						<div>
							<Label className="text-sm font-medium">Description</Label>
							<Textarea
								className="mt-2"
								value={description}
								onChange={(e) => setDescription(e.target.value)}
								rows={4}
								placeholder="A short description of this model — shown anywhere additional_attributes.description is consumed."
								data-testid="model-catalog-description-textarea"
							/>
						</div>

						<DottedSeparator />

						{/* Other attributes */}
						<div className="space-y-3">
							<div className="flex items-center justify-between">
								<Label className="text-sm font-medium">Other Attributes</Label>
								<Button type="button" variant="outline" size="sm" onClick={handleAddRow} data-testid="model-catalog-add-attribute-row">
									<Plus className="mr-1 h-3 w-3" />
									Add
								</Button>
							</div>
							{extraRows.length === 0 ? (
								<p className="text-muted-foreground text-xs">
									No additional attributes. Add a key-value pair for anything beyond description.
								</p>
							) : (
								<div className="space-y-2">
									{extraRows.map((row, i) => (
										<div key={row.id} className="flex items-start gap-2">
											<Input
												value={row.key}
												onChange={(e) => handleRowChange(row.id, "key", e.target.value)}
												placeholder="key"
												className="flex-1"
												data-testid={`model-catalog-attribute-key-${i}`}
											/>
											<Input
												value={row.value}
												onChange={(e) => handleRowChange(row.id, "value", e.target.value)}
												placeholder="value"
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

					<div className="bg-card sticky bottom-0 shrink-0 border-t px-8 py-4">
						<div className="flex items-center justify-end gap-3">
							{!hasUpdateAccess && <p className="text-destructive text-sm">You don't have permission to perform this action</p>}
							<Button type="button" variant="outline" onClick={handleClose} data-testid="model-catalog-attribute-cancel">
								Cancel
							</Button>
							<Button
								type="button"
								onClick={handleSubmit}
								disabled={isLoading || !isDirty || !hasUpdateAccess}
								data-testid="model-catalog-attribute-submit"
							>
								{isLoading ? "Saving..." : "Save Changes"}
							</Button>
						</div>
					</div>
				</div>
			</SheetContent>
		</Sheet>
	);
}