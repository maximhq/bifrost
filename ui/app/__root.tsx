import { Outlet, createRootRoute, redirect } from "@tanstack/react-router";

/**
 * Root route. Intentionally minimal — does NOT wrap children in providers,
 * because the existing nested layouts (app/workspace/layout.tsx →
 * ClientLayout, app/login/layout.tsx, app/pprof/layout.tsx) each set up
 * their own ThemeProvider / ReduxProvider / NuqsAdapter / etc.
 *
 * If/when we consolidate provider setup, the providers can move here.
 */
export const Route = createRootRoute({
	beforeLoad: ({ location }) => {
		// Root index "/" → /login. The old app/page.tsx redirect lived here.
		if (location.pathname === "/" || location.pathname === "") {
			throw redirect({ to: "/login" });
		}
	},
	component: RootComponent,
});

function RootComponent() {
	return <Outlet />;
}