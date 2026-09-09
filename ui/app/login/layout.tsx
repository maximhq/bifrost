import { ThemeProvider } from "@/components/themeProvider";
import { useBranding } from "@/lib/hooks/useBranding";
import { ReduxProvider } from "@/lib/store/provider";
import { DEFAULT_POST_LOGIN_PATH, getLoginGotoFromSearch } from "@/lib/utils/loginGoto";
import { getApiBaseUrl } from "@/lib/utils/port";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { useTheme } from "next-themes";
import { NuqsAdapter } from "nuqs/adapters/tanstack-router";
import LoginPage from "./page";

function RouteComponent() {
	return (
		<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
			<ReduxProvider>
				<NuqsAdapter>
					<div className="bg-background min-h-screen">
						<LoginPage />
					</div>
				</NuqsAdapter>
			</ReduxProvider>
		</ThemeProvider>
	);
}

// Split out so it can call useBranding, which needs the Redux store. The
// pending state previously rendered without ReduxProvider; it is now wrapped
// below so this brief screen shows the customer's logo too rather than
// flashing the Bifrost one on the way to the login form.
function PendingCard() {
	const { resolvedTheme } = useTheme();
	const { logoSrc, logoAlt } = useBranding(resolvedTheme === "dark");
	return (
		<div className="flex min-h-screen items-center justify-center p-4">
			<div className="w-full max-w-md">
				<div className="border-border bg-card w-full space-y-6 rounded-sm border p-8">
					<div className="flex items-center justify-center">
						<img src={logoSrc} alt={logoAlt} width={160} height={26} className="max-h-[40px] w-auto max-w-[220px] object-contain" />
					</div>
					<div className="flex items-center justify-center py-6">
						<div className="text-muted-foreground text-sm">Checking authentication...</div>
					</div>
				</div>
			</div>
		</div>
	);
}

function PendingComponent() {
	return (
		<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
			<ReduxProvider>
				<PendingCard />
			</ReduxProvider>
		</ThemeProvider>
	);
}

export const Route = createFileRoute("/login")({
	loader: async ({ location }) => {
		const postLoginPath = getLoginGotoFromSearch(location.searchStr) ?? DEFAULT_POST_LOGIN_PATH;
		let data: { is_auth_enabled: boolean; has_valid_token: boolean } | null = null;
		try {
			const res = await fetch(`${getApiBaseUrl()}/session/is-auth-enabled`, {
				credentials: "include",
			});
			if (res.ok) {
				data = await res.json();
			}
		} catch {
			// Fetch failed — fall through to login page
		}
		if (data && (!data.is_auth_enabled || data.has_valid_token)) {
			// If auth is disabled but SSO is configured (restart pending), stay on
			// the login page so the user sees the restart notice instead of looping.
			if (!data.is_auth_enabled) {
				try {
					const authTypeRes = await fetch(`${getApiBaseUrl()}/auth/type`, {
						credentials: "include",
					});
					if (authTypeRes.ok) {
						const authType: { type: string } = await authTypeRes.json();
						if (authType.type === "sso") {
							return; // SSO configured — show login form with restart notice
						}
					}
				} catch {
					// Ignore — fall through to the workspace redirect
				}
			}
			throw redirect({ href: postLoginPath });
		}
	},
	pendingComponent: PendingComponent,
	pendingMs: 0,
	component: RouteComponent,
});