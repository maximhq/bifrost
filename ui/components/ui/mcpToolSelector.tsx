"use client"

import { CodeEditor } from "@/app/workspace/logs/views/codeEditor";
import { ChevronDown, ChevronRight, X } from "lucide-react"
import { useCallback, useMemo, useState } from "react"
import { components, OptionProps } from "react-select";
import { AsyncMultiSelect } from "./asyncMultiselect"
import { Badge } from "./badge"
import { Button } from "./button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "./collapsible"
import { Option } from "./multiselectUtils";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./table";
import { cn } from "./utils"

// Types
export interface SelectedTool {
  mcpClientId: string
  toolName: string
}

export interface ToolFunction {
  name: string
  description?: string
  // Parameters can be any object type to support various schema formats
  parameters?: Record<string, unknown> | object
  strict?: boolean
}

export interface MCPClientInfo {
  config: {
    client_id: string
    name: string
    connection_type?: string
  }
  tools: ToolFunction[]
  state?: string
}

interface ToolOptionMeta {
  mcpClientId: string
  mcpClientName: string
  toolName: string
  description?: string
  parameters?: Record<string, unknown> | object
}

interface MCPToolSelectorProps {
  value: SelectedTool[]
  onChange: (tools: SelectedTool[]) => void
  mcpClients: MCPClientInfo[]
  placeholder?: string
  disabled?: boolean
  className?: string
}

