"use client";

import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { ChevronDown, Plus, X } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import type { FieldErrors, PricingFieldKey } from "./pricingOverrideDrawer";
import { PRICING_FIELDS } from "./pricingOverrideDrawer";

const PRICING_GROUPS = [
	{ key: "token" as const, label: "Token" },
	{ key: "cache" as const, label: "Cache" },
	{ key: "image" as const, label: "Image" },
	{ key: "av" as const, label: "Audio & Video" },
	{ key: "other" as const, label: "Other" },
];

type GroupKey = (typeof PRICING_GROUPS)[number]["key"];

interface PricingFieldSelectorProps {
	values: Partial<Record<PricingFieldKey, string>>;
	errors: FieldErrors;
	onChange: (key: PricingFieldKey, value: string) => void;
	onFieldInteraction?: () => void;
}

export function PricingFieldSelector({ values, errors, onChange, onFieldInteraction }: PricingFieldSelectorProps) {
	const [search, setSearch] = useState("");
	const [openGroups, setOpenGroups] = useState<Set<GroupKey>>(new Set(["token"]));

	const [activeFields, setActiveFields] = useState<Set<PricingFieldKey>>(
		() => new Set(PRICING_FIELDS.filter((f) => values[f.key] != null && values[f.key]!.trim() !== "").map((f) => f.key)),
	);

	// Auto-activate fields that gain values (e.g., from JSON editing or loading an existing override)
	useEffect(() => {
		setActiveFields((prev) => {
			const next = new Set(prev);
			for (const f of PRICING_FIELDS) {
				if (values[f.key] != null && values[f.key]!.trim() !== "") {
					next.add(f.key);
				}
			}
			return next;
		});
	}, [values]);

	const trimmedSearch = search.trim().toLowerCase();
	const isSearching = trimmedSearch.length > 0;

	const filteredFields = useMemo(() => {
		if (!isSearching) return null;
		return PRICING_FIELDS.filter((f) => f.label.toLowerCase().includes(trimmedSearch) || f.key.toLowerCase().includes(trimmedSearch));
	}, [isSearching, trimmedSearch]);

	const groupedFields = useMemo(
		() =>
			PRICING_GROUPS.map((group) => ({
				...group,
				fields: PRICING_FIELDS.filter((f) => f.group === group.key),
			})),
		[],
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
				placeholder="Search pricing fields..."
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
						{groupedFields.map((group) => {
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
											className={cn(
												"text-muted-foreground h-4 w-4 transition-transform duration-200",
												isOpen && "rotate-180",
											)}
										/>
									</button>

									{isOpen && (
										<div className="bg-muted/20 space-y-0.5 border-t px-2 pb-2 pt-1">
											{group.fields.map((field) => renderFieldRow(field))}
										</div>
									)}
								</div>
							);
						})}
					</div>
				)}
			</div>
		</div>
	);
}
