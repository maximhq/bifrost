import * as DialogPrimitive from "@radix-ui/react-dialog";
import { useLocation, useRouter } from "@tanstack/react-router";
import { Command } from "cmdk";
import { ArrowUpRight, Check, ChevronRight, CornerDownLeft, Link2, Search } from "lucide-react";
import { useCallback, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { preserveTimeFilters } from "@/components/navigation/navItem";
import { useNavItems } from "@/components/navigation/navItems";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { WarpIcon } from "@/components/ui/icons";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useWarp } from "@/lib/contexts/warpContext";
import { cn } from "@/lib/utils";

import {
	findCurrentCommandId,
	flattenNavItems,
	flattenPaletteActions,
	modKeyLabel,
	type PaletteAction,
	type PaletteNavCommand,
	type PalettePage,
	searchCommands,
} from "./commandPalette.utils";
import { usePaletteActions } from "./commandPaletteActions";

// One rendered row, whatever it does: open a page, run an action, open a nested page, or ask Warp.
interface PaletteRow {
	id: string;
	title: string;
	detail?: string;
	icon?: PaletteNavCommand["icon"];
	trailing: "enter" | "external" | "page" | "checked";
	isCurrent?: boolean;
	command?: PaletteNavCommand;
	action?: PaletteAction;
}

interface CommandGroupView {
	id: string;
	heading: string;
	rows: PaletteRow[];
}

const WARP_ROW_ID = "warp:ask";

const navRow = (command: PaletteNavCommand, showSection: boolean, currentId?: string): PaletteRow => ({
	id: command.id,
	title: command.title,
	detail: showSection ? command.section : undefined,
	icon: command.icon,
	trailing: command.isExternal ? "external" : "enter",
	isCurrent: command.id === currentId,
	command,
});

const actionRow = (action: PaletteAction, showSection: boolean): PaletteRow => ({
	id: action.id,
	title: action.title,
	detail: showSection ? action.section : undefined,
	icon: action.icon,
	trailing: action.page ? "page" : action.checked ? "checked" : "enter",
	action,
});

