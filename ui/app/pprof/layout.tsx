import { ThemeProvider } from "@/components/themeProvider";
import { ReduxProvider } from "@/lib/store";
import { isDevelopmentMode } from "@/lib/utils/port";
import { createFileRoute, notFound } from "@tanstack/react-router";
import { Toaster } from "sonner";
import PprofPage from "./page";

function PprofLayout({ children }: { children: React.ReactNode }) {
	// Only allow access in development mode
	if (!isDevelopmentMode()) {
		throw notFound();
	}

	return (
		<ThemeProvider attribute="class" defaultTheme="dark" enableSystem>
			<Toaster />
			<ReduxProvider>
				<div className="min-h-screen bg-zinc-950 text-zinc-100">{children}</div>
			</ReduxProvider>
		</ThemeProvider>
	);
}

function RouteComponent() {
	return (
		<PprofLayout>
			<PprofPage />
		</PprofLayout>
	);
}

export const Route = createFileRoute("/pprof")({
	component: RouteComponent,
});