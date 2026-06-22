import { Button } from "@/components/ui/button";
import { MultiSelect, type MultiSelectOption } from "@/components/ui/multiSelect";
import { SearchSelect } from "@/components/ui/searchSelect";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useDebouncedValue } from "@/hooks/useDebounce";
import { useGetCoreConfigQuery, useGetMCPClientsQuery, useGetVirtualKeysQuery } from "@/lib/store";
import { cn } from "@/lib/utils";
import { Check, Globe2, KeyRound, Server, SquareTerminal } from "lucide-react";
import { parseAsArrayOf, parseAsBoolean, parseAsString, parseAsStringLiteral, useQueryStates } from "nuqs";
import { useEffect, useMemo, useState } from "react";
import { HARNESSES } from "./harnesses";
import { PlatformSelect } from "./platformSelect";
import type { HarnessID, HarnessPlatform, ServerScope, VirtualKeyOption } from "./types";
import { isClientAllowedForVirtualKey, maskSecret } from "./utils";

// Literal value sets driving the URL-persisted enums.
const HARNESS_IDS = HARNESSES.map((h) => h.id);
const HARNESS_PLATFORMS: HarnessPlatform[] = ["macos", "windows", "linux"];
const SERVER_SCOPES: ServerScope[] = ["all", "selected"];

