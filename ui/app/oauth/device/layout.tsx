import { ThemeProvider } from "@/components/themeProvider";
import { NuqsAdapter } from "nuqs/adapters/tanstack-router";
import { createFileRoute } from "@tanstack/react-router";
import { Toaster } from "sonner";
import DeviceSignInPage from "./page";

// Public verification page shared by every client gateway sign-in (RFC 8628).
// A client's /login (Claude Code's, for one) opens it with ?user_code=…; the user
// confirms the code and is handed to the OAuth consent page to pick an identity. The consent page
// sends them back here with ?approved=1 once the sign-in is approved.
//
// Like /oauth/consent it renders outside the dashboard chrome and needs no
// dashboard session, so it sets up its own providers. publicShell keeps
// ClientLayout from fetching the protected config for a signed-out visitor:
// that fetch 401s and redirects to /login, which a developer signing in with
// only a virtual key can never get past.
function RouteComponent() {
	return (
		<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
			<NuqsAdapter>
				<Toaster closeButton />
				<DeviceSignInPage />
			</NuqsAdapter>
		</ThemeProvider>
	);
}

export const Route = createFileRoute("/oauth/device")({
	staticData: { publicShell: true },
	component: RouteComponent,
});