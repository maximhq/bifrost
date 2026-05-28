import FullPageLoader from "@/components/fullPageLoader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { RenderProviderIcon } from "@/lib/constants/icons";
import { ProviderLabels, ProviderName } from "@/lib/constants/logs";
import {
	ModelDetails,
	useGetModelDetailsQuery,
	useGetProvidersQuery,
} from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Edit, Search } from "lucide-react";
import { useMemo, useState } from "react";
import AttributeSheet from "./attributeSheet";

// High enough to cover the full pricing catalog (~1.5k entries) so the tab
// renders the entire matching set without paginating. The server's `query`
// + `provider` filters do the narrowing.
const MAX_RESULTS = 1000;

const toTestIdPart = (value: string) =>
	value
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, "-")
		.replace(/^-|-$/g, "");

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

interface AttributesTabProps {
	hasAccess: boolean;
}

export default function AttributesTab({ hasAccess }: AttributesTabProps) {
	const hasUpdateAccess = useRbac(RbacResource.ModelProvider, RbacOperation.Update);

	const [search, setSearch] = useState("");
	const [providerFilter, setProviderFilter] = useState<string>("");
	const [editing, setEditing] = useState<ModelDetails | null>(null);

	const debouncedSearch = useDebouncedValue(search, 300);

	const { data: providersData } = useGetProvidersQuery(undefined, { skip: !hasAccess });
	const { data, isLoading, error, refetch } = useGetModelDetailsQuery(
		{
			query: debouncedSearch || undefined,
			provider: providerFilter || undefined,
			limit: MAX_RESULTS,
			unfiltered: true,
		},
		{ skip: !hasAccess },
	);

	const models = data?.models ?? [];

	const providerOptions = useMemo(
		() => Array.from(new Set((providersData ?? []).map((p) => p.name))).sort(),
		[providersData],
	);

	if (isLoading && !data) return <FullPageLoader />;

	if (error) {
		return (
			<div className="flex h-full flex-col items-center justify-center gap-4 text-center">
				<p className="text-muted-foreground text-sm">Failed to load models</p>
				<button type="button" onClick={refetch} className="text-sm underline" data-testid="model-catalog-retry-button">
					Retry
				</button>
			</div>
		);
	}

	return (
		<>
			{editing && <AttributeSheet model={editing} onClose={() => setEditing(null)} />}

			<div className="space-y-4">
				<div className="flex items-center justify-between">
					<div>
						<h2 className="text-lg font-semibold">Models</h2>
						<p className="text-muted-foreground text-sm">
							Attach descriptions and tags to specific models. Editorial — decoupled from pricing sync.
						</p>
					</div>
				</div>

				<div className="flex items-center gap-3">
					<div className="relative max-w-sm flex-1">
						<Search className="text-muted-foreground absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2" />
						<Input
							aria-label="Search models"
							placeholder="Search by model name..."
							value={search}
							onChange={(e) => setSearch(e.target.value)}
							className="pl-9"
							data-testid="model-catalog-search-input"
						/>
					</div>
					<Select value={providerFilter || "__all__"} onValueChange={(v) => setProviderFilter(v === "__all__" ? "" : v)}>
						<SelectTrigger className="w-[200px]" data-testid="model-catalog-provider-filter">
							<SelectValue placeholder="All providers" />
						</SelectTrigger>
						<SelectContent>
							<SelectItem value="__all__">All providers</SelectItem>
							{providerOptions.map((p) => (
								<SelectItem key={p} value={p}>
									{ProviderLabels[p as ProviderName] || p}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
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
							{models.length === 0 ? (
								<TableRow>
									<TableCell colSpan={5} className="h-24 text-center">
										<span className="text-muted-foreground text-sm">
											{!debouncedSearch && !providerFilter ? "No models loaded yet." : "No matching models."}
										</span>
									</TableCell>
								</TableRow>
							) : (
								models.map((m) => {
									const attrs = m.additional_attributes ?? {};
									const extraKeys = Object.keys(attrs).filter((k) => k !== "description");
									const testKey = `${toTestIdPart(m.name)}-${toTestIdPart(m.provider)}`;
									return (
										<TableRow key={`${m.provider}|${m.name}`} data-testid={`model-catalog-row-${testKey}`}>
											<TableCell className="py-3">
												<div className="flex items-center gap-2">
													<RenderProviderIcon
														provider={m.provider as KnownProvider}
														size="sm"
														className="h-4 w-4"
													/>
													<span className="text-sm">{ProviderLabels[m.provider as ProviderName] || m.provider}</span>
												</div>
											</TableCell>
											<TableCell className="py-3 font-mono text-sm">{m.name}</TableCell>
											<TableCell className="max-w-[400px] py-3">
												<DescriptionCell description={attrs.description} />
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
												<Button
													variant="ghost"
													size="icon"
													className="h-8 w-8"
													disabled={!hasUpdateAccess}
													onClick={() => setEditing(m)}
													aria-label={`Edit attributes for ${m.name}`}
													data-testid={`model-catalog-edit-${testKey}`}
												>
													<Edit className="h-4 w-4" />
												</Button>
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
