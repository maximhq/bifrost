import { Button } from "@/components/ui/button";
import { Form, FormControl, FormField, FormItem, FormLabel, FormMessage } from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ModelMultiselect } from "@/components/ui/modelMultiselect";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import {
	getErrorMessage,
	ModelCatalogEntry,
	useGetProvidersQuery,
	useUpsertModelCatalogEntriesMutation,
} from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import { Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { toast } from "sonner";
import { z } from "zod";

interface AttributeSheetProps {
	entry?: ModelCatalogEntry | null;
	onSave: () => void;
	onCancel: () => void;
}

const formSchema = z.object({
	model: z.string().min(1, "Model is required"),
	provider: z.string().min(1, "Provider is required"),
	description: z.string().optional(),
});

type FormData = z.infer<typeof formSchema>;

// Local row type for the extra-attributes editor. We keep these outside the
// zod schema because empty rows are valid during editing — we filter them
// out at submit time. The id is a render-stable identifier (not persisted)
// so React keeps DOM nodes pinned to the right row across add/remove.
interface AttributeRow {
	id: string;
	key: string;
	value: string;
}

// newRowId mints a render-stable id for an AttributeRow. crypto.randomUUID
// is available in modern browsers; we fall back to a counter for old ones
// and SSR.
let rowIdCounter = 0;
function newRowId(): string {
	if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
		return crypto.randomUUID();
	}
	rowIdCounter += 1;
	return `row-${rowIdCounter}`;
}

// Pulls non-description attributes out of the entry into editable rows.
function rowsFromEntry(entry?: ModelCatalogEntry | null): AttributeRow[] {
	if (!entry?.attributes) return [];
	return Object.entries(entry.attributes)
		.filter(([k]) => k !== "description")
		.map(([key, value]) => ({ id: newRowId(), key, value }));
}

