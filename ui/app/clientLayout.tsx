'use client'

import FullPageLoader from '@/components/fullPageLoader'
import NotAvailableBanner from '@/components/notAvailableBanner'
import ProgressProvider from '@/components/progressBar'
import Sidebar from '@/components/sidebar'
import { ThemeProvider } from '@/components/themeProvider'
import { SidebarProvider } from '@/components/ui/sidebar'
import { WebSocketProvider } from '@/hooks/useWebSocket'
import { getErrorMessage, ReduxProvider, useGetCoreConfigQuery } from '@/lib/store'
import { NuqsAdapter } from 'nuqs/adapters/next/app'
import { useEffect } from 'react'
import { toast, Toaster } from 'sonner'

function AppContent ({ children }: { children: React.ReactNode }) {
  const { data: bifrostConfig, error } = useGetCoreConfigQuery({})

  useEffect(() => {
    if (error) {
      toast.error(getErrorMessage(error))
    }
  }, [error])

  return (
    <WebSocketProvider>
      <SidebarProvider>
        <Sidebar />
        <div className="dark:bg-card custom-scrollbar my-[1rem] h-[calc(100dvh-2rem)] w-full overflow-auto rounded-md border border-gray-200 bg-white dark:border-zinc-800">
          <main className="custom-scrollbar relative mx-auto flex flex-col p-4">
            {bifrostConfig?.is_db_connected ? children : bifrostConfig ? <NotAvailableBanner /> : <FullPageLoader />}
          </main>
        </div>
      </SidebarProvider>
    </WebSocketProvider>
  )
}

export function ClientLayout ({ children }: { children: React.ReactNode }) {
  return (
    <ProgressProvider>
      <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
        <Toaster />
        <ReduxProvider>
          <NuqsAdapter>
            <AppContent>{children}</AppContent>
          </NuqsAdapter>
        </ReduxProvider>
      </ThemeProvider>
    </ProgressProvider>
  )
}

