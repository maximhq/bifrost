"use client";

import FullPageLoader from "@/components/fullPageLoader";
import NotAvailableBanner from "@/components/notAvailableBanner";
import ProgressProvider from "@/components/progressBar";
import Sidebar from "@/components/sidebar";
import { ThemeProvider } from "@/components/themeProvider";
import { SidebarProvider } from "@/components/ui/sidebar";
import { WebSocketProvider } from "@/hooks/useWebSocket";
import { getErrorMessage, ReduxProvider, useGetCoreConfigQuery } from "@/lib/store";
import { BifrostConfig } from "@/lib/types/config";
import { RbacProvider } from "@enterprise/lib/contexts/rbacContext";
import { usePathname } from "next/navigation";
import { NuqsAdapter } from "nuqs/adapters/next/app";
import { useEffect } from "react";
import { toast, Toaster } from "sonner";

function AppContent({ children }: { children: React.ReactNode }) {
	const { data: bifrostConfig, error, isLoading } = useGetCoreConfigQuery({});

	useEffect(() => {
		if (error) {
			toast.error(getErrorMessage(error));
		}
	}, [error]);

	return (
		<WebSocketProvider>
			<SidebarProvider>
				<Sidebar />
				<div className="dark:bg-card custom-scrollbar my-[1rem] h-[calc(100dvh-2rem)] w-full overflow-auto rounded-tl-md rounded-bl-md border border-gray-200 bg-white dark:border-zinc-800">
					<main className="custom-scrollbar relative mx-auto flex flex-col overflow-y-hidden p-4">
						{isLoading ? <FullPageLoader /> : <FullPage config={bifrostConfig}>{children}</FullPage>}
					</main>
				</div>
			</SidebarProvider>
		</WebSocketProvider>
	);
}

function FullPage({ config, children }: { config: BifrostConfig | undefined; children: React.ReactNode }) {
	const pathname = usePathname();
	if (config && config.is_db_connected) {
		return children;
	}
	if (config && config.is_logs_connected && pathname.startsWith("/workspace/logs")) {
		return children;
	}
	return <NotAvailableBanner />;
}

export function ClientLayout({ children }: { children: React.ReactNode }) {
	return (
		<ProgressProvider>
			<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
				<Toaster />
				<ReduxProvider>
					<NuqsAdapter>
						<RbacProvider>
							<AppContent>{children}</AppContent>
						</RbacProvider>
					</NuqsAdapter>
				</ReduxProvider>
			</ThemeProvider>
		</ProgressProvider>
	);
}