export default function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
	const { t } = useTranslation("shell");
	const router = useRouter();
	const pathname = useLocation({ select: (l) => l.pathname });
	const search = useLocation({ select: (l) => l.searchStr ?? "" });
	const navItems = useNavItems();
	const warp = useWarp();
	// The hook catches the TypeError of a missing navigator.clipboard (plain HTTP hosts) and toasts the failure.
	const { copy } = useCopyToClipboard({
		successMessage: t("commandPalette.toast.linkCopied"),
		errorMessage: t("commandPalette.toast.linkCopyFailed"),
	});
	const [query, setQuery] = useState("");
	const [selected, setSelected] = useState("");
	const [pageStack, setPageStack] = useState<string[]>([]);
	const [wasOpen, setWasOpen] = useState(open);
	// Set when a row hands focus to something else (a sheet, the Warp dock), so closing does not pull it back.
	const skipFocusRestore = useRef(false);

	// Reset while rendering the opening frame, not in an effect after it, so the first frame
	// already shows an empty query instead of painting the last one and re-rendering.
	if (open !== wasOpen) {
		setWasOpen(open);
		if (open) {
			setQuery("");
			setSelected("");
			setPageStack([]);
			skipFocusRestore.current = false;
		}
	}

	const close = useCallback(() => onOpenChange(false), [onOpenChange]);
	const commands = useMemo(() => flattenNavItems(navItems, IS_ENTERPRISE), [navItems]);
	const currentId = useMemo(() => findCurrentCommandId(commands, pathname, search), [commands, pathname, search]);
	const { actions, thisPage: pageCommands } = usePaletteActions({ close, commands, currentId });
	const thisPage = useMemo<PaletteAction[]>(
		() => [
			...pageCommands,
			{
				id: "page-link:copy",
				title: t("commandPalette.actions.copyLink"),
				icon: Link2,
				keywords: ["url", "share", "copy"],
				run: () => {
					close();
					void copy(window.location.href);
				},
			},
		],
		[pageCommands, close, copy, t],
	);
	// Looked up by id on every render, so an open nested page shows fresh state such as the checked theme.
	const pagesById = useMemo(() => {
		const map = new Map<string, PalettePage>();
		for (const a of actions) if (a.page) map.set(a.page.id, a.page);
		return map;
	}, [actions]);
	const page = pageStack.length ? pagesById.get(pageStack[pageStack.length - 1]) : undefined;

	const goTo = (stack: string[]) => {
		setPageStack(stack);
		setQuery("");
		setSelected("");
	};

	const trimmed = query.trim();
	const groups = useMemo<CommandGroupView[]>(() => {
		if (page) {
			const rows = (trimmed ? searchCommands(page.actions, trimmed) : page.actions).map((a) => actionRow(a, false));
			return rows.length ? [{ id: `page:${page.id}`, heading: page.title, rows }] : [];
		}
		const out: CommandGroupView[] = [];
		if (trimmed) {
			// Pages and actions rank together, so an exact "dark" beats a page that only matches by letters.
			const results = searchCommands<PaletteNavCommand | PaletteAction>(
				// The current page's commands also sit in the Create page; list each id once.
				[...thisPage, ...commands, ...flattenPaletteActions(actions).filter((a) => !thisPage.some((p) => p.id === a.id))],
				trimmed,
			);
			// No group at all on a miss, or its heading renders above the empty state.
			if (results.length)
				out.push({
					id: "results",
					heading: t("commandPalette.groups.results"),
					rows: results.map((r) => ("href" in r ? navRow(r, true, currentId) : actionRow(r, true))),
				});
		} else {
			// On top only when the page brought commands of its own; copy link alone would take the default selection.
			const pageGroup = pageCommands.length > 0;
			if (pageGroup)
				out.push({ id: "this-page", heading: t("commandPalette.groups.thisPage"), rows: thisPage.map((a) => actionRow(a, false)) });
			const sections = new Map<string, CommandGroupView>();
			for (const c of commands) {
				const key = c.section ?? "";
				if (!sections.has(key))
					sections.set(key, { id: `section:${key}`, heading: c.section ?? t("commandPalette.groups.general"), rows: [] });
				sections.get(key)!.rows.push(navRow(c, false, currentId));
			}
			out.push(...sections.values());
			const rootActions = pageGroup ? actions : [...actions, ...thisPage];
			out.push({ id: "actions", heading: t("commandPalette.groups.actions"), rows: rootActions.map((a) => actionRow(a, false)) });
		}
		// Offered on every query, not only a miss: a question can still match a page by a stray word.
		if (trimmed && warp)
			out.push({
				id: "warp",
				heading: t("nav.warp"),
				rows: [{ id: WARP_ROW_ID, title: t("warp.ask"), detail: trimmed, icon: WarpIcon, trailing: "enter" }],
			});
		return out;
	}, [page, trimmed, thisPage, pageCommands, commands, actions, currentId, warp, t]);

	const rowByValue = useMemo(() => {
		const map = new Map<string, PaletteRow>();
		for (const g of groups) for (const r of g.rows) map.set(r.id, r);
		return map;
	}, [groups]);
	// Filtering is ours (cmdk's scorer ranks breadcrumb hits as high as title hits), so cmdk never
	// re-selects on its own: a selection that left the result list falls back to the top result.
	const activeValue = rowByValue.has(selected) ? selected : (rowByValue.keys().next().value ?? "");

	const navigate = useCallback(
		(command: PaletteNavCommand, inNewTab = false) => {
			if (inNewTab || command.isExternal) {
				window.open(command.href, "_blank", "noopener,noreferrer");
				if (command.isExternal) onOpenChange(false);
				return;
			}
			onOpenChange(false);
			router.history.push(preserveTimeFilters(command.href, command.href.split("?")[0], pathname, search));
		},
		[onOpenChange, router, pathname, search],
	);

	const select = (row: PaletteRow) => {
		if (row.command) return navigate(row.command);
		if (row.action?.page) return goTo([...pageStack, row.action.page.id]);
		if (row.action?.run) {
			skipFocusRestore.current = true;
			return row.action.run();
		}
		if (row.id === WARP_ROW_ID && warp) {
			skipFocusRestore.current = true;
			onOpenChange(false);
			warp.ask(trimmed);
		}
	};

	const onKeyDown = (event: React.KeyboardEvent) => {
		if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
			const command = rowByValue.get(activeValue)?.command;
			if (!command) return;
			event.preventDefault();
			navigate(command, true);
			return;
		}
		// Backspace on an empty query steps back out of a nested page.
		if (event.key === "Backspace" && !query && pageStack.length) {
			event.preventDefault();
			goTo(pageStack.slice(0, -1));
		}
	};

	const mod = modKeyLabel();

	return (
		<DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
			<DialogPrimitive.Portal>
				<DialogPrimitive.Overlay
					data-testid="command-palette-overlay"
					className="data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 fixed inset-0 z-50 bg-black/25 duration-100 dark:bg-black/50"
				/>
				<DialogPrimitive.Content
					data-testid="command-palette"
					aria-describedby={undefined}
					onCloseAutoFocus={(e) => {
						if (skipFocusRestore.current) e.preventDefault();
					}}
					onEscapeKeyDown={(e) => {
						// Escape clears the query, then steps out of a nested page, then closes.
						if (query) {
							e.preventDefault();
							setQuery("");
						} else if (pageStack.length) {
							e.preventDefault();
							goTo(pageStack.slice(0, -1));
						}
					}}
					className={cn(
						"dark:bg-card fixed top-[min(20vh,160px)] left-1/2 z-50 w-[calc(100%-2rem)] max-w-[640px] -translate-x-1/2 overflow-hidden rounded-xl border border-gray-200 bg-white shadow-[0_24px_64px_-12px_rgba(0,0,0,0.25)] outline-none dark:border-zinc-800",
						"data-[state=open]:animate-in data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0 data-[state=closed]:zoom-out-[0.98] data-[state=open]:zoom-in-[0.98] duration-100 ease-out",
					)}
				>
					<DialogPrimitive.Title className="sr-only">{t("commandPalette.title")}</DialogPrimitive.Title>
					<Command
						label={t("commandPalette.title")}
						shouldFilter={false}
						loop
						value={activeValue}
						onValueChange={setSelected}
						onKeyDown={onKeyDown}
					>
						<div className="flex h-14 items-center gap-3 border-b border-gray-100 px-4 dark:border-zinc-800">
							<Search className="text-muted-foreground size-4 shrink-0" />
							{page && (
								<span
									data-testid="command-palette-page-chip"
									className="text-muted-foreground shrink-0 rounded-sm border border-gray-200 bg-gray-50 px-1.5 py-0.5 text-xs dark:border-zinc-700 dark:bg-zinc-900"
								>
									{page.title}
								</span>
							)}
							<Command.Input
								data-testid="command-palette-input"
								value={query}
								onValueChange={(q) => {
									setQuery(q);
									setSelected("");
								}}
								placeholder={page?.placeholder ?? t("commandPalette.placeholder")}
								className="placeholder:text-muted-foreground text-foreground h-full flex-1 bg-transparent text-[15px] outline-none"
							/>
							{/* i18n-ignore */}
							<Kbd>esc</Kbd>
						</div>
						<Command.List
							data-testid="command-palette-list"
							// The cap lives inside the height so the transition runs between the heights actually shown.
							className="custom-scrollbar h-[min(var(--cmdk-list-height),440px,55vh)] scroll-py-2 overflow-y-auto overscroll-contain px-2 transition-[height] duration-200 ease-[cubic-bezier(0.2,0,0,1)] motion-reduce:transition-none"
						>
							<div className="py-2">
								<Command.Empty className="text-muted-foreground py-10 text-center text-sm">
									{t("commandPalette.empty", { query: trimmed })}
								</Command.Empty>
								{groups.map((group) => (
									<Command.Group
										key={group.id}
										heading={group.heading}
										className="[&_[cmdk-group-heading]]:text-muted-foreground [&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:pt-1 [&_[cmdk-group-heading]]:pb-1.5 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-medium [&:not(:first-child)]:mt-2"
									>
										{group.rows.map((row) => (
											<PaletteItem key={row.id} row={row} currentLabel={t("commandPalette.current")} onSelect={() => select(row)} />
										))}
									</Command.Group>
								))}
							</div>
						</Command.List>
						<div className="text-muted-foreground flex h-10 items-center gap-4 border-t border-gray-100 px-4 text-[11px] dark:border-zinc-800">
							<span className="flex items-center gap-1.5">
								<Kbd>↑</Kbd>
								<Kbd>↓</Kbd>
								{t("commandPalette.footer.navigate")}
							</span>
							<span className="flex items-center gap-1.5">
								<Kbd>↵</Kbd>
								{t("commandPalette.footer.open")}
							</span>
							{page ? (
								<span className="flex items-center gap-1.5">
									<Kbd>⌫</Kbd>
									{t("commandPalette.footer.back")}
								</span>
							) : (
								<span className="flex items-center gap-1.5">
									<Kbd>{mod}</Kbd>
									<Kbd>↵</Kbd>
									{t("commandPalette.footer.newTab")}
								</span>
							)}
						</div>
					</Command>
				</DialogPrimitive.Content>
			</DialogPrimitive.Portal>
		</DialogPrimitive.Root>
	);
}

