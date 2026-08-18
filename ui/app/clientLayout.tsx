import FullPageLoader from "@/components/fullPageLoader";
import NotAvailableBanner from "@/components/notAvailableBanner";
import OnboardingWidget from "@/components/onboardingWidget";
import ProgressProvider from "@/components/progressBar";
import Sidebar from "@/components/sidebar";
import { ThemeProvider } from "@/components/themeProvider";
import Topbar from "@/components/topbar";
import TrialExpiryBanner from "@/components/trialExpiryBanner";
import { Button } from "@/components/ui/button";
import { SidebarProvider } from "@/components/ui/sidebar";
import { useStoreSync } from "@/hooks/useStoreSync";
import { useNotificationSync } from "@/hooks/useNotificationSync";
import { TopbarProvider } from "@/lib/contexts/topbarContext";
import { WebSocketProvider } from "@/hooks/useWebSocket";
import { getErrorMessage, ReduxProvider, useGetCoreConfigQuery, useIsAuthEnabledQuery } from "@/lib/store";
import { BifrostConfig } from "@/lib/types/config";
import { RbacProvider, useRbacContext } from "@enterprise/lib/contexts/rbacContext";
import { useLocation, useMatches } from "@tanstack/react-router";
import { RefreshCw, WifiOff } from "lucide-react";
import { NuqsAdapter } from "nuqs/adapters/tanstack-router";
import { lazy, Suspense, useEffect, useState } from "react";
import { CookiesProvider } from "react-cookie";
import { toast, Toaster } from "sonner";

// Lazy import — only loaded in development, completely excluded from prod bundle
const DevProfilerLazy = lazy(() =>
	import("@/components/devProfiler").then((mod) => ({
		default: mod.DevProfiler,
	})),
);
const DevProfiler = () => (
	<Suspense fallback={null}>
		<DevProfilerLazy />
	</Suspense>
);

function StoreSyncInitializer() {
	useStoreSync();
	useNotificationSync();
	return null;
}

function AppContent({ children }: { children: React.ReactNode }) {
	// Routes can declare `staticData: { tempTokenScoped: true }` to advertise that
	// they're reachable via a server-emitted, temp-token-bearing URL by visitors
	// without a dashboard session. The actual layout choice is made per-visitor:
	// an authenticated admin still sees the full dashboard chrome, while an
	// anonymous visitor arriving with `#t=<token>` gets a stripped MinimalShell.
	// The auth-via-temp-token half lives in <TempTokenScope>.
	const matches = useMatches();
	const tempTokenScoped = matches.some((m) => (m.staticData as { tempTokenScoped?: boolean } | undefined)?.tempTokenScoped === true);
	// publicShell: route declares it's a static, auth-free page that should
	// always render MinimalShell — no chrome, no auth probe, no API calls.
	// Used by the post-OAuth "authentication successful" landing, which has
	// neither a fragment nor a cookie to drive the tempTokenScoped per-visitor
	// logic.
	const publicShell = matches.some((m) => (m.staticData as { publicShell?: boolean } | undefined)?.publicShell === true);
	// Probe dashboard auth state on opted-in routes. is-auth-enabled is whitelisted
	// (no 401 risk) and returns whether the current cookie is a valid session.
	const { data: authState, isLoading: authLoading } = useIsAuthEnabledQuery(undefined, { skip: !tempTokenScoped });

	// Snapshot fragment presence at mount: TempTokenScope strips the fragment
	// shortly after, so re-reading window.location.hash would flip false on
	// re-render. Only fragment-bearing arrivals are MinimalShell candidates.
	const [hadFragmentTempToken] = useState(() => {
		if (typeof window === "undefined") return false;
		const fragment = window.location.hash;
		if (!fragment || fragment.length < 2) return false;
		return !!new URLSearchParams(fragment.slice(1)).get("t");
	});

	const useMinimalShell = tempTokenScoped && !!authState?.is_auth_enabled && !authState?.has_valid_token && hadFragmentTempToken;

	const {
		data: bifrostConfig,
		error,
		isLoading,
		isFetching,
		refetch,
	} = useGetCoreConfigQuery(
		{},
		{
			skip: publicShell || useMinimalShell || (tempTokenScoped && authLoading),
		},
	);

	// Permissions are restored from sessionStorage (async) and refreshed from the
	// API. Until that first resolve, useRbac() returns false for everything, which
	// would briefly collapse the sidebar to a single tab and flash NoPermissionView
	// on the active route. Gate the full dashboard chrome on it; the cached read is
	// a single frame so this is imperceptible. Minimal/public shells don't use RBAC
	// and are handled by the early returns below.
	const { isLoading: rbacLoading } = useRbacContext();

	useEffect(() => {
		if (error) {
			toast.error(getErrorMessage(error));
		}
	}, [error]);

	if (publicShell) {
		return <MinimalShell>{children}</MinimalShell>;
	}
	if (tempTokenScoped && authLoading) {
		return <FullPageLoader />;
	}
	if (useMinimalShell) {
		return <MinimalShell>{children}</MinimalShell>;
	}

	if (rbacLoading) {
		return <FullPageLoader />;
	}

	return (
		<WebSocketProvider>
			<CookiesProvider>
				<StoreSyncInitializer />
				<TopbarProvider>
					<SidebarProvider>
						<Sidebar />
						{/* Content column: a fixed-height flex stack so the topbar takes its
					    48px and the content card absorbs the remainder. The topbar has no
					    background of its own, so it reads as the same surface as the
					    sidebar (both show the page body background). */}
						<div className="flex h-dvh w-full min-w-0 flex-col">
							<Topbar />
							{/* No w-full: in a column flex container the cross axis is width, and
							    an explicit 100% would sit *outside* the right margin, pushing it
							    off-screen. Default align-items:stretch already fills the column
							    minus margins. No top margin either: the card butts against the
							    60px topbar so its top edge lands on the same line as the sidebar's
							    search input, and --app-content-viewport compensates so full-height
							    pages still measure to the card's inner height. */}
							<div className="dark:bg-card custom-scrollbar content-container mx-0 min-h-0 min-w-0 flex-1 overflow-auto border border-gray-200 bg-white md:mr-2 md:mb-2 md:rounded-md md:px-10 dark:border-zinc-800">
								<TrialExpiryBanner />
								<main className="custom-scrollbar content-container-inner relative mx-auto flex h-full min-h-0 flex-col overflow-y-hidden md:p-4">
									{isLoading ? (
										<FullPageLoader />
									) : (
										<FullPage config={bifrostConfig} hasError={!!error} isRetrying={isFetching} onRetry={refetch}>
											{children}
										</FullPage>
									)}
								</main>
								{bifrostConfig?.is_db_connected && <OnboardingWidget />}
							</div>
						</div>
					</SidebarProvider>
				</TopbarProvider>
			</CookiesProvider>
		</WebSocketProvider>
	);
}

