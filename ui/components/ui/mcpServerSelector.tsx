"use client"

import { ExternalLink, X } from "lucide-react"
import { useCallback, useMemo } from "react"
import { AsyncMultiSelect } from "./asyncMultiselect"
import { Badge } from "./badge"
import { Button } from "./button"
import { Option } from "./multiselectUtils"
import { cn } from "./utils"

// Types
export interface MCPServerInfo {
  config: {
    client_id: string
    name: string
    connection_type?: string
  }
  tools?: { name: string }[]
  state?: string
}

interface ServerOptionMeta {
  clientId: string
  name: string
  connectionType?: string
  toolCount: number
  state?: string
}

interface MCPServerSelectorProps {
  value: string[]
  onChange: (serverIds: string[]) => void
  mcpClients: MCPServerInfo[]
  placeholder?: string
  disabled?: boolean
  className?: string
  /** Base URL path for the MCP registry page (default: /workspace/mcp-registry) */
  registryPath?: string
}

export function MCPServerSelector({
  value,
  onChange,
  mcpClients,
  placeholder = "Search and select MCP servers...",
  disabled = false,
  className,
  registryPath = "/workspace/mcp-registry",
}: MCPServerSelectorProps) {
  // Create options from MCP clients using meta for complex data
  const allServerOptions = useMemo((): Option<ServerOptionMeta>[] => {
    return mcpClients.map((client) => ({
      label: client.config.name,
      value: client.config.client_id,
      meta: {
        clientId: client.config.client_id,
        name: client.config.name,
        connectionType: client.config.connection_type,
        toolCount: client.tools?.length || 0,
        state: client.state,
      },
    }))
  }, [mcpClients])

  // Get full server info for selected servers
  const selectedServersWithInfo = useMemo(() => {
    return value.map((serverId) => {
      const client = mcpClients.find((c) => c.config.client_id === serverId)
      return {
        clientId: serverId,
        name: client?.config.name || serverId,
        connectionType: client?.config.connection_type,
        toolCount: client?.tools?.length || 0,
        state: client?.state,
      }
    })
  }, [value, mcpClients])

  // Filter out already selected servers from options
  const availableOptions = useMemo(() => {
    const selectedSet = new Set(value)
    return allServerOptions.filter((opt) => !selectedSet.has(opt.value))
  }, [allServerOptions, value])

  const handleSelectServer = useCallback(
    (selected: Option<ServerOptionMeta>[]) => {
      if (selected.length === 0) return

      const newServer = selected[selected.length - 1]
      if (!newServer?.meta) return

      // Check if already selected
      if (!value.includes(newServer.meta.clientId)) {
        onChange([...value, newServer.meta.clientId])
      }
    },
    [value, onChange]
  )

  const handleRemoveServer = useCallback(
    (serverId: string) => {
      onChange(value.filter((id) => id !== serverId))
    },
    [value, onChange]
  )

  const reload = useCallback(
    (query: string, callback: (options: Option<ServerOptionMeta>[]) => void) => {
      const lowerQuery = query.toLowerCase()
      const filtered = availableOptions.filter((opt) =>
        opt.label.toLowerCase().includes(lowerQuery)
      )
      callback(filtered)
    },
    [availableOptions]
  )

  const getConnectionTypeBadgeVariant = (type?: string) => {
    switch (type) {
      case "http":
        return "default"
      case "stdio":
        return "secondary"
      case "sse":
        return "outline"
      default:
        return "outline"
    }
  }

  const getStateBadgeVariant = (state?: string) => {
    switch (state) {
      case "connected":
        return "success"
      case "error":
        return "destructive"
      default:
        return "secondary"
    }
  }

  return (
		<div className={cn("space-y-3", className)}>
			{/* Search dropdown */}
			<AsyncMultiSelect<ServerOptionMeta>
				placeholder={placeholder}
				disabled={disabled}
				defaultOptions={availableOptions}
				reload={reload}
				debounce={150}
				onChange={handleSelectServer}
				value={[]}
				isClearable={false}
				closeMenuOnSelect={true}
				hideSelectedOptions={true}
				controlShouldRenderValue={false}
				noOptionsMessage={() => "No MCP servers found"}
				views={{
					option: (props) => {
						// Access data as Option<ServerOptionMeta> since that's the actual runtime type
						const data = props.data as unknown as Option<ServerOptionMeta>;
						return (
							<div
								className={cn(
									"my-1 flex w-full cursor-pointer items-center justify-between rounded-sm p-2 text-sm",
									props.isFocused && "bg-accent dark:!bg-card",
									"hover:bg-accent",
									props.isSelected && "bg-accent dark:!bg-card",
								)}
								onClick={() => props.selectOption(props.data)}
							>
								<div className="flex items-center gap-2">
									<span className="text-content-primary font-medium">{data.meta?.name}</span>
									{data.meta?.connectionType && (
										<Badge variant={getConnectionTypeBadgeVariant(data.meta.connectionType)} className="text-xs uppercase">
											{data.meta.connectionType}
										</Badge>
									)}
								</div>
								<span className="text-content-tertiary text-xs">{data.meta?.toolCount} tools</span>
							</div>
						);
					},
				}}
			/>

			{/* Selected servers list */}
			{selectedServersWithInfo.length > 0 && (
				<div className="space-y-2">
					{selectedServersWithInfo.map((server) => (
						<div key={server.clientId} className="bg-muted/50 flex items-center justify-between rounded-md border px-3 py-2">
							<div className="flex items-center gap-2">
								<span className="text-foreground text-sm font-medium">{server.name}</span>
								{server.connectionType && (
									<Badge variant={getConnectionTypeBadgeVariant(server.connectionType)} className="text-xs uppercase">
										{server.connectionType}
									</Badge>
								)}
								{server.state && (
									<Badge variant={getStateBadgeVariant(server.state)} className="text-xs capitalize">
										{server.state}
									</Badge>
								)}
								<span className="text-muted-foreground text-xs">{server.toolCount} tools</span>
							</div>
							<div className="flex items-center gap-1">
								<Button type="button" variant="ghost" size="sm" className="h-8 w-8 p-0" asChild>
									<a
										href={`${registryPath}?server=${server.clientId}`}
										target="_blank"
										rel="noopener noreferrer"
										title="Open server details in new tab"
									>
										<ExternalLink className="h-4 w-4" />
									</a>
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="sm"
									className="h-8 w-8 p-0"
									onClick={() => handleRemoveServer(server.clientId)}
									disabled={disabled}
								>
									<X className="h-4 w-4" />
								</Button>
							</div>
						</div>
					))}
				</div>
			)}

			{/* Empty state */}
			{selectedServersWithInfo.length === 0 && (
				<div className="text-muted-foreground rounded-md border border-dashed p-4 text-center text-sm">
					No servers selected. Use the search above to add MCP servers.
				</div>
			)}
		</div>
	);
}