export function MCPUsageGuideSheet() {
	// ── URL-persisted settings (survive refresh) ─────────────────────────
	// All user-facing selections live in query params so the install wizard
	// can be reconstructed exactly after a reload or shared via the URL.
	const [urlState, setUrlState] = useQueryStates(
		{
			install: parseAsBoolean.withDefault(false),
			harness: parseAsStringLiteral(HARNESS_IDS).withDefault("claude-code"),
			platform: parseAsStringLiteral(HARNESS_PLATFORMS).withDefault("macos"),
			scope: parseAsStringLiteral(SERVER_SCOPES).withDefault("all"),
			vk: parseAsString,
			servers: parseAsArrayOf(parseAsString).withDefault([]),
		},
		// Use replace (default) so tab/scope clicks don't spam browser history.
		{ history: "replace" },
	);
	const open = urlState.install;
	const setOpen = (value: boolean) => setUrlState({ install: value });
	const harness = urlState.harness;
	const platform = urlState.platform;
	const serverScope = urlState.scope;

	// ── Local state ──────────────────────────────────────────────────────
	const [virtualKeySearch, setVirtualKeySearch] = useState("");
	const [selectedVirtualKey, setSelectedVirtualKey] = useState<VirtualKeyOption["virtualKey"] | undefined>();
	const [virtualKeySelectOpen, setVirtualKeySelectOpen] = useState(false);
	const debouncedVirtualKeySearch = useDebouncedValue(virtualKeySearch, 250);

	// ── Queries ──────────────────────────────────────────────────────────
	const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true }, { skip: !open });
	const { data: virtualKeysData, isFetching: isFetchingVirtualKeys } = useGetVirtualKeysQuery(
		{ limit: 50, search: debouncedVirtualKeySearch || undefined },
		{ skip: !open },
	);
	const { data: mcpClientsData, isFetching: isFetchingMCPClients } = useGetMCPClientsQuery(
		{ limit: 50 },
		{ skip: !open || !selectedVirtualKey, refetchOnMountOrArgChange: true },
	);

	// ── Derived data ─────────────────────────────────────────────────────
	const activeVirtualKeys = useMemo(() => virtualKeysData?.virtual_keys?.filter((vk) => vk.is_active) ?? [], [virtualKeysData]);

	const virtualKeyOptions = useMemo<VirtualKeyOption[]>(
		() => activeVirtualKeys.map((vk) => ({ value: vk.id, label: vk.name, virtualKey: vk })),
		[activeVirtualKeys],
	);

	const allowedMCPClients = useMemo(() => {
		if (!selectedVirtualKey) return [];
		return (mcpClientsData?.clients ?? []).filter((client) => isClientAllowedForVirtualKey(client, selectedVirtualKey));
	}, [mcpClientsData, selectedVirtualKey]);

	const serverOptions = useMemo<MultiSelectOption[]>(
		() =>
			allowedMCPClients.map((client) => ({
				value: client.config.client_id,
				label: client.config.name,
			})),
		[allowedMCPClients],
	);

	const selectedServers = useMemo(
		() => allowedMCPClients.filter((client) => urlState.servers.includes(client.config.client_id)),
		[allowedMCPClients, urlState.servers],
	);

	const canGenerateCommand = !!selectedVirtualKey && (serverScope === "all" || selectedServers.length > 0);

	// ── Effects ──────────────────────────────────────────────────────────
	// Resolve the persisted virtual-key id into its full object, otherwise
	// fall back to auto-selecting the first active key. Auto-selection is kept
	// transient (not written to the URL) so it re-resolves on every open.
	useEffect(() => {
		if (!open) return;
		if (selectedVirtualKey) {
			const refreshed = activeVirtualKeys.find((vk) => vk.id === selectedVirtualKey.id);
			if (refreshed && refreshed !== selectedVirtualKey) setSelectedVirtualKey(refreshed);
			return;
		}
		if (urlState.vk) {
			const match = activeVirtualKeys.find((vk) => vk.id === urlState.vk);
			if (match) setSelectedVirtualKey(match);
			return;
		}
		if (!debouncedVirtualKeySearch && activeVirtualKeys[0]) {
			setSelectedVirtualKey(activeVirtualKeys[0]);
		}
	}, [activeVirtualKeys, debouncedVirtualKeySearch, open, selectedVirtualKey, urlState.vk]);

	// Prune persisted server ids that are no longer allowed for the key. Guarded
	// on loaded client data so we never wipe persisted ids before they resolve.
	useEffect(() => {
		if (!mcpClientsData) return;
		setUrlState((prev) => {
			const filtered = prev.servers.filter((id) => allowedMCPClients.some((client) => client.config.client_id === id));
			return filtered.length === prev.servers.length ? {} : { servers: filtered };
		});
	}, [mcpClientsData, allowedMCPClients, setUrlState]);

	// ── Resolve the active harness definition ────────────────────────────
	const activeHarness = HARNESSES.find((h) => h.id === harness) ?? HARNESSES[0];

	// ── Render ───────────────────────────────────────────────────────────
	return (
		<>
			<Button type="button"
				onClick={() => setOpen(true)}
				data-testid="mcp-usage-guide-trigger"
				variant="outline"
				className="h-8"
			>
				<SquareTerminal />
				<span className="hidden sm:inline">Connect agent</span>
			</Button>

			<Sheet open={open} onOpenChange={setOpen}>
				<SheetContent className="flex w-full flex-col overflow-y-auto p-0 pt-4 sm:max-w-2xl">
					<SheetHeader className="flex flex-col items-start px-0 py-4" headerClassName="mb-0 sticky px-8 -top-4 bg-card z-10">
						<div className="flex items-center gap-2">
							<div>
								<SheetTitle>Install Bifrost MCP</SheetTitle>
								<SheetDescription>Build a copy-ready command or config for your agent harness.</SheetDescription>
							</div>
						</div>
					</SheetHeader>

					<div className="flex flex-col gap-6 px-8 py-4">
						{/* ── Harness selector tabs ───────────────────────── */}
						<section className="flex flex-col gap-2 transition-[border-color,background-color] duration-150 ease-out">
							<div className="flex items-center gap-2 text-sm font-medium">
								<span>Harness</span>
							</div>
							<Tabs value={harness} onValueChange={(value) => setUrlState({ harness: value as HarnessID })}>
								<TabsList className="no-scrollbar flex w-full flex-row justify-start overflow-x-auto rounded-sm">
									{HARNESSES.map((h) => (
										<TabsTrigger key={h.id} value={h.id} className="flex flex-none shrink-0 gap-2">
											<div className="flex items-center gap-2">
												{h.icon}
												{h.label}
											</div>
										</TabsTrigger>
									))}
								</TabsList>
							</Tabs>
						</section>

						{/* ── Virtual key picker ─────────────────────────── */}
						<section className="flex flex-col gap-2 transition-[border-color,background-color] duration-150 ease-out">
							<div className="flex items-center gap-2 text-sm font-medium">
								<span>Virtual key</span>
							</div>
							<SearchSelect<VirtualKeyOption>
								async
								open={virtualKeySelectOpen}
								onOpenChange={setVirtualKeySelectOpen}
								options={virtualKeyOptions}
								onSearchChange={setVirtualKeySearch}
								isSearching={isFetchingVirtualKeys}
								isLoading={isFetchingVirtualKeys && virtualKeyOptions.length === 0}
								onValueSelect={(option) => {
									setSelectedVirtualKey(option.virtualKey);
									// Switching keys clears server selection and resets the scope.
									setUrlState({ vk: option.virtualKey.id, servers: [], scope: "all" });
									setVirtualKeySelectOpen(false);
								}}
								label={
									<Button
										type="button"
										variant="outline"
										className="h-9 w-full justify-start bg-transparent"
										data-testid="mcp-usage-guide-vk-select"
									>
										<KeyRound className="text-muted-foreground size-4" />
										<span className="truncate">{selectedVirtualKey?.name ?? "Search virtual keys"}</span>
										{selectedVirtualKey && (
											<span className="text-muted-foreground ml-auto hidden font-mono text-xs sm:inline">
												{maskSecret(selectedVirtualKey.value)}
											</span>
										)}
									</Button>
								}
								entryView={(option) => (
									<div className="flex min-w-0 flex-1 items-center gap-2">
										<div className="flex min-w-0 flex-col">
											<span className="truncate font-medium">{option.label}</span>
											<span className="text-muted-foreground text-xs">{maskSecret(option.virtualKey.value)}</span>
										</div>
										{selectedVirtualKey?.id === option.virtualKey.id && <Check className="ml-auto size-4 text-green-600" />}
									</div>
								)}
								searchPlaceholder="Search virtual keys..."
								emptyMessage="No active virtual keys found."
								align="start"
								className="w-full"
								contentClassName="w-[var(--radix-popover-trigger-width)]"
							/>
						</section>

						{/* ── Server scope selector ──────────────────────── */}
						{selectedVirtualKey && (
							<section className="flex flex-col gap-2 transition-[opacity,transform] duration-200 ease-out motion-reduce:transition-none">
								<div className="flex items-center gap-2 text-sm font-medium">
									<span>Server access</span>
								</div>
								<div className="grid gap-2 sm:grid-cols-2">
									<button
										type="button"
										onClick={() => setUrlState({ scope: "all", servers: [] })}
										className={cn(
											"flex items-center gap-2 rounded-sm border px-3 py-2 h-9 text-left text-sm transition-[background-color,border-color,transform] duration-150 ease-out hover:bg-accent active:scale-[0.99]",
											serverScope === "all" && "border-primary bg-primary/5",
										)}
										data-testid="mcp-usage-guide-server-scope-all"
									>
										<Globe2 className="text-muted-foreground size-4" />
										<span className="font-medium">All servers</span>
										{serverScope === "all" && <Check className="ml-auto size-4 text-green-600" />}
									</button>
									<button
										type="button"
										onClick={() => setUrlState({ scope: "selected" })}
										className={cn(
											"flex items-center gap-2 rounded-sm border px-3 py-2 h-9 text-left text-sm transition-[background-color,border-color,transform] duration-150 ease-out hover:bg-accent active:scale-[0.99]",
											serverScope === "selected" && "border-primary bg-primary/5",
										)}
										data-testid="mcp-usage-guide-server-scope-selected"
									>
										<Server className="text-muted-foreground size-4" />
										<span className="font-medium">Selected servers</span>
										{serverScope === "selected" && <Check className="ml-auto size-4 text-green-600" />}
									</button>
								</div>

								{serverScope === "selected" && (
									<div className="transition-[opacity,transform] duration-200 ease-out motion-reduce:transition-none">
										<MultiSelect
											options={serverOptions}
											defaultValue={urlState.servers}
											resetOnDefaultValueChange
											onValueChange={(ids) => setUrlState({ servers: ids })}
											placeholder={isFetchingMCPClients ? "Loading allowed servers..." : "Select allowed MCP servers"}
											emptyIndicator="No allowed MCP servers found."
											maxCount={3}
											className="border-input text-foreground hover:bg-accent hover:text-accent-foreground h-8 rounded-sm bg-transparent font-normal"
											popoverClassName="w-[var(--radix-popover-trigger-width)]"
											data-testid="mcp-usage-guide-server-select"
										/>
									</div>
								)}
							</section>
						)}

						{/* ── Platform selector ───────────────────────────── */}
						{selectedVirtualKey && activeHarness.usesPlatform && (
							<section className="flex flex-col gap-2 transition-[opacity,transform] duration-200 ease-out motion-reduce:transition-none">
								<div className="flex items-center gap-2 text-sm font-medium">
									<span>Platform</span>
								</div>
								<PlatformSelect platform={platform} onPlatformChange={(value) => setUrlState({ platform: value })} />
							</section>
						)}

						{/* ── Active harness install panel ────────────────── */}
						<activeHarness.Install
							canGenerateCommand={canGenerateCommand}
							clientConfig={bifrostConfig?.client_config}
							platform={platform}
							selectedServers={selectedServers}
							serverScope={serverScope}
							virtualKey={selectedVirtualKey}
						/>
					</div>
				</SheetContent>
			</Sheet>
		</>
	);
}
