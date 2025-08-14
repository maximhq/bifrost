import ProgressProvider from '@/components/progress-bar'
import Sidebar from '@/components/sidebar'
import { ThemeProvider } from '@/components/theme-provider'
import { SidebarProvider } from '@/components/ui/sidebar'
import { WebSocketProvider } from '@/hooks/useWebSocket'
import type { Metadata } from 'next'
import { Geist, Geist_Mono } from 'next/font/google'
import { Toaster } from 'sonner'
import './globals.css'

const geistSans = Geist({
  variable: '--font-geist-sans',
  subsets: ['latin'],
})

const geistMono = Geist_Mono({
  variable: '--font-geist-mono',
  subsets: ['latin'],
})

export const metadata: Metadata = {
  title: 'Bifrost - The fastest LLM gateway',
  description:
    'Production-ready fastest LLM gateway that connects to 12+ providers through a single API. Get automatic failover, load balancing, mcp support and zero-downtime deployments.',
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className={`${geistSans.variable} ${geistMono.variable} antialiased`}>
        <ProgressProvider>
          <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
            <Toaster />
            <WebSocketProvider>
              <SidebarProvider>
                <Sidebar />
                <div className="w-full bg-white dark:bg-card h-[calc(100dvh-2rem)] mt-[1rem] mb-[1rem] rounded-md border border-gray-200 dark:border-gray-800 overflow-auto custom-scrollbar">
                  <main className="custom-scrollbar w-5xl relative mx-auto flex  flex-col px-4 py-12">{children}</main>
                </div>
              </SidebarProvider>
            </WebSocketProvider>
          </ThemeProvider>
        </ProgressProvider>
      </body>
    </html>
  )
}
