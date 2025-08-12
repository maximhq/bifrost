'use client'

import ProgressProvider from '@/components/progress-bar'
import Sidebar from '@/components/sidebar'
import { ThemeProvider } from '@/components/theme-provider'
import { SidebarProvider } from '@/components/ui/sidebar'
import { WebSocketProvider } from '@/hooks/useWebSocket'
import type { Metadata } from 'next'
import { Geist, Geist_Mono } from 'next/font/google'
import { toast, Toaster } from 'sonner'
import './globals.css'
import { useEffect, useState } from 'react'
import { BifrostConfig } from '@/lib/types/config'
import { apiService } from '@/lib/api'
import NotAvailableBanner from '@/components/not-available-banner'
import FullPageLoader from '@/components/full-page-loader'

const geistSans = Geist({
  variable: '--font-geist-sans',
  subsets: ['latin'],
})

const geistMono = Geist_Mono({
  variable: '--font-geist-mono',
  subsets: ['latin'],
})

// export const metadata: Metadata = {
//   title: 'Bifrost - The fastest LLM gateway',
//   description:
//     'Production-ready fastest LLM gateway that connects to 12+ providers through a single API. Get automatic failover, load balancing, mcp support and zero-downtime deployments.',
// }

export default function RootLayout({ children }: { children: React.ReactNode }) {
  const [bifrostConfig, setBifrostConfig] = useState<BifrostConfig | null>(null)

  useEffect(() => {
    const fetchBifrostConfig = async () => {
      const [response, error] = await apiService.getCoreConfig()
      if (error) {
        toast.error(error)
      } else if (response) {
        setBifrostConfig(response)
      }
    }
    fetchBifrostConfig()
  }, [])

  return (
    <html lang="en" suppressHydrationWarning>
      <body className={`${geistSans.variable} ${geistMono.variable} antialiased`}>
        <ProgressProvider>
          <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
            <Toaster />
            <WebSocketProvider>
              <SidebarProvider>
                <Sidebar />
                <div className="dark:bg-card custom-scrollbar my-[1rem] h-[calc(100dvh-2rem)] w-full overflow-auto rounded-md border border-gray-200 bg-white dark:border-gray-800">
                  <main className="custom-scrollbar w-5xl relative mx-auto flex flex-col px-4 py-12">
                    {bifrostConfig?.is_db_connected ? children : bifrostConfig ? <NotAvailableBanner /> : <FullPageLoader />}
                  </main>
                </div>
              </SidebarProvider>
            </WebSocketProvider>
          </ThemeProvider>
        </ProgressProvider>
      </body>
    </html>
  )
}