export default function AttributeSheet({ entry, onSave, onCancel }: AttributeSheetProps) {
	const [isOpen, setIsOpen] = useState(true);
	const isEditing = !!entry;

	const hasCreateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const canSubmit = isEditing ? hasUpdateAccess : hasCreateAccess;

	const { data: providersData } = useGetProvidersQuery();
	const [upsertEntries, { isLoading }] = useUpsertModelCatalogEntriesMutation();

	const availableProviders = providersData || [];

	const form = useForm<FormData>({
		mode: "onChange",
		resolver: zodResolver(formSchema),
		defaultValues: {
			model: entry?.model || "",
			provider: entry?.provider || "",
			description: entry?.attributes?.description || "",
		},
	});

	const [extraRows, setExtraRows] = useState<AttributeRow[]>(() => rowsFromEntry(entry));
	// Compare without ids — ids are render-only state.
	const stripIds = (rows: AttributeRow[]) => rows.map(({ key, value }) => ({ key, value }));
	const [initialRowsKey, setInitialRowsKey] = useState(() => JSON.stringify(stripIds(rowsFromEntry(entry))));

	useEffect(() => {
		if (!entry) return;
		if (form.formState.isDirty) return;
		form.reset({
			model: entry.model,
			provider: entry.provider,
			description: entry.attributes?.description || "",
		});
		const next = rowsFromEntry(entry);
		setExtraRows(next);
		setInitialRowsKey(JSON.stringify(stripIds(next)));
	}, [entry, form]);

	const rowsDirty = useMemo(() => JSON.stringify(stripIds(extraRows)) !== initialRowsKey, [extraRows, initialRowsKey]);
	const isDirty = form.formState.isDirty || rowsDirty;

	const handleClose = () => {
		setIsOpen(false);
		setTimeout(() => onCancel(), 150);
	};

	const handleAddRow = () => setExtraRows((prev) => [...prev, { id: newRowId(), key: "", value: "" }]);
	const handleRowChange = (id: string, field: "key" | "value", val: string) => {
		setExtraRows((prev) => prev.map((row) => (row.id === id ? { ...row, [field]: val } : row)));
	};
	const handleRemoveRow = (id: string) => setExtraRows((prev) => prev.filter((row) => row.id !== id));

	const onSubmit = async (data: FormData) => {
		if (!canSubmit) {
			toast.error("You don't have permission to perform this action");
			return;
		}

		// Validate that extra rows have non-empty keys when they have any value.
		// Empty rows are fine — we drop them.
		const cleaned = extraRows
			.map((r) => ({ key: r.key.trim(), value: r.value }))
			.filter((r) => r.key !== "" || r.value !== "");
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
		const desc = (data.description || "").trim();
		if (desc !== "") attributes.description = desc;
		for (const r of cleaned) {
			attributes[r.key] = r.value;
		}

		try {
			await upsertEntries([
				{
					// id is ignored on upsert when (model, provider) matches an
					// existing row; including it for edits lets the backend
					// keep the same row.
					id: entry?.id ?? 0,
					model: data.model,
					provider: data.provider,
					attributes: Object.keys(attributes).length > 0 ? attributes : undefined,
				},
			]).unwrap();
			toast.success(isEditing ? "Catalog entry updated" : "Catalog entry created");
			onSave();
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
					<SheetTitle>{isEditing ? "Edit Model Attributes" : "Add Model Attributes"}</SheetTitle>
					<SheetDescription>
						{isEditing
							? "Update the description and other attributes for this model."
							: "Attach editorial attributes (description, tags) to a model. Decoupled from pricing — these survive the pricing sync."}
					</SheetDescription>
				</SheetHeader>

				<Form {...form}>
					<form onSubmit={form.handleSubmit(onSubmit)} className="flex h-full flex-col gap-6">
						<div className="grow space-y-4 px-8">
							{/* Provider */}
							<FormField
								control={form.control}
								name="provider"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Provider</FormLabel>
										<Select value={field.value} onValueChange={field.onChange} disabled={isEditing}>
											<FormControl>
												<SelectTrigger className="w-full" data-testid="model-catalog-provider-select">
													<SelectValue placeholder="Select a provider" />
												</SelectTrigger>
											</FormControl>
											<SelectContent>
												{availableProviders
													.filter((p) => p.name)
													.map((provider) => (
														<SelectItem key={provider.name} value={provider.name}>
															<RenderProviderIcon
																provider={provider.custom_provider_config?.base_provider_type || (provider.name as KnownProvider)}
																size="sm"
																className="h-4 w-4"
															/>
															{provider.custom_provider_config
																? provider.name
																: ProviderLabels[provider.name as ProviderName] || provider.name}
														</SelectItem>
													))}
											</SelectContent>
										</Select>
										<FormMessage />
									</FormItem>
								)}
							/>

							{/* Model */}
							<FormField
								control={form.control}
								name="model"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Model</FormLabel>
										<FormControl>
											<div data-testid="model-catalog-model-select">
												<ModelMultiselect
													provider={form.watch("provider") || undefined}
													value={field.value}
													onChange={field.onChange}
													placeholder="Search for a model..."
													isSingleSelect
													loadModelsOnEmptyProvider="base_models"
													disabled={isEditing}
												/>
											</div>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>

							<DottedSeparator />

							{/* Description */}
							<FormField
								control={form.control}
								name="description"
								render={({ field }) => (
									<FormItem>
										<FormLabel>Description</FormLabel>
										<FormControl>
											<Textarea
												{...field}
												rows={4}
												placeholder="A short description of this model — shown anywhere attributes.description is consumed."
												data-testid="model-catalog-description-textarea"
											/>
										</FormControl>
										<FormMessage />
									</FormItem>
								)}
							/>

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
								{!canSubmit && <p className="text-destructive text-sm">You don't have permission to perform this action</p>}
								<Button type="button" variant="outline" onClick={handleClose} data-testid="model-catalog-attribute-cancel">
									Cancel
								</Button>
								<Button
									type="submit"
									data-testid="model-catalog-attribute-submit"
									disabled={isLoading || !isDirty || !canSubmit}
								>
									{isLoading ? "Saving..." : isEditing ? "Save Changes" : "Add Attributes"}
								</Button>
							</div>
						</div>
					</form>
				</Form>
			</SheetContent>
		</Sheet>
	);
}