function PaletteItem({ row, currentLabel, onSelect }: { row: PaletteRow; currentLabel: string; onSelect: () => void }) {
	const Icon = row.icon;
	return (
		<Command.Item
			value={row.id}
			onSelect={onSelect}
			data-testid={`command-palette-item-${row.id}`}
			className="group text-foreground/85 data-[selected=true]:text-foreground relative flex h-10 cursor-pointer items-center gap-3 rounded-lg px-2.5 text-sm transition-colors duration-75 select-none data-[selected=true]:bg-gray-100 dark:data-[selected=true]:bg-zinc-800/80"
		>
			{Icon ? (
				<Icon className="text-muted-foreground group-data-[selected=true]:text-foreground size-4 shrink-0 transition-colors" />
			) : (
				<span className="size-4 shrink-0" />
			)}
			<span className="min-w-0 truncate">{row.title}</span>
			{row.detail && <span className="text-muted-foreground min-w-0 shrink truncate text-xs">{row.detail}</span>}
			<span className="ml-auto flex shrink-0 items-center gap-2">
				{row.isCurrent && (
					<span className="text-muted-foreground rounded-sm border border-gray-200 px-1.5 py-px text-[10px] leading-4 dark:border-zinc-700">
						{currentLabel}
					</span>
				)}
				{row.trailing === "external" && <ArrowUpRight className="text-muted-foreground size-3.5" />}
				{row.trailing === "page" && <ChevronRight className="text-muted-foreground size-3.5" />}
				{row.trailing === "checked" && <Check className="text-muted-foreground size-3.5" />}
				{row.trailing === "enter" && (
					<CornerDownLeft className="text-muted-foreground size-3.5 opacity-0 transition-opacity duration-75 group-data-[selected=true]:opacity-100" />
				)}
			</span>
		</Command.Item>
	);
}

function Kbd({ children }: { children: React.ReactNode }) {
	return (
		<kbd className="text-muted-foreground inline-flex h-5 min-w-5 items-center justify-center rounded border border-gray-200 bg-gray-50 px-1 font-sans text-[10px] font-medium dark:border-zinc-700 dark:bg-zinc-900">
			{children}
		</kbd>
	);
}