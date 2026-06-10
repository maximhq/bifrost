import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scrollArea";
import { useToast } from "@/hooks/use-toast";
import { getErrorMessage, useGetMCPClientsQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ArrowLeft, ExternalLink, PackagePlus } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { MCP_LIBRARY_SERVERS, MCPLibraryServer } from "./data";
import { MCPLibraryInstallSheet, sanitizeServerName } from "./views/mcpLibraryInstallSheet";
const VIEW_MODE_STORAGE_KEY = "mcp-library-view-mode";
type MCPLibraryViewMode = "grid" | "table";

function getInitialViewMode(): MCPLibraryViewMode {
	if (typeof window === "undefined") return "table";
	try {
		const savedViewMode = window.localStorage.getItem(VIEW_MODE_STORAGE_KEY);
		return savedViewMode === "grid" || savedViewMode === "table" ? savedViewMode : "table";
	} catch {
		return "table";
	}
}

export default function MCPLibraryPage() {
	const hasCreateMCPClientAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Create);
	const hasDeleteMCPLibraryAccess = useRbac(RbacResource.MCPGateway, RbacOperation.Delete);
	const [selectedServer, setSelectedServer] = useState<MCPLibraryServer | null>(null);
	const [viewMode, setViewMode] = useState<MCPLibraryViewMode>(getInitialViewMode);
	const { toast } = useToast();

	const {
		data: mcpClientsData,
		error,
		refetch,
	} = useGetMCPClientsQuery({
		limit: 100,
		offset: 0,
	});

	useEffect(() => {
		if (!error) return;
		const message = getErrorMessage(error);
		if (message.toLowerCase().includes("mcp is not configured in this bifrost instance")) return;
		toast({ title: "Error", description: message, variant: "destructive" });
	}, [error, toast]);

	const installedServerSlugs = useMemo(() => {
		const clients = mcpClientsData?.clients || [];
		return new Set(
			MCP_LIBRARY_SERVERS.filter((server) =>
				clients.some((client) => {
					const connectionString = client.config.connection_string;
					const connectionUrl = connectionString?.from_env ? connectionString.env_var : connectionString?.value;
					return connectionUrl === server.url || client.config.name.toLowerCase() === server.name.toLowerCase();
				}),
			).map((server) => server.slug),
		);
	}, [mcpClientsData?.clients]);

	const handleInstalled = async () => {
		await refetch();
	};
	const isCatalogEmpty = !isFetching && totalCount === 0 && !debouncedSearch && !hasActiveFilters;

	return (
		<div className="mx-auto w-full max-w-7xl space-y-6">
			<div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
				<div className="space-y-1">
					<Button asChild variant="ghost" className="mb-2 -ml-2 gap-2" data-testid="mcp-library-back-btn">
						<Link to="/workspace/mcp-registry">
							<ArrowLeft className="h-4 w-4" />
							MCP Catalog
						</Link>
					</Button>
					<h2 className="text-lg font-semibold tracking-tight">MCP Server Library</h2>
					<p className="text-muted-foreground max-w-2xl text-sm">
						Install admin-curated MCP servers with prefilled connection details and your preferred authentication method.
					</p>
				</div>
			</div>

			<div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
				{MCP_LIBRARY_SERVERS.map((server) => {
					const isInstalled = installedServerSlugs.has(server.slug);
					return (
						<Card key={server.slug} className="gap-4" data-testid={`mcp-library-card-${server.slug}`}>
							<CardHeader className="grid-cols-[auto_1fr] gap-3">
								<img src={server.logo} alt="" className="row-span-2 h-11 w-11 rounded-sm border bg-white object-contain p-1" />
								<CardTitle className="flex min-w-0 items-center gap-2">
									<span className="truncate">{server.name}</span>
									{isInstalled && <Badge variant="success">Installed</Badge>}
								</CardTitle>
								<CardDescription className="line-clamp-2">{server.description}</CardDescription>
							</CardHeader>
							<CardContent className="flex flex-1 flex-col gap-3">
								<div className="space-y-1">
									<p className="text-muted-foreground text-xs font-medium">Connection URL</p>
									<p className="bg-muted truncate rounded-sm px-2 py-1.5 text-xs" title={server.url}>
										{server.url}
									</p>
								</div>
							</CardContent>
							<CardFooter className="justify-between gap-2">
								<Button asChild variant="outline" data-testid={`mcp-library-docs-${server.slug}`}>
									<a href={server.link} target="_blank" rel="noreferrer">
										<ExternalLink className="h-4 w-4" />
										Docs
									</a>
								</Button>
								<Button
									onClick={() => setSelectedServer(server)}
									disabled={isInstalled || !hasCreateMCPClientAccess}
									data-testid={`mcp-library-install-${server.slug}`}
								>
									<PackagePlus className="h-4 w-4" />
									{isInstalled ? "Installed" : "Install"}
								</Button>
							</CardFooter>
						</Card>
					);
				})}
			</div>

			{selectedServer && (
				<MCPLibraryInstallSheet
					server={selectedServer}
					open={!!selectedServer}
					onClose={() => setSelectedServer(null)}
					onInstalled={handleInstalled}
				/>
			)}
		</div>
	);
}