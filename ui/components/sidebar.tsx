'use client'

import { BoxIcon, BugIcon, ExternalLink, Puzzle, Settings2Icon, Telescope, Shield } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from '@/components/ui/sidebar'
import { useWebSocket } from '@/hooks/useWebSocket'
import { cn } from '@/lib/utils'
import { BooksIcon, DiscordLogoIcon, GithubLogoIcon } from '@phosphor-icons/react'
import { useTheme } from 'next-themes'
import Image from 'next/image'
import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { useEffect, useState } from 'react'
import { ThemeToggle } from './theme-toggle'
import { apiService } from '@/lib/api'
import { toast } from 'sonner'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'

// Custom MCP Icon Component
const MCPIcon = ({ className }: { className?: string }) => (
  <svg
    className={className}
    fill="currentColor"
    fillRule="evenodd"
    height="1em"
    style={{ flex: 'none', lineHeight: 1 }}
    viewBox="0 0 24 24"
    width="1em"
    xmlns="http://www.w3.org/2000/svg"
    aria-label="MCP clients icon"
  >
    <title>MCP clients icon</title>
    <path d="M15.688 2.343a2.588 2.588 0 00-3.61 0l-9.626 9.44a.863.863 0 01-1.203 0 .823.823 0 010-1.18l9.626-9.44a4.313 4.313 0 016.016 0 4.116 4.116 0 011.204 3.54 4.3 4.3 0 013.609 1.18l.05.05a4.115 4.115 0 010 5.9l-8.706 8.537a.274.274 0 000 .393l1.788 1.754a.823.823 0 010 1.18.863.863 0 01-1.203 0l-1.788-1.753a1.92 1.92 0 010-2.754l8.706-8.538a2.47 2.47 0 000-3.54l-.05-.049a2.588 2.588 0 00-3.607-.003l-7.172 7.034-.002.002-.098.097a.863.863 0 01-1.204 0 .823.823 0 010-1.18l7.273-7.133a2.47 2.47 0 00-.003-3.537z" />
    <path d="M14.485 4.703a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a4.115 4.115 0 000 5.9 4.314 4.314 0 006.016 0l7.12-6.982a.823.823 0 000-1.18.863.863 0 00-1.204 0l-7.119 6.982a2.588 2.588 0 01-3.61 0 2.47 2.47 0 010-3.54l7.12-6.982z" />
  </svg>
)

// Main navigation items
const navigationItems = [
  {
    title: 'Logs',
    url: '/',
    icon: Telescope,
    description: 'Request logs & monitoring',
  },
  {
    title: 'Providers',
    url: '/providers',
    icon: BoxIcon,
    description: 'Configure models',
  },
  {
    title: 'MCP clients',
    url: '/mcp-clients',
    icon: MCPIcon,
    description: 'MCP configuration',
  },
  {
    title: 'Governance',
    url: '/governance',
    icon: Shield,
    description: 'Manage virtual keys & access',
  },
  {
    title: 'Config',
    url: '/config',
    icon: Settings2Icon,
    description: 'Bifrost settings',
  },
  {
    title: 'Docs',
    url: '/docs',
    icon: BooksIcon,
    description: 'Documentation & guides',
  },
  {
    title: 'Plugins',
    url: '/plugins',
    icon: Puzzle,
    description: 'Extend Bifrost functionality',
    badge: 'Beta',
  },
]

// External links
const externalLinks = [
  {
    title: 'Discord Server',
    url: 'https://getmax.im/bifrost-discord',
    icon: DiscordLogoIcon,
  },
  {
    title: 'GitHub Repository',
    url: 'https://github.com/maximhq/bifrost',
    icon: GithubLogoIcon,
  },
  {
    title: 'Report a bug',
    url: 'https://github.com/maximhq/bifrost/issues/new?title=[Bug Report]&labels=bug&type=bug&projects=maximhq/1',
    icon: BugIcon,
  },
  {
    title: 'Full Documentation',
    url: 'https://github.com/maximhq/bifrost/tree/main/docs',
    icon: BooksIcon,
  },
]

