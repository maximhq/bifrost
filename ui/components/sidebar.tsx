'use client'

import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'
import { Home, LucideIcon, Settings, Zap } from 'lucide-react'
import Link from 'next/link'
import { usePathname } from 'next/navigation'

interface NavItem {
  title: string
  href: string
  icon: LucideIcon
}

const navItems: NavItem[] = [
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

export function Sidebar() {
  const pathname = usePathname()

  return (
    <div className="flex h-full w-64 flex-col bg-card border-r border-border">
      <div className="flex items-center px-6 py-6">
        <div className="flex items-center space-x-3">
          <div className="flex items-center justify-center w-8 h-8 bg-primary rounded-lg">
            <Zap className="h-5 w-5 text-primary-foreground" />
          </div>
          <h1 className="text-xl font-bold text-foreground">Bifrost</h1>
        </div>
      </div>
      <Separator className="mx-4" />
      <nav className="flex-1 space-y-2 p-4">
        {navItems.map((item) => {
          const Icon = item.icon
          const isActive = pathname === item.href
          
          return (
            <Button
              key={item.href}
              variant={isActive ? 'secondary' : 'ghost'}
              className={cn(
                'w-full justify-start h-10 px-3',
                isActive 
                  ? 'bg-secondary text-secondary-foreground font-medium shadow-sm' 
                  : 'text-muted-foreground hover:text-foreground hover:bg-accent'
              )}
              asChild
            >
              <Link href={item.href}>
                <Icon className="mr-3 h-4 w-4" />
                {item.title}
              </Link>
            </Button>
          )
        })}
      </nav>
      <div className="p-4 mt-auto">
        <div className="text-xs text-muted-foreground">
          <p>Bifrost UI</p>
          <p>AI Provider Gateway</p>
        </div>
      </div>
    </div>
  )
} 