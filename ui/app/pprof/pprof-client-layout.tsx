"use client";

import { ThemeProvider } from "@/components/themeProvider";
import { ReduxProvider } from "@/lib/store";
import { Toaster } from "sonner";

export function PprofClientLayout({ children }: { children: React.ReactNode }) {
	return (
		<ThemeProvider attribute="class" defaultTheme="dark" enableSystem>
			<Toaster />
			<ReduxProvider>
				<div className="min-h-screen bg-zinc-950 text-zinc-100">{children}</div>
			</ReduxProvider>
		</ThemeProvider>
	);
}
