import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { ChevronDown, Plus, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { FieldErrors, PricingFieldKey } from "./pricingOverrideSheet";
import { PRICING_FIELDS } from "./pricingOverrideSheet";

type GroupKey = "chat" | "embedding" | "rerank" | "audio" | "image" | "video" | "ocr";

const PRICING_GROUPS: { key: GroupKey; label: string }[] = [
	{ key: "chat", label: "Chat / Text / Responses" },
	{ key: "embedding", label: "Embedding" },
	{ key: "rerank", label: "Rerank" },
	{ key: "audio", label: "Audio" },
	{ key: "image", label: "Image" },
	{ key: "video", label: "Video" },
	{ key: "ocr", label: "OCR" },
];

const REQUEST_TYPE_TO_CATEGORY: Record<string, GroupKey> = {
	chat_completion: "chat",
	text_completion: "chat",
	responses: "chat",
	embedding: "embedding",
	rerank: "rerank",
	speech: "audio",
	transcription: "audio",
	image_generation: "image",
	image_variation: "image",
	image_edit: "image",
	video_generation: "video",
	video_remix: "video",
	ocr: "ocr",
};

interface PricingFieldSelectorProps {
	values: Partial<Record<PricingFieldKey, string>>;
	errors: FieldErrors;
	selectedRequestTypes?: string[];
	onChange: (key: PricingFieldKey, value: string) => void;
	onFieldInteraction?: () => void;
}

export function PricingFieldSelector({ values, errors, selectedRequestTypes, onChange, onFieldInteraction }: PricingFieldSelectorProps) {
	const [search, setSearch] = useState("");
	const [openGroups, setOpenGroups] = useState<Set<GroupKey>>(new Set(["chat"]));

	const [activeFields, setActiveFields] = useState<Set<PricingFieldKey>>(
		() => new Set(PRICING_FIELDS.filter((f) => values[f.key] != null && values[f.key]!.trim() !== "").map((f) => f.key)),
	);

	// Sync active fields to exactly the set of keys that have non-empty values.
	// This handles both loading new overrides (adds keys) and clearing the patch (removes stale keys).
	useEffect(() => {
		setActiveFields(new Set(PRICING_FIELDS.filter((f) => values[f.key] != null && values[f.key]!.trim() !== "").map((f) => f.key)));
	}, [values]);

	// Derive active categories from selected request types
	const activeCategories = useMemo<Set<GroupKey> | null>(() => {
		if (!selectedRequestTypes || selectedRequestTypes.length === 0) return null;
		const cats = new Set<GroupKey>();
		for (const rt of selectedRequestTypes) {
			const cat = REQUEST_TYPE_TO_CATEGORY[rt];
			if (cat) cats.add(cat);
		}
		return cats.size > 0 ? cats : null;
	}, [selectedRequestTypes]);

	const trimmedSearch = search.trim().toLowerCase();
	const isSearching = trimmedSearch.length > 0;

	const filteredFields = useMemo(() => {
		if (!isSearching) return null;
		return PRICING_FIELDS.filter((f) => f.label.toLowerCase().includes(trimmedSearch) || f.key.toLowerCase().includes(trimmedSearch));
	}, [isSearching, trimmedSearch]);

	// Fields visible per group when not searching, respecting activeCategories filter
	const visibleGroupedFields = useMemo(
		() =>
			PRICING_GROUPS.map((group) => {
				const fields = PRICING_FIELDS.filter((f) => {
					if (f.group !== group.key) return false;
					if (activeCategories === null) return true;
					return (f.requestTypeGroups as readonly string[]).some((rg) => activeCategories.has(rg as GroupKey));
				});
				return { ...group, fields };
			}).filter((g) => g.fields.length > 0),
		[activeCategories],
	);

	const toggleGroup = (key: GroupKey) => {
		setOpenGroups((prev) => {
			const next = new Set(prev);
			if (next.has(key)) next.delete(key);
			else next.add(key);
			return next;
		});
	};

	const activateField = (key: PricingFieldKey) => {
		setActiveFields((prev) => new Set([...prev, key]));
	};

	const deactivateField = (key: PricingFieldKey) => {
		setActiveFields((prev) => {
			const next = new Set(prev);
			next.delete(key);
			return next;
		});
		onFieldInteraction?.();
		onChange(key, "");
	};

	const handleInputChange = (key: PricingFieldKey, value: string) => {
		onFieldInteraction?.();
		onChange(key, value);
	};

	const renderFieldRow = (field: { key: PricingFieldKey; label: string }) => {
		const isActive = activeFields.has(field.key);
		const hasValue = values[field.key]?.trim();
		const error = errors[field.key];

		if (!isActive) {
			return (
				<button
					key={field.key}
					type="button"
					className="hover:bg-muted flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm transition-colors"
					onClick={() => activateField(field.key)}
					data-testid={`pricing-field-activate-${field.key}`}
				>
					<Plus className="text-muted-foreground h-3.5 w-3.5 shrink-0" />
					<span className="text-muted-foreground">{field.label}</span>
				</button>
			);
		}

		return (
			<div key={field.key} className="rounded-sm px-1 py-1.5">
				<div className="mb-1 flex items-center gap-2">
					<span className="flex-1 text-sm font-medium">{field.label}</span>
					<button
						type="button"
						className="text-muted-foreground hover:text-foreground rounded-sm p-0.5 transition-colors"
						onClick={() => deactivateField(field.key)}
						data-testid={`pricing-field-deactivate-${field.key}`}
						title="Remove field"
					>
						<X className="h-3.5 w-3.5" />
					</button>
				</div>
				<Input
					data-testid={`pricing-override-field-input-${field.key}`}
					type="text"
					inputMode="decimal"
					className={cn("h-8", hasValue && "ring-primary/40 ring-1")}
					value={values[field.key] ?? ""}
					onChange={(e) => handleInputChange(field.key, e.target.value)}
					placeholder="0.0"
				/>
				{error && <p className="text-destructive mt-1 text-xs">{error}</p>}
			</div>
		);
	};

	return (
		<div className="space-y-2">
			<Input
				placeholder="Search all pricing fields..."
				value={search}
				onChange={(e) => setSearch(e.target.value)}
				className="h-9"
				data-testid="pricing-field-search"
			/>

			<div className="rounded-md border">
				{isSearching ? (
					<div className="space-y-0.5 p-2">
						{filteredFields!.length === 0 ? (
							<div className="text-muted-foreground py-4 text-center text-sm">No fields match &ldquo;{search}&rdquo;</div>
						) : (
							filteredFields!.map((field) => renderFieldRow(field))
						)}
					</div>
				) : (
					<div className="divide-y">
						{visibleGroupedFields.length === 0 ? (
							<div className="text-muted-foreground py-4 text-center text-sm">No pricing fields for the selected request types</div>
						) : (
							visibleGroupedFields.map((group) => {
								const isOpen = openGroups.has(group.key);
								const valueCount = group.fields.filter((f) => values[f.key]?.trim()).length;

								return (
									<div key={group.key}>
										<button
											type="button"
											className="hover:bg-muted/50 flex w-full items-center justify-between px-3 py-2.5 text-sm font-medium transition-colors"
											onClick={() => toggleGroup(group.key)}
											data-testid={`pricing-group-toggle-${group.key}`}
										>
											<span className="flex items-center gap-2">
												{group.label}
												{valueCount > 0 && (
													<Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
														{valueCount}
													</Badge>
												)}
											</span>
											<ChevronDown
												className={cn("text-muted-foreground h-4 w-4 transition-transform duration-200", isOpen && "rotate-180")}
											/>
										</button>

										{isOpen && (
											<div className="bg-muted/20 space-y-0.5 border-t px-2 pt-1 pb-2">
												{group.fields.map((field) => renderFieldRow(field))}
											</div>
										)}
									</div>
								);
							})
						)}
					</div>
				)}
			</div>
		</div>
	);
}