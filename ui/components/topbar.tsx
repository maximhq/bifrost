import { ThemeToggle } from "@/components/themeToggle";
import NotificationCenter from "@/components/notificationCenter";
import { deriveTitleFromPathname, TOPBAR_MENU_SIDE_OFFSET } from "@/components/topbar.utils";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdownMenu";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { IS_ENTERPRISE } from "@/lib/constants/config";
import { useDescriptionSlotRef, useMobileFilterSlotRef, useTopbarTitle } from "@/lib/contexts/topbarContext";
import { useBranding } from "@/lib/hooks/useBranding";
import { useGetCoreConfigQuery, useLogoutMutation } from "@/lib/store";
import type { UserInfo } from "@enterprise/lib/store/utils/tokenManager";
import { getUserInfo } from "@enterprise/lib/store/utils/tokenManager";
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from "@phosphor-icons/react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { BugIcon, ChevronDown, LogOut, Menu, User } from "lucide-react";
import { useTheme } from "next-themes";
import { useEffect, useMemo, useState } from "react";

// External links that used to live in the sidebar footer icon row. They now
// render as labelled rows inside the topbar menu, which is both more legible
// than a row of bare glyphs and frees the sidebar footer for the promo card.
const externalLinks: {
	title: string;
	url: string;
	// Mixed lucide + phosphor icons, which disagree on their prop surface
	// (`weight` vs `strokeWidth`), so the slot is intentionally loose.
	icon: React.ComponentType<Record<string, unknown>>;
	strokeWidth?: number;
}[] = [
	{
		title: "Discord Server",
		url: "https://discord.gg/exN5KAydbU",
		icon: DiscordLogoIcon,
	},
	{
		title: "GitHub Repository",
		url: "https://github.com/maximhq/bifrost",
		icon: GithubLogoIcon,
	},
	{
		title: "Report a bug",
		url: "https://github.com/maximhq/bifrost/issues/new?title=[Bug Report]&labels=bug&type=bug&projects=maximhq/1",
		icon: BugIcon,
		strokeWidth: 1.5,
	},
	{
		title: "Full Documentation",
		url: "https://docs.getbifrost.ai",
		icon: BooksIcon,
		strokeWidth: 1,
	},
];

/**
 * Resolves the topbar title. A page can name itself via useSetTopbarTitle();
 * otherwise we derive it from the last non-empty path segment, e.g.
 * "/workspace/mcp-registry" -> "MCP Registry".
 */
function usePageTitle() {
	const pathname = useLocation({ select: (location) => location.pathname });
	const override = useTopbarTitle();
	const derived = useMemo(() => deriveTitleFromPathname(pathname), [pathname]);
	return override ?? derived;
}

/**
 * Topbar renders the strip above the inset content card. It deliberately
 * carries no background of its own so it reads as the same surface as the
 * sidebar — both simply show the page body background, since <Sidebar> is
 * rendered with `bg-transparent border-none`.
 *
 * It is 52px tall (h-13) with pt-1, putting its centred row at y=28 — the same
 * axis as the sidebar logo, which SidebarHeader's `mt-1 + p-2` around an h-8 row
 * centres at 28. 52px is also where the sidebar's search input starts, so the
 * content card below lines up with it. Keep this in sync with
 * --app-topbar-height in globals.css — every full-height page measures itself
 * against that variable.
 */
