"use client";

import FullPageLoader from "@/components/full-page-loader";
import NotAvailableBanner from "@/components/not-available-banner";
import ProgressProvider from "@/components/progress-bar";
import Sidebar from "@/components/sidebar";
import { ThemeProvider } from "@/components/theme-provider";
import { SidebarProvider } from "@/components/ui/sidebar";
import { WebSocketProvider } from "@/hooks/useWebSocket";
import { getErrorMessage, ReduxProvider, useGetCoreConfigQuery } from "@/lib/store";
import { Geist, Geist_Mono } from "next/font/google";
import { useEffect } from "react";
import { toast, Toaster } from "sonner";
import "./globals.css";

const geistSans = Geist({
	variable: "--font-geist-sans",
	subsets: ["latin"],
});

const geistMono = Geist_Mono({
	variable: "--font-geist-mono",
	subsets: ["latin"],
});

function AppContent({ children }: { children: React.ReactNode }) {
	const { data: bifrostConfig, error } = useGetCoreConfigQuery({});

	useEffect(() => {
		if (error) {
			toast.error(getErrorMessage(error));
		}
	}, [error]);

	return (
		<WebSocketProvider>
			<SidebarProvider>
				<Sidebar />
				<div className="dark:bg-card custom-scrollbar my-[1rem] h-[calc(100dvh-2rem)] w-full overflow-auto rounded-md border border-gray-200 bg-white dark:border-zinc-800">
					<main className="custom-scrollbar relative mx-auto flex w-5xl flex-col px-4 py-12 2xl:w-7xl">
						{bifrostConfig?.is_db_connected ? children : bifrostConfig ? <NotAvailableBanner /> : <FullPageLoader />}
					</main>
				</div>
			</SidebarProvider>
		</WebSocketProvider>
	);
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
	return (
		<html lang="en" suppressHydrationWarning>
			<body className={`${geistSans.variable} ${geistMono.variable} antialiased`}>
				<ProgressProvider>
					<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
						<Toaster />
						<ReduxProvider>
							<AppContent>{children}</AppContent>
						</ReduxProvider>
					</ThemeProvider>
				</ProgressProvider>
			</body>
		</html>
	);
}
