import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import "./globals.css";
import Sidebar from "@/components/sidebar";
import { SidebarProvider } from "@/components/ui/sidebar";
import { ThemeProvider } from "@/components/theme-provider";
import { Toaster } from "sonner";
import ProgressProvider from "@/components/progress-bar";

const geistSans = Geist({
	variable: "--font-geist-sans",
	subsets: ["latin"],
});

const geistMono = Geist_Mono({
	variable: "--font-geist-mono",
	subsets: ["latin"],
});

export const metadata: Metadata = {
	title: "Bifrost UI - AI Gateway Dashboard",
	description:
		"Production-ready AI gateway that connects to 8+ providers through a single API. Get automatic failover, intelligent load balancing, and zero-downtime deployments.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
	return (
		<html lang="en" suppressHydrationWarning>
			<body className={`${geistSans.variable} ${geistMono.variable} antialiased`}>
				<ProgressProvider>
					<ThemeProvider attribute="class" defaultTheme="system" enableSystem>
						<Toaster />
						<SidebarProvider>
							<Sidebar />
							<main className="relative mx-auto flex min-h-screen w-5xl flex-col pt-24">{children}</main>
						</SidebarProvider>
					</ThemeProvider>
				</ProgressProvider>
			</body>
		</html>
	);
}
