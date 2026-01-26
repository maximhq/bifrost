"use client";

import FullPageLoader from "@/components/fullPageLoader";
import NotAvailableBanner from "@/components/notAvailableBanner";
import ProgressProvider from "@/components/progressBar";
import Sidebar from "@/components/sidebar";
import { ThemeProvider } from "@/components/themeProvider";
import { SidebarProvider } from "@/components/ui/sidebar";
import { useStoreSync } from "@/hooks/useStoreSync";
import { WebSocketProvider } from "@/hooks/useWebSocket";
import { getErrorMessage, ReduxProvider, useGetCoreConfigQuery } from "@/lib/store";
import { BifrostConfig } from "@/lib/types/config";
import { RbacProvider } from "@enterprise/lib/contexts/rbacContext";
import dynamic from "next/dynamic";
import { usePathname } from "next/navigation";
import { NuqsAdapter } from "nuqs/adapters/next/app";
import { useEffect } from "react";
import { CookiesProvider } from "react-cookie";
import { toast, Toaster } from "sonner";

// Dynamic import - only loaded in development, completely excluded from prod bundle
const DevProfiler = dynamic(() => import("@/components/devProfiler").then((mod) => ({ default: mod.DevProfiler })), { ssr: false });

function StoreSyncInitializer() {
	useStoreSync();
	return null;
}

function AppContent({ children }: { children: React.ReactNode }) {
	const { data: bifrostConfig, error, isLoading } = useGetCoreConfigQuery({});

	useEffect(() => {
		if (error) {
			toast.error(getErrorMessage(error));
		}
	}, [error]);

	return (
		<WebSocketProvider>
			<CookiesProvider>
				<StoreSyncInitializer />
				<SidebarProvider>
					<Sidebar />
					<div className="dark:bg-card custom-scrollbar my-[0.5rem] mr-[0.5rem] h-[calc(100dvh-1rem)] w-full min-w-xl overflow-auto rounded-md border border-gray-200 bg-white px-10 dark:border-zinc-800">
						<main className="custom-scrollbar relative mx-auto flex flex-col overflow-y-hidden p-4">
							{isLoading ? <FullPageLoader /> : <FullPage config={bifrostConfig}>{children}</FullPage>}
						</main>
					</div>
				</SidebarProvider>
			</CookiesProvider>
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
							{process.env.NODE_ENV === "development" && <DevProfiler />}
						</RbacProvider>
					</NuqsAdapter>
				</ReduxProvider>
			</ThemeProvider>
		</ProgressProvider>
	);
}