export function MCPToolSelector({
  value,
  onChange,
  mcpClients,
  placeholder = "Search and select tools...",
  disabled = false,
  className,
}: MCPToolSelectorProps) {
  const [expandedTools, setExpandedTools] = useState<Set<string>>(new Set())

  // Flatten all tools from all MCP clients into searchable options
  // Using meta field for complex data as per Option type definition
  const allToolOptions = useMemo(() => {
    const options: Option<ToolOptionMeta>[] = []
    
    for (const client of mcpClients) {
      if (!client.tools) continue
      
      for (const tool of client.tools) {
        const key = `${client.config.client_id}:${tool.name}`
        
        options.push({
          label: `${client.config.name} / ${tool.name}`,
          value: key,
          meta: {
            mcpClientId: client.config.client_id,
            mcpClientName: client.config.name,
            toolName: tool.name,
            description: tool.description,
            parameters: tool.parameters,
          },
        })
      }
    }
    
    return options
  }, [mcpClients])

  // Get full tool info for selected tools
  const selectedToolsWithInfo = useMemo(() => {
    return value.map((selected) => {
      const client = mcpClients.find((c) => c.config.client_id === selected.mcpClientId)
      const tool = client?.tools?.find((t) => t.name === selected.toolName)
      return {
        ...selected,
        mcpClientName: client?.config.name || selected.mcpClientId,
        description: tool?.description,
        parameters: tool?.parameters,
      }
    })
  }, [value, mcpClients])

  // Filter out already selected tools from options
  const availableOptions = useMemo(() => {
    const selectedKeys = new Set(
      value.map((t) => `${t.mcpClientId}:${t.toolName}`)
    )
    return allToolOptions.filter((opt) => !selectedKeys.has(opt.value))
  }, [allToolOptions, value])

  const toggleExpanded = useCallback((key: string) => {
    setExpandedTools((prev) => {
      const next = new Set(prev)
      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
      }
      return next
    })
  }, [])

  const handleSelectTool = useCallback(
    (selected: Option<ToolOptionMeta>[]) => {
      if (selected.length === 0) return
      
      const newTool = selected[selected.length - 1]
      if (!newTool?.meta) return
      
      const newSelectedTool: SelectedTool = {
        mcpClientId: newTool.meta.mcpClientId,
        toolName: newTool.meta.toolName,
      }
      
      // Check if already selected
      const exists = value.some(
        (t) => t.mcpClientId === newSelectedTool.mcpClientId && t.toolName === newSelectedTool.toolName
      )
      
      if (!exists) {
        onChange([...value, newSelectedTool])
      }
    },
    [value, onChange]
  )

  const handleRemoveTool = useCallback(
    (mcpClientId: string, toolName: string) => {
      onChange(value.filter((t) => !(t.mcpClientId === mcpClientId && t.toolName === toolName)))
    },
    [value, onChange]
  )

  const reload = useCallback(
    (query: string, callback: (options: Option<ToolOptionMeta>[]) => void) => {
      const lowerQuery = query.toLowerCase()
      const filtered = availableOptions.filter(
        (opt) =>
          opt.label.toLowerCase().includes(lowerQuery) ||
          opt.meta?.description?.toLowerCase().includes(lowerQuery)
      )
      callback(filtered)
    },
    [availableOptions]
  )

  return (
		<div className={cn("space-y-3", className)}>
			{/* Search dropdown */}
			<AsyncMultiSelect<ToolOptionMeta>
				placeholder={placeholder}
				disabled={disabled}
				defaultOptions={availableOptions}
				reload={reload}
				debounce={150}
				onChange={handleSelectTool}
				value={[]}
				isClearable={false}
				closeMenuOnSelect={true}
				hideSelectedOptions={true}
				controlShouldRenderValue={false}
				noOptionsMessage={() => "No results found"}
				views={{
					option: (optionProps: OptionProps<ToolOptionMeta>) => {
						const { Option } = components;
						// Access data as Option<ToolOptionMeta> since that's the actual runtime type
						const data = optionProps.data as unknown as Option<ToolOptionMeta>;
						return (
							<Option
								{...optionProps}
								className={cn(
									"my-1 flex w-full cursor-pointer flex-col gap-0.5 rounded-sm p-2 text-sm",
									optionProps.isFocused && "bg-accent dark:!bg-card",
									"hover:bg-accent",
									optionProps.isSelected && "bg-accent dark:!bg-card",
								)}
							>
								<div className="flex items-center gap-2">
									<span className="text-content-primary font-medium">{data.meta?.toolName}</span>
									<Badge variant="outline" className="text-xs">
										{data.meta?.mcpClientName}
									</Badge>
								</div>
								{data.meta?.description && <span className="text-content-tertiary line-clamp-2 text-xs">{data.meta.description}</span>}
							</Option>
						);
					},
				}}
			/>

			{/* Selected tools table */}
			{selectedToolsWithInfo.length > 0 && (
				<div className="overflow-hidden rounded-md border">
					<Table className="table-fixed">
						<TableHeader>
							<TableRow>
								<TableHead className="w-10"></TableHead>
								<TableHead className="w-auto">Tool</TableHead>
								<TableHead className="hidden w-32 md:table-cell">Server</TableHead>
								<TableHead className="w-10"></TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							{selectedToolsWithInfo.map((tool) => {
								const key = `${tool.mcpClientId}:${tool.toolName}`;
								const isExpanded = expandedTools.has(key);

								return (
									<Collapsible key={key} open={isExpanded} onOpenChange={() => toggleExpanded(key)} asChild>
										<>
											<TableRow className="group">
												<TableCell className="p-2">
													<CollapsibleTrigger asChild>
														<button
															type="button"
															className="hover:bg-muted flex h-8 w-8 items-center justify-center rounded-md transition-colors"
															disabled={!tool.parameters}
														>
															{tool.parameters ? (
																isExpanded ? (
																	<ChevronDown className="h-4 w-4" />
																) : (
																	<ChevronRight className="h-4 w-4" />
																)
															) : (
																<span className="h-4 w-4" />
															)}
														</button>
													</CollapsibleTrigger>
												</TableCell>
												<TableCell className="overflow-hidden">
													<div className="min-w-0 overflow-hidden">
														<div className="text-foreground truncate text-sm font-medium">{tool.toolName}</div>
														{tool.description && <p className="text-muted-foreground mt-0.5 truncate text-xs">{tool.description}</p>}
													</div>
												</TableCell>
												<TableCell className="hidden md:table-cell">
													<Badge variant="outline">{tool.mcpClientName}</Badge>
												</TableCell>
												<TableCell className="p-2">
													<Button
														type="button"
														variant="ghost"
														size="sm"
														className="h-8 w-8 p-0"
														onClick={() => handleRemoveTool(tool.mcpClientId, tool.toolName)}
														disabled={disabled}
													>
														<X className="h-4 w-4" />
													</Button>
												</TableCell>
											</TableRow>
											<CollapsibleContent asChild>
												<tr>
													<td colSpan={4} className="p-0">
														<div className="bg-muted/30 border-t px-4 py-3">
															<div className="text-muted-foreground mb-2 text-xs font-medium">Parameters Schema</div>
															{tool.parameters ? (
																<CodeEditor
																	className="z-0 w-full rounded-md border"
																	shouldAdjustInitialHeight={true}
																	maxHeight={300}
																	wrap={true}
																	code={JSON.stringify(tool.parameters, null, 2)}
																	lang="json"
																	readonly={true}
																	options={{
																		scrollBeyondLastLine: false,
																		collapsibleBlocks: true,
																		lineNumbers: "off",
																		alwaysConsumeMouseWheel: false,
																	}}
																/>
															) : (
																<div className="text-muted-foreground text-sm">No parameters defined</div>
															)}
														</div>
													</td>
												</tr>
											</CollapsibleContent>
										</>
									</Collapsible>
								);
							})}
						</TableBody>
					</Table>
				</div>
			)}

			{/* Empty state */}
			{selectedToolsWithInfo.length === 0 && (
				<div className="text-muted-foreground rounded-md border border-dashed p-4 text-center text-sm">
					No tools selected. Use the search above to add tools.
				</div>
			)}
		</div>
	);
}
