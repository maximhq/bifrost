import TempTokenScope from "@/components/tempTokenScope";
import { ThemeProvider } from "@/components/themeProvider";
import { ReduxProvider } from "@/lib/store";
import { NuqsAdapter } from "nuqs/adapters/tanstack-router";
import { createFileRoute } from "@tanstack/react-router";
import { Toaster } from "sonner";
import OAuth2ConsentPage from "./page";

// Public OAuth2 consent page — renders outside the dashboard chrome so
// external users who arrive via `claude mcp add` can pick their identity
// without needing a Bifrost dashboard account.
//
// tempTokenScoped: ClientLayout renders MinimalShell and skips the protected
// config fetch when this flag is set. TempTokenScope extracts the `#t=…`
// fragment minted by /oauth2/authorize and attaches it as
// X-Bifrost-Temp-Token on every API call, letting the consent flow APIs
// authenticate the anonymous visitor.
//
// ThemeProvider, ReduxProvider, NuqsAdapter and Toaster are provided here
// directly because this route sits outside /workspace and does not inherit
// them from ClientLayout.
function RouteComponent() {
	return (
		<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
			<ReduxProvider>
				<NuqsAdapter>
					<Toaster closeButton />
					<TempTokenScope name="oauth2_consent">
						<OAuth2ConsentPage />
					</TempTokenScope>
				</NuqsAdapter>
			</ReduxProvider>
		</ThemeProvider>
	);
}

export const Route = createFileRoute("/oauth/consent")({
	staticData: { tempTokenScoped: true },
	component: RouteComponent,
});
