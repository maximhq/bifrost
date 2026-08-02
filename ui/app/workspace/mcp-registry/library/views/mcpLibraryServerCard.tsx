import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { getErrorMessage, useDeleteMCPLibraryEntryMutation } from "@/lib/store";
import type { MCPLibraryEntry } from "@/lib/types/mcp";
import { Link } from "@tanstack/react-router";
import { BookIcon, Globe, Radio, Terminal, Trash2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { MCPLibraryDeleteDialog } from "./mcpLibraryDeleteDialog";

const MAX_VISIBLE_TAGS = 3;
export const MCP_ICON_FALLBACK = "/images/mcp.svg";

/** Map connection_type to a human-friendly transport label. */
export function transportLabel(connectionType: string): string {
	switch (connectionType) {
		case "stdio":
			return "stdio";
		case "sse":
			return "SSE";
		default:
			return "HTTP";
	}
}

/** Map connection_type to an icon for the compact transport badge. */
export function transportIcon(connectionType: string) {
	switch (connectionType) {
		case "stdio":
			return <Terminal className="size-4" />;
		case "sse":
			return <Radio className="size-4" />;
		default:
			return <Globe className="size-4" />;
	}
}

export function authLabel(authType?: string): string {
	switch (authType) {
		case "headers":
			return "Headers";
		case "oauth":
			return "OAuth";
		case "per_user_headers":
			return "User headers";
		case "per_user_oauth":
			return "User OAuth";
		default:
			return "No auth";
	}
}

interface MCPLibraryServerCardProps {
	server: MCPLibraryEntry;
	isInstalled: boolean;
	canCreateMCPClient: boolean;
	canDelete: boolean;
	onInstall: (server: MCPLibraryEntry) => void;
}

export function MCPLibraryServerCard({ server, isInstalled, canCreateMCPClient, canDelete, onInstall }: MCPLibraryServerCardProps) {
	const [deleteEntry, { isLoading: isDeleting }] = useDeleteMCPLibraryEntryMutation();
	const [confirmOpen, setConfirmOpen] = useState(false);
	const isCustom = server.source === "custom";

	const handleDelete = async () => {
		try {
			await deleteEntry(server.id).unwrap();
			toast.success(`"${server.name}" removed from the library.`);
			setConfirmOpen(false);
		} catch (error) {
			toast.error(getErrorMessage(error));
		}
	};
	return (
		<Card
			key={server.slug}
			className="group hover:border-primary/30 hover:shadow-primary/5 h-full gap-0 overflow-hidden py-0 shadow-none transition-all duration-200 hover:shadow-md"
			data-testid={`mcp-library-card-${server.slug}`}
		>
			<CardHeader className="bg-muted/20 border-b px-4 py-4">
				<div className="flex min-w-0 items-start gap-3">
					<div className="bg-background flex h-12 w-12 shrink-0 items-center justify-center overflow-hidden rounded-md border shadow-xs">
						{server.icon_url ? (
							<img
								src={server.icon_url}
								alt=""
								className="h-full w-full object-contain p-1.5"
								onError={(event) => {
									event.currentTarget.onerror = null;
									event.currentTarget.src = MCP_ICON_FALLBACK;
								}}
							/>
						) : (
							<img src={"/images/mcp.svg"} alt="" className="h-full w-full object-contain p-1.5" />
						)}
					</div>
					<div className="min-w-0 flex-1 space-y-1">
						<div className="flex min-w-0 items-start justify-between gap-2">
							<CardTitle className="min-w-0 pt-0.5 text-sm leading-5">
								<span className="block truncate">{server.name}</span>
							</CardTitle>
							<div className="flex shrink-0 items-center gap-1.5">
								{isCustom && <Badge variant="outline">Custom</Badge>}
								{isInstalled && <Badge variant="success">Installed</Badge>}
							</div>
						</div>
						<div className="flex min-w-0 flex-wrap items-center gap-1.5">
							{server.category && (
								<Badge variant="outline" className="bg-background/70 max-w-full truncate">
									{server.category}
								</Badge>
							)}
							{server.publisher && <span className="text-muted-foreground min-w-0 truncate text-xs">by {server.publisher}</span>}
						</div>
					</div>
				</div>
			</CardHeader>
			<CardContent className="flex flex-1 flex-col gap-3 px-4 py-3">
				<CardDescription className="line-clamp-2 min-h-10 leading-5">{server.description || "No description available."}</CardDescription>
				{server.tags && server.tags.length > 0 && (
					<div className="flex min-w-0 flex-wrap items-center gap-1.5">
						{server.tags.slice(0, MAX_VISIBLE_TAGS).map((tag) => (
							<Badge key={tag} variant="secondary" className="max-w-full truncate text-xs">
								{tag}
							</Badge>
						))}
						{server.tags.length > MAX_VISIBLE_TAGS && (
							<Tooltip>
								<TooltipTrigger asChild>
									<Badge variant="secondary" className="shrink-0 text-xs">
										+{server.tags.length - MAX_VISIBLE_TAGS}
									</Badge>
								</TooltipTrigger>
								<TooltipContent className="flex max-w-xs flex-wrap gap-1">
									{server.tags.slice(MAX_VISIBLE_TAGS).map((tag) => (
										<Badge key={tag} variant="secondary" className="text-xs">
											{tag}
										</Badge>
									))}
								</TooltipContent>
							</Tooltip>
						)}
					</div>
				)}
			</CardContent>
			<CardFooter className="bg-muted/10 mt-auto justify-between gap-3 border-t px-4 py-3 !pt-3">
				<div className="text-muted-foreground flex min-w-0 items-center gap-2 text-xs">
					<span className="flex shrink-0 items-center gap-1">
						{transportIcon(server.connection_type)}
						{transportLabel(server.connection_type)}
					</span>
					<span className="bg-border h-3 w-px shrink-0" />
					<span className="truncate">{authLabel(server.auth_type)}</span>
				</div>
				<div className="flex shrink-0 items-center gap-2">
					{canDelete && (
						<div className="fade-in hidden group-hover:block">
							<Tooltip>
								<TooltipTrigger asChild>
									<Button
										variant="outline"
										size="sm"
										onClick={() => setConfirmOpen(true)}
										aria-label={`Remove ${server.name} from library`}
										data-testid={`mcp-library-delete-${server.slug}`}
									>
										<Trash2 className="h-4 w-4" />
									</Button>
								</TooltipTrigger>
								<TooltipContent>Remove from library</TooltipContent>
							</Tooltip>
						</div>
					)}
					{server.docs_url && (
						<Tooltip>
							<TooltipTrigger asChild>
								<Button
									asChild
									variant="outline"
									size="sm"
									aria-label={`Open ${server.name} documentation`}
									data-testid={`mcp-library-docs-${server.slug}`}
								>
									<a href={server.docs_url} target="_blank" rel="noreferrer">
										<BookIcon className="h-4 w-4" />
									</a>
								</Button>
							</TooltipTrigger>
							<TooltipContent>Documentation</TooltipContent>
						</Tooltip>
					)}
					{isInstalled ? (
						<Button asChild size="sm" data-testid={`mcp-library-open-${server.slug}`}>
							<Link to="/workspace/mcp-registry">Open</Link>
						</Button>
					) : (
						<Button
							size="sm"
							onClick={() => onInstall(server)}
							disabled={!canCreateMCPClient}
							data-testid={`mcp-library-install-${server.slug}`}
						>
							Install
						</Button>
					)}
				</div>
			</CardFooter>

			<MCPLibraryDeleteDialog
				server={server}
				open={confirmOpen}
				isDeleting={isDeleting}
				onOpenChange={(open) => !open && setConfirmOpen(false)}
				onConfirm={handleDelete}
				confirmTestId={`mcp-library-delete-confirm-${server.slug}`}
			/>
		</Card>
	);
}

/** Skeleton placeholder mirroring the card layout while the library catalog loads. */
export function MCPLibraryServerCardSkeleton() {
	return (
		<Card className="h-full gap-0 overflow-hidden py-0 shadow-none" data-testid="mcp-library-card-skeleton">
			<CardHeader className="bg-muted/20 border-b px-4 py-4">
				<div className="flex min-w-0 items-start gap-3">
					<Skeleton className="h-12 w-12 shrink-0 rounded-md" />
					<div className="min-w-0 flex-1 space-y-2">
						<Skeleton className="h-4 w-3/4" />
						<div className="flex items-center gap-1.5">
							<Skeleton className="h-5 w-16 rounded-full" />
							<Skeleton className="h-4 w-20" />
						</div>
					</div>
				</div>
			</CardHeader>
			<CardContent className="flex flex-1 flex-col gap-3 px-4 py-3">
				<div className="min-h-10 space-y-2">
					<Skeleton className="h-4 w-full" />
					<Skeleton className="h-4 w-5/6" />
				</div>
				<div className="flex flex-wrap items-center gap-1.5">
					<Skeleton className="h-5 w-14 rounded-full" />
					<Skeleton className="h-5 w-16 rounded-full" />
					<Skeleton className="h-5 w-12 rounded-full" />
				</div>
			</CardContent>
			<CardFooter className="bg-muted/10 mt-auto justify-between gap-3 border-t px-4 py-3 !pt-3">
				<Skeleton className="h-4 w-24" />
				<Skeleton className="h-8 w-20 rounded-md" />
			</CardFooter>
		</Card>
	);
}