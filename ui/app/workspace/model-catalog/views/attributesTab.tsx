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
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Input } from "@/components/ui/input";
import FullPageLoader from "@/components/fullPageLoader";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import {
	getErrorMessage,
	ModelCatalogEntry,
	useDeleteModelCatalogEntryMutation,
	useGetModelCatalogQuery,
} from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Edit, MoreHorizontal, Plus, Search, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import AttributeSheet from "./attributeSheet";

const toTestIdPart = (value: string) =>
	value
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-|-$/g, "");

// Renders the description column. Long descriptions are truncated to keep the
// table scannable; the full text is reachable via tooltip.
function DescriptionCell({ description }: { description?: string }) {
	if (!description) return <span className="text-muted-foreground text-sm">—</span>;
	const truncated = description.length > 80;
	const text = truncated ? `${description.slice(0, 80)}…` : description;
	if (!truncated) return <span className="text-sm">{text}</span>;
	return (
		<TooltipProvider>
			<Tooltip>
				<TooltipTrigger asChild>
					<span className="text-sm">{text}</span>
				</TooltipTrigger>
				<TooltipContent className="max-w-md">{description}</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

function ActionsMenu({
	entry,
	hasUpdateAccess,
	hasDeleteAccess,
	onEdit,
	onDelete,
}: {
	entry: ModelCatalogEntry;
	hasUpdateAccess: boolean;
	hasDeleteAccess: boolean;
	onEdit: (entry: ModelCatalogEntry) => void;
	onDelete: (entry: ModelCatalogEntry) => void;
}) {
	const [isOpen, setIsOpen] = useState(false);
	const testKey = `${toTestIdPart(entry.model)}-${toTestIdPart(entry.provider)}`;
	return (
		<DropdownMenu open={isOpen} onOpenChange={setIsOpen}>
			<DropdownMenuTrigger asChild onClick={(e) => e.stopPropagation()}>
				<Button
					variant="ghost"
					size="icon"
					className="h-8 w-8"
					aria-label={`Actions for ${entry.model}`}
					data-testid={`model-catalog-actions-${testKey}`}
				>
					<MoreHorizontal className="h-4 w-4" />
				</Button>
			</DropdownMenuTrigger>
			<DropdownMenuContent align="end">
				<DropdownMenuItem
					className="cursor-pointer"
					disabled={!hasUpdateAccess}
					data-testid={`model-catalog-edit-${testKey}`}
					onSelect={(e) => {
						e.preventDefault();
						onEdit(entry);
						setIsOpen(false);
					}}
				>
					<Edit className="h-4 w-4" />
					Edit
				</DropdownMenuItem>
				<DropdownMenuItem
					variant="destructive"
					className="cursor-pointer"
					disabled={!hasDeleteAccess}
					data-testid={`model-catalog-delete-${testKey}`}
					onSelect={(e) => {
						e.preventDefault();
						onDelete(entry);
						setIsOpen(false);
					}}
				>
					<Trash2 className="h-4 w-4" />
					Delete
				</DropdownMenuItem>
			</DropdownMenuContent>
		</DropdownMenu>
	);
}

interface AttributesTabProps {
	hasAccess: boolean;
}

export default function AttributesTab({ hasAccess }: AttributesTabProps) {
	const hasCreateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Create);
	const hasUpdateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);
	const hasDeleteAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Delete);

	const { data: entries, isLoading, error, refetch } = useGetModelCatalogQuery(undefined, { skip: !hasAccess });
	const [deleteEntry, { isLoading: isDeleting }] = useDeleteModelCatalogEntryMutation();

	const [search, setSearch] = useState("");
	const [showSheet, setShowSheet] = useState(false);
	const [editingId, setEditingId] = useState<number | null>(null);
	const [deleteCandidateId, setDeleteCandidateId] = useState<number | null>(null);

	const filtered = useMemo(() => {
		if (!entries) return [];
		const q = search.trim().toLowerCase();
		if (!q) return entries;
		return entries.filter(
			(e) =>
				e.model.toLowerCase().includes(q) ||
				e.provider.toLowerCase().includes(q) ||
				(e.attributes?.description || "").toLowerCase().includes(q),
		);
	}, [entries, search]);

	const editingEntry = useMemo(
		() => (editingId !== null && entries ? entries.find((e) => e.id === editingId) ?? null : null),
		[editingId, entries],
	);
	const deleteCandidate = useMemo(
		() => (deleteCandidateId !== null && entries ? entries.find((e) => e.id === deleteCandidateId) ?? null : null),
		[deleteCandidateId, entries],
	);

	const handleAdd = () => {
		setEditingId(null);
		setShowSheet(true);
	};
	const handleEdit = (entry: ModelCatalogEntry) => {
		setEditingId(entry.id);
		setShowSheet(true);
	};
	const handleSheetSaved = () => {
		setShowSheet(false);
		setEditingId(null);
	};
	const handleDelete = async () => {
		if (!deleteCandidate) return;
		try {
			await deleteEntry(deleteCandidate.id).unwrap();
			toast.success("Catalog entry deleted");
			setDeleteCandidateId(null);
		} catch (err) {
			toast.error(getErrorMessage(err));
		}
	};

	if (isLoading) return <FullPageLoader />;

	if (error) {
		return (
			<div className="flex h-full flex-col items-center justify-center gap-4 text-center">
				<p className="text-muted-foreground text-sm">Failed to load model catalog</p>
				<button type="button" onClick={refetch} className="text-sm underline" data-testid="model-catalog-retry-button">
					Retry
				</button>
			</div>
		);
	}

	const hasAnyEntries = (entries?.length ?? 0) > 0;

	return (
		<>
			{showSheet && <AttributeSheet entry={editingEntry} onSave={handleSheetSaved} onCancel={() => setShowSheet(false)} />}

			<AlertDialog open={!!deleteCandidate} onOpenChange={(open) => !open && setDeleteCandidateId(null)}>
				<AlertDialogContent>
					<AlertDialogHeader>
						<AlertDialogTitle>Delete Model Attributes</AlertDialogTitle>
						<AlertDialogDescription>
							Remove attributes for &quot;{deleteCandidate?.provider}/{deleteCandidate?.model}&quot;? This cannot be undone.
						</AlertDialogDescription>
					</AlertDialogHeader>
					<AlertDialogFooter>
						<AlertDialogCancel data-testid="model-catalog-delete-cancel">Cancel</AlertDialogCancel>
						<AlertDialogAction
							onClick={handleDelete}
							disabled={isDeleting}
							className="bg-red-600 hover:bg-red-700"
							data-testid="model-catalog-delete-confirm"
						>
							{isDeleting ? "Deleting..." : "Delete"}
						</AlertDialogAction>
					</AlertDialogFooter>
				</AlertDialogContent>
			</AlertDialog>

			<div className="space-y-4">
				<div className="flex items-center justify-between">
					<div>
						<h2 className="text-lg font-semibold">Model Attributes</h2>
						<p className="text-muted-foreground text-sm">
							Attach descriptions and tags to specific models. Editorial — decoupled from pricing sync.
						</p>
					</div>
					<Button onClick={handleAdd} disabled={!hasCreateAccess} data-testid="model-catalog-button-create">
						<Plus className="h-4 w-4" />
						Add Attributes
					</Button>
				</div>

				<div className="flex items-center gap-3">
					<div className="relative max-w-sm flex-1">
						<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
						<Input
							aria-label="Search model catalog"
							placeholder="Search by model, provider, or description..."
							value={search}
							onChange={(e) => setSearch(e.target.value)}
							className="pl-9"
							data-testid="model-catalog-search-input"
						/>
					</div>
				</div>

				<div className="rounded-sm border" data-testid="model-catalog-attributes-table">
					<Table>
						<TableHeader>
							<TableRow className="hover:bg-transparent">
								<TableHead className="font-medium">Provider</TableHead>
								<TableHead className="font-medium">Model</TableHead>
								<TableHead className="font-medium">Description</TableHead>
								<TableHead className="font-medium">Other</TableHead>
								<TableHead className="w-[60px]"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{!hasAnyEntries ? (
								<TableRow>
									<TableCell colSpan={5} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">
											No model attributes yet. Click &quot;Add Attributes&quot; to create the first one.
										</span>
									</TableCell>
								</TableRow>
							) : filtered.length === 0 ? (
								<TableRow>
									<TableCell colSpan={5} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">No matching entries.</span>
									</TableCell>
								</TableRow>
							) : (
								filtered.map((entry) => {
									const extraKeys = Object.keys(entry.attributes || {}).filter((k) => k !== "description");
									return (
										<TableRow
											key={entry.id}
											data-testid={`model-catalog-row-${toTestIdPart(entry.model)}-${toTestIdPart(entry.provider)}`}
										>
											<TableCell className="py-3">
												<div className="flex items-center gap-2">
													<RenderProviderIcon
														provider={entry.provider as KnownProvider}
														size="sm"
														className="h-4 w-4"
													/>
													<span className="text-sm">{ProviderLabels[entry.provider as ProviderName] || entry.provider}</span>
												</div>
											</TableCell>
											<TableCell className="py-3 font-mono text-sm">{entry.model}</TableCell>
											<TableCell className="max-w-[400px] py-3">
												<DescriptionCell description={entry.attributes?.description} />
											</TableCell>
											<TableCell className="py-3">
												{extraKeys.length === 0 ? (
													<span className="text-muted-foreground text-sm">—</span>
												) : (
													<Badge variant="secondary">
														{extraKeys.length} {extraKeys.length === 1 ? "attribute" : "attributes"}
													</Badge>
												)}
											</TableCell>
											<TableCell className="py-3">
												<ActionsMenu
													entry={entry}
													hasUpdateAccess={hasUpdateAccess}
													hasDeleteAccess={hasDeleteAccess}
													onEdit={handleEdit}
													onDelete={(e) => setDeleteCandidateId(e.id)}
												/>
											</TableCell>
										</TableRow>
									);
								})
							)}
						</TableBody>
					</Table>
				</div>
			</div>
		</>
	);
}
