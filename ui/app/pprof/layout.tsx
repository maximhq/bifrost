"use client";

import { ThemeProvider } from "@/components/themeProvider";
import { ReduxProvider } from "@/lib/store";
import { isDevelopmentMode } from "@/lib/utils/port";
import { notFound } from "next/navigation";
import { Toaster } from "sonner";

export default function PprofLayout({ children }: { children: React.ReactNode }) {
	// Only allow access in development mode
	if (!isDevelopmentMode()) {
		notFound();
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