export default function Topbar() {
	const title = usePageTitle();
	const setDescriptionSlot = useDescriptionSlotRef();
	const setMobileFilterSlot = useMobileFilterSlotRef();
	const navigate = useNavigate();
	const [logout] = useLogoutMutation();
	const { data: coreConfig } = useGetCoreConfigQuery({});
	const { resolvedTheme } = useTheme();
	const { logoSrc, logoAlt } = useBranding(resolvedTheme === "dark");

	// Enterprise SCIM/OAuth stashes the profile in localStorage. Read it after
	// mount so SSR/first paint doesn't diverge from the hydrated tree.
	const [userInfo, setUserInfo] = useState<UserInfo | null>(null);
	useEffect(() => {
		if (IS_ENTERPRISE) {
			setUserInfo(getUserInfo());
		}
	}, []);

	const isAuthEnabled = coreConfig?.auth_config?.is_enabled || false;
	const showUserPill = IS_ENTERPRISE && !!userInfo;
	const canLogout = showUserPill || isAuthEnabled;

	const displayName = userInfo?.name || userInfo?.email || userInfo?.preferred_username || "User";
	// Only show the email as a second line when it isn't already the headline.
	const secondaryLine = userInfo?.email && userInfo.email !== displayName ? userInfo.email : undefined;

	const handleLogout = async () => {
		try {
			await logout().unwrap();
		} finally {
			// Redirect regardless — a failed server-side logout still means the
			// user intended to end the session locally.
			navigate({ to: "/login" });
		}
	};

	return (
		<header className="flex h-13 w-full shrink-0 items-center gap-2 px-3 pt-1 md:pr-4 md:pl-2" data-testid="topbar-container-root">
			<div className="flex min-w-0 flex-1 items-center gap-2">
				<SidebarTrigger className="shrink-0 md:hidden" />
				<img className="h-[22px] w-auto max-w-[120px] object-contain md:hidden" src={logoSrc} alt={logoAlt} width={70} height={70} />
				{/* text-lg font-semibold is the existing in-page <h1> scale, so hoisting
				    the title here doesn't visually demote it. */}
				<h1 className="hidden truncate text-lg font-semibold md:block">{title}</h1>
				{/* Anchor for <PageTitle>'s description popover. Pages portal into
				    this node, so the topbar never has to know their content. */}
				<span ref={setDescriptionSlot} className="hidden shrink-0 items-center md:flex" />
			</div>

			{/* Theme stays a first-class topbar control rather than a menu entry —
			    it's a display preference, not an account action. */}
			<span ref={setMobileFilterSlot} className="flex shrink-0 items-center md:hidden" />
			<NotificationCenter />
			<ThemeToggle />

			<DropdownMenu>
				<DropdownMenuTrigger asChild>
					{showUserPill ? (
						<button
							type="button"
							data-testid="topbar-user-pill"
							aria-label="Account menu"
							className="text-muted-foreground hover:bg-accent hover:text-accent-foreground md:border-border md:bg-card/60 md:text-foreground flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors md:h-8 md:w-auto md:max-w-[220px] md:min-w-0 md:gap-1.5 md:rounded-full md:border md:py-0 md:pr-2 md:pl-1"
						>
							<span className="bg-muted text-muted-foreground hidden size-6 shrink-0 items-center justify-center rounded-full md:flex">
								<User className="size-3.5" strokeWidth={2} />
							</span>
							<User className="size-4 md:hidden" strokeWidth={2} />
							{/* min-w-0 + truncate is what keeps a 200-character display
							    name from stretching the pill and pushing the page title
							    out of the topbar. */}
							<span className="hidden min-w-0 truncate text-sm font-medium md:block">{displayName}</span>
							<ChevronDown className="text-muted-foreground hidden size-3.5 shrink-0 md:block" strokeWidth={2} />
						</button>
					) : (
						<button
							type="button"
							data-testid="topbar-menu-btn"
							aria-label="Open menu"
							className="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md transition-colors"
						>
							<Menu className="size-4" strokeWidth={2} />
						</button>
					)}
				</DropdownMenuTrigger>

				<DropdownMenuContent align="end" sideOffset={TOPBAR_MENU_SIDE_OFFSET} className="w-60">
					{showUserPill && (
						<>
							<DropdownMenuLabel className="flex min-w-0 flex-col gap-0.5 py-2">
								<span className="truncate text-sm font-medium">{displayName}</span>
								{secondaryLine && <span className="text-muted-foreground truncate text-xs font-normal">{secondaryLine}</span>}
							</DropdownMenuLabel>
							<DropdownMenuSeparator />
						</>
					)}

					<DropdownMenuGroup>
						{externalLinks.map((item) => (
							<DropdownMenuItem key={item.title} asChild>
								<a href={item.url} target="_blank" rel="noopener noreferrer" className="cursor-pointer">
									<item.icon className="size-4" size={16} weight="regular" strokeWidth={item.strokeWidth} />
									<span className="truncate">{item.title}</span>
								</a>
							</DropdownMenuItem>
						))}
					</DropdownMenuGroup>

					{canLogout && (
						<>
							<DropdownMenuSeparator />
							<DropdownMenuItem onClick={handleLogout} data-testid="topbar-logout-btn" className="cursor-pointer">
								<LogOut className="size-4" strokeWidth={2} />
								<span>Sign out</span>
							</DropdownMenuItem>
						</>
					)}
				</DropdownMenuContent>
			</DropdownMenu>
		</header>
	);
}