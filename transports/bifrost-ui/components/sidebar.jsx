'use client'

import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'
import { Home, Settings, Zap } from 'lucide-react'
import Link from 'next/link'
import { usePathname } from 'next/navigation'

const navItems = [
  {
    title: 'Home',
    href: '/',
    icon: Home
  },
  {
    title: 'Config',
    href: '/config',
    icon: Settings
  }
]

export function Sidebar () {
  const pathname = usePathname()

  return (
    <div className="flex h-full w-64 flex-col bg-background border-r">
      <div className="flex items-center px-6 py-4">
        <div className="flex items-center space-x-2">
          <Zap className="h-6 w-6 text-primary" />
          <h1 className="text-xl font-bold">Bifrost</h1>
        </div>
      </div>
      <Separator />
      <nav className="flex-1 space-y-1 p-4">
        {navItems.map((item) => {
          const Icon = item.icon
          const isActive = pathname === item.href
          
          return (
            <Button
              key={item.href}
              variant={isActive ? 'secondary' : 'ghost'}
              className={cn(
                'w-full justify-start',
                isActive && 'bg-secondary font-medium'
              )}
              asChild
            >
              <Link href={item.href}>
                <Icon className="mr-2 h-4 w-4" />
                {item.title}
              </Link>
            </Button>
          )
        })}
      </nav>
    </div>
  )
} 