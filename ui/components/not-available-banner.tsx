import React from 'react'
import { AlertCircle, Database } from 'lucide-react'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import Link from 'next/link'

const NotAvailableBanner = () => {
  return (
    <div className="h-base flex items-center justify-center p-4">
      <div className="w-full max-w-md">
        <Alert className="border-destructive/50 text-destructive dark:border-destructive [&>svg]:text-destructive">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle className="flex items-center gap-2">
            <Database className="h-4 w-4" />
            Database Not Configured
          </AlertTitle>
          <AlertDescription className="mt-2 space-y-2">
            <div>Bifrost’s UI requires a database connection, but no database is configured in your config.json</div>
            <div className="text-muted-foreground text-sm">
              To enable the UI, please add the database settings to your config.json (see{' '}
              <Link href="https://getmaxim.ai/bifrost/docs/config" target="_blank" className="font-medium underline underline-offset-2">
                documentation
              </Link>
              ).
            </div>
          </AlertDescription>
        </Alert>
      </div>
    </div>
  )
}

export default NotAvailableBanner
