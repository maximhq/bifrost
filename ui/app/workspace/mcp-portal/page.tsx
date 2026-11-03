import { VirtualKeySelector } from "@/components/entitySelectors/virtualKeySelector";
import PageTitle from "@/components/pageTitle";
import { Button } from "@/components/ui/button";
import { getErrorMessage, useGetMCPPortalQuery } from "@/lib/store";
import { KeyRound, Loader2, RefreshCw, Server, UserRound } from "lucide-react";
import { parseAsString, useQueryState } from "nuqs";
import ServerCard from "./views/serverCard";

export default function MCPPortalPage() {
	const [virtualKeyId, setVirtualKeyId] = useQueryState("virtual_key_id", parseAsString.withDefault("").withOptions({ history: "push" }));
	const { currentData, isFetching, isError, error, refetch } = useGetMCPPortalQuery(virtualKeyId ? { virtual_key_id: virtualKeyId } : {}, {
		refetchOnFocus: true,
		refetchOnMountOrArgChange: true,
	});
	// currentData is scoped to the active query argument, unlike data, which
	// may retain the previous identity's response while the next query loads.
	const identity = !isError ? currentData?.identity : undefined;

	return (
		<div className="mx-auto flex w-full max-w-7xl flex-col gap-6" data-testid="mcp-portal-page">
			<PageTitle breadcrumbs={[{ label: "MCP Servers", to: "/workspace/mcp-registry" }, { label: "MCP Portal" }]}>
				View the MCP servers you can access and configure your personal credentials.
			</PageTitle>
			<div className="flex flex-wrap items-start justify-between gap-4">
				<div>
					<h2 className="text-lg font-semibold">Your MCP servers</h2>
					<p className="text-muted-foreground mt-1 text-sm">
						Choose an identity to see its accessible servers. Credentials are never displayed here.
					</p>
				</div>
				<Button variant="outline" onClick={() => void refetch()} disabled={isFetching} data-testid="mcp-portal-refresh">
					<RefreshCw className={`size-4 ${isFetching ? "animate-spin" : ""}`} />
					Refresh
				</Button>
			</div>
			<section className="bg-muted/30 flex flex-wrap items-center gap-3 rounded-lg border p-4" aria-label="Portal identity">
				<span className="text-sm font-medium">View as</span>
				<Button
					variant={virtualKeyId ? "outline" : "secondary"}
					onClick={() => void setVirtualKeyId(null)}
					disabled={!virtualKeyId}
					data-testid="mcp-portal-current-user"
				>
					<UserRound className="size-4" />
					Current user
				</Button>
				<span className="text-muted-foreground text-xs">or</span>
				<div className="w-full sm:w-72" data-testid="mcp-portal-virtual-key-selector">
					<VirtualKeySelector
						value={virtualKeyId}
						onChange={(value) => void setVirtualKeyId(value || null)}
						placeholder="Select a virtual key"
						triggerIcon={<KeyRound className="size-4" />}
						fallbackOption={
							identity?.mode === "vk" && identity.id === virtualKeyId ? { value: identity.id, label: identity.name || identity.id } : null
						}
					/>
				</div>
				{identity && (
					<p className="text-muted-foreground w-full text-xs" data-testid="mcp-portal-identity">
						Showing access for <span className="text-foreground font-medium">{identity.name || identity.id}</span> (
						{identity.mode === "vk" ? "virtual key" : "current user"}).
					</p>
				)}
			</section>
			{isError ? (
				<div className="border-destructive/40 bg-destructive/5 rounded-lg border p-6" role="alert" data-testid="mcp-portal-error">
					<h3 className="text-sm font-semibold">Unable to load accessible servers</h3>
					<p className="text-muted-foreground mt-2 text-sm">{getErrorMessage(error)}</p>
					<Button variant="outline" className="mt-4" onClick={() => void refetch()} disabled={isFetching} data-testid="mcp-portal-retry">
						Try again
					</Button>
				</div>
			) : !currentData ? (
				<div
					className="text-muted-foreground flex min-h-52 items-center justify-center gap-2 rounded-lg border text-sm"
					role="status"
					data-testid="mcp-portal-loading"
				>
					<Loader2 className="size-5 animate-spin" /> Loading accessible servers…
				</div>
			) : !currentData.identity ? (
				<div
					className="flex min-h-60 flex-col items-center justify-center rounded-lg border p-8 text-center"
					data-testid="mcp-portal-no-identity"
				>
					<KeyRound className="text-muted-foreground mb-4 size-8" />
					<h3 className="font-semibold">Select a virtual key to get started</h3>
					<p className="text-muted-foreground mt-2 max-w-md text-sm">
						There is no signed-in user identity available for this session. Select a virtual key above to view its MCP servers and configure
						credentials.
					</p>
				</div>
			) : currentData.servers.length === 0 ? (
				<div
					className="flex min-h-60 flex-col items-center justify-center rounded-lg border p-8 text-center"
					data-testid="mcp-portal-empty"
				>
					<Server className="text-muted-foreground mb-4 size-8" />
					<h3 className="font-semibold">No accessible MCP servers</h3>
					<p className="text-muted-foreground mt-2 max-w-md text-sm">
						This identity does not have access to any MCP servers. Try another identity or ask your administrator to grant access.
					</p>
				</div>
			) : (
				<section aria-label="Accessible MCP servers" aria-busy={isFetching}>
					<p className="text-muted-foreground mb-3 text-sm" data-testid="mcp-portal-count">
						{currentData.count} accessible {currentData.count === 1 ? "server" : "servers"}
					</p>
					<div className="grid items-stretch gap-4 md:grid-cols-2 xl:grid-cols-3">
						{currentData.servers.map((server) => (
							<ServerCard
								key={`${virtualKeyId ? `vk:${virtualKeyId}` : "user"}:${server.id}`}
								server={server}
								virtualKeyId={virtualKeyId}
								disabled={isFetching}
							/>
						))}
					</div>
				</section>
			)}
		</div>
	);
}