export default function AppSidebar() {
  const pathname = usePathname()
  const [mounted, setMounted] = useState(false)
  const { resolvedTheme } = useTheme()

  const [isGovernanceEnabled, setIsGovernanceEnabled] = useState(false)

  useEffect(() => {
    setMounted(true)
  }, [])

  useEffect(() => {
    const fetchConfig = async () => {
      const [coreConfig, error] = await apiService.getCoreConfig()
      if (error) {
        toast.error(error)
      } else if (coreConfig) {
        setIsGovernanceEnabled(coreConfig.enable_governance)
      }
    }
    fetchConfig()
  }, [])

  const isActiveRoute = (url: string) => {
    if (url === '/' && pathname === '/') return true
    if (url !== '/' && pathname.startsWith(url)) return true
    return false
  }

  // Always render the light theme version for SSR to avoid hydration mismatch
  const logoSrc = mounted && resolvedTheme === 'dark' ? '/bifrost-logo-dark.png' : '/bifrost-logo.png'

  const { isConnected: isWebSocketConnected } = useWebSocket()

  return (
    <Sidebar className="border-border border-r">
      <SidebarHeader className="flex h-12 justify-between border-b px-0">
        <div className="flex h-full items-center justify-between gap-2 px-1.5">
          <Link href="/" className="group flex items-center gap-2">
            <Image className="h-10 w-auto" src={logoSrc} alt="Bifrost" width={100} height={100} />
          </Link>
          <ThemeToggle />
        </div>
      </SidebarHeader>

      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu className="space-y-1">
              {navigationItems.map((item) => {
                const isActive = isActiveRoute(item.url)
                const isAllowed = item.title === 'Governance' ? isGovernanceEnabled : true
                return (
                  <TooltipProvider key={item.title}>
                    <Tooltip>
                      <TooltipTrigger>
                        <SidebarMenuItem>
                          <SidebarMenuButton
                            asChild
                            className={`relative h-16 rounded-lg border px-3 transition-all duration-200 ${
                              isActive
                                ? 'bg-accent text-primary border-primary/10'
                                : isAllowed
                                  ? 'hover:bg-accent hover:text-accent-foreground border-transparent'
                                  : 'hover:bg-destructive/5 hover:text-muted-foreground text-muted-foreground cursor-default border-transparent'
                            } `}
                          >
                            <Link href={isAllowed ? item.url : '#'} className="flex w-full items-center justify-between">
                              <div>
                                <div className="flex items-center gap-2">
                                  <item.icon className={`h-4 w-4 ${isActive ? 'text-primary' : 'text-muted-foreground'}`} />
                                  <span className="text-sm font-medium">{item.title}</span>
                                </div>
                                <span className="text-muted-foreground overflow-hidden text-ellipsis whitespace-nowrap text-xs">
                                  {item.description}
                                </span>
                              </div>
                              {item.url === '/' && isWebSocketConnected && (
                                <div className="h-2 w-2 animate-pulse rounded-full bg-green-800 dark:bg-green-200" />
                              )}
                              {item.badge && (
                                <Badge
                                  variant={item.badge === 'Live' ? 'default' : 'outline'}
                                  className={cn('h-5 px-2 py-0.5 text-xs', item.badge === 'Live' && 'animate-pulse duration-200')}
                                >
                                  {item.badge}
                                </Badge>
                              )}
                            </Link>
                          </SidebarMenuButton>
                        </SidebarMenuItem>
                      </TooltipTrigger>
                      {!isAllowed && <TooltipContent side="right">Please enable governance in the config page</TooltipContent>}
                    </Tooltip>
                  </TooltipProvider>
                )
              })}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>

        <SidebarGroup>
          <SidebarGroupLabel className="text-muted-foreground px-3 py-2 text-xs font-semibold uppercase tracking-wider">
            Resources
          </SidebarGroupLabel>
          <SidebarGroupContent>
            <SidebarMenu className="space-y-1">
              {externalLinks.map((item) => (
                <SidebarMenuItem key={item.title}>
                  <SidebarMenuButton
                    asChild
                    className="hover:bg-accent hover:text-accent-foreground h-9 rounded-lg px-3 transition-all duration-200"
                  >
                    <a href={item.url} target="_blank" rel="noopener noreferrer" className="group flex w-full items-center justify-between">
                      <div className="flex items-center space-x-3">
                        <item.icon className="text-muted-foreground h-4 w-4" size={16} weight="bold" />
                        <span className="text-sm">{item.title}</span>
                      </div>
                      <ExternalLink className="text-muted-foreground h-3 w-3 opacity-0 transition-opacity group-hover:opacity-100" />
                    </a>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              ))}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      </SidebarContent>

      <SidebarFooter className="border-border border-t px-6 py-4">
        <div className="text-muted-foreground mx-auto flex w-fit items-center space-x-1 text-xs">
          <span>Made with ♥️ by</span>
          <a href="https://getmaxim.ai" target="_blank" rel="noopener noreferrer" className="text-primary">
            Maxim AI
          </a>
        </div>
      </SidebarFooter>
    </Sidebar>
  )
}
