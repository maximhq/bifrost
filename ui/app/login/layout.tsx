import { ThemeProvider } from "@/components/themeProvider";
import { ReduxProvider } from "@/lib/store/provider";
import { DEFAULT_POST_LOGIN_PATH, getLoginGotoFromSearch } from "@/lib/utils/loginGoto";
import { getApiBaseUrl } from "@/lib/utils/port";
import { createFileRoute, redirect } from "@tanstack/react-router";
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

function PendingComponent() {
	return (
		<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
			<div className="flex min-h-screen items-center justify-center p-4">
				<div className="w-full max-w-md">
					<div className="border-border bg-card w-full space-y-6 rounded-sm border p-8">
						<div className="flex items-center justify-center">
							<img src="/bifrost-logo.webp" alt="Bifrost" width={160} height={26} />
						</div>
						<div className="flex items-center justify-center py-6">
							<div className="text-muted-foreground text-sm">Checking authentication...</div>
						</div>
					</div>
				</div>
			</div>
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