// MinimalShell renders a centered container without sidebar, websocket,
// store-sync, or any dashboard-config fetches. Used for routes that opt
// in via `staticData.tempTokenScoped` — typically public, scoped pages
// like the MCP per-user OAuth auth page.
function MinimalShell({ children }: { children: React.ReactNode }) {
	return (
		<div className="dark:bg-card custom-scrollbar content-container h-dvh w-full overflow-auto border border-gray-200 bg-white px-4 md:my-[0.5rem] md:h-[calc(100dvh-1rem)] md:rounded-md md:px-10 dark:border-zinc-800">
			<main className="custom-scrollbar content-container-inner relative mx-auto flex h-full min-h-0 flex-col overflow-y-hidden p-4">
				{children}
			</main>
		</div>
	);
}

function FullPage({
	config,
	hasError,
	isRetrying,
	onRetry,
	children,
}: {
	config: BifrostConfig | undefined;
	hasError: boolean;
	isRetrying: boolean;
	onRetry: () => void;
	children: React.ReactNode;
}) {
	const pathname = useLocation({ select: (l) => l.pathname });
	if (config && config.is_db_connected) {
		return children;
	}
	if (config && config.is_logs_connected && pathname.startsWith("/workspace/logs")) {
		return children;
	}
	if (hasError) {
		return <ConfigUnreachable isRetrying={isRetrying} onRetry={onRetry} />;
	}
	return <NotAvailableBanner />;
}

function ConfigUnreachable({ isRetrying, onRetry }: { isRetrying: boolean; onRetry: () => void }) {
	return (
		<div className="h-base flex items-center justify-center p-4 sm:p-6" data-testid="config-unreachable">
			<section className="bg-card w-full max-w-lg rounded-sm border p-6 sm:p-8" aria-labelledby="config-unreachable-title">
				<div className="bg-muted text-muted-foreground flex size-11 items-center justify-center rounded-sm">
					<WifiOff className="size-5" aria-hidden="true" />
				</div>
				<h1 id="config-unreachable-title" className="text-foreground mt-5 text-xl font-semibold tracking-tight">
					We can&apos;t reach the dashboard
				</h1>
				<p className="text-muted-foreground mt-2 max-w-md text-sm leading-6">
					Bifrost didn&apos;t return its configuration. This is usually a brief interruption, especially while Bifrost is being upgraded.
				</p>
				<div className="mt-6 flex items-center gap-3 border-t pt-5">
					<Button size="sm" isLoading={isRetrying} disabled={isRetrying} data-testid="config-retry-btn" onClick={onRetry}>
						{!isRetrying && <RefreshCw aria-hidden="true" />}
						{isRetrying ? "Trying again…" : "Try again"}
					</Button>
					<p className="text-muted-foreground text-xs">Your settings and data are unaffected.</p>
				</div>
			</section>
		</div>
	);
}

export function ClientLayout({ children }: { children: React.ReactNode }) {
	return (
		<ProgressProvider>
			<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
				<Toaster closeButton />
				<ReduxProvider>
					<NuqsAdapter>
						<RbacProvider>
							<AppContent>{children}</AppContent>
							{process.env.NODE_ENV === "development" && !process.env.BIFROST_DISABLE_PROFILER && <DevProfiler />}
						</RbacProvider>
					</NuqsAdapter>
				</ReduxProvider>
			</ThemeProvider>
		</ProgressProvider>
	);
}