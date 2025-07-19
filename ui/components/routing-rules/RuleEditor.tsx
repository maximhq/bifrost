'use client'

import { useCallback, useMemo, useState } from 'react'
import ReactFlow, {
    Background,
    Controls,
    Handle,
    Position,
    addEdge,
    useEdgesState,
    useNodesState,
    type Connection,
    type Edge,
    type Node,
    type NodeTypes
} from 'reactflow'
import 'reactflow/dist/style.css'

import {
    RoutingAction,
    RoutingCondition,
    RoutingRule
} from '@/lib/types/routing'
import {
    AlertTriangle,
    Check,
    Filter,
    Mail,
    MessageSquare,
    Play,
    Route,
    X,
    Zap
} from 'lucide-react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '../ui/card'
import { Separator } from '../ui/separator'

interface RuleEditorProps {
  rule?: RoutingRule
  onSave: (rule: RoutingRule) => void
  onCancel: () => void
}

// Available component types for drag and drop
const COMPONENT_TYPES = [
  {
    id: 'trigger',
    type: 'trigger',
    name: 'Trigger',
    description: 'Start an automation',
    icon: Play,
    color: 'bg-blue-100 text-blue-600',
    category: 'Triggers'
  },
  {
    id: 'condition',
    type: 'condition',
    name: 'Condition',
    description: 'Add conditional logic',
    icon: AlertTriangle,
    color: 'bg-yellow-100 text-yellow-600',
    category: 'Logic'
  },
  {
    id: 'action',
    type: 'action',
    name: 'Action',
    description: 'Perform an action',
    icon: MessageSquare,
    color: 'bg-green-100 text-green-600',
    category: 'Actions'
  },
  {
    id: 'route',
    type: 'route',
    name: 'Route',
    description: 'Route to provider',
    icon: Route,
    color: 'bg-purple-100 text-purple-600',
    category: 'Actions'
  },
  {
    id: 'filter',
    type: 'filter',
    name: 'Filter',
    description: 'Filter requests',
    icon: Filter,
    color: 'bg-orange-100 text-orange-600',
    category: 'Logic'
  },
  {
    id: 'transform',
    type: 'transform',
    name: 'Transform',
    description: 'Transform data',
    icon: Zap,
    color: 'bg-indigo-100 text-indigo-600',
    category: 'Actions'
  }
]

// Node data interfaces
interface BaseNodeData {
  name: string
  description: string
  service: string
  completed: boolean
  isEditable?: boolean
  onDelete?: (nodeId: string) => void
}

interface TriggerNodeData extends BaseNodeData {
  type: 'trigger'
}

interface ConditionNodeData extends BaseNodeData {
  type: 'condition'
  condition?: RoutingCondition
}

interface ActionNodeData extends BaseNodeData {
  type: 'action'
  action?: RoutingAction
}

interface BranchActionNodeData extends BaseNodeData {
  type: 'branchAction'
  branch: string
  action?: RoutingAction
}

// Custom node components using existing theme
const TriggerNode = ({ data, id }: { data: TriggerNodeData; id: string }) => {
  const handleDelete = () => {
    if (data.onDelete) {
      data.onDelete(id)
    }
  }

  return (
    <div className="relative">
      {/* Completed tag above the step - right side */}
      <div className="absolute -top-8 right-0 z-10">
        <Badge variant={data.completed ? 'default' : 'secondary'} className="text-xs">
          {data.completed && <Check className="h-3 w-3 mr-1" />}
          {data.completed ? 'Completed' : 'Draft'}
        </Badge>
      </div>
      
      <div className="bg-card border-2 border-primary/20 rounded-lg p-4 min-w-[350px] shadow-sm relative">
        <Handle 
          type="source" 
          position={Position.Bottom} 
          className="w-3 h-3 bg-primary border-2 border-background" 
          style={{ bottom: -6 }}
        />
        
        {/* Delete button */}
        {data.isEditable && (
          <Button
            variant="ghost"
            size="icon"
            className="absolute top-2 right-2 h-6 w-6 hover:bg-destructive hover:text-destructive-foreground"
            onClick={handleDelete}
          >
            <X className="h-3 w-3" />
          </Button>
        )}
        
        <div className="flex items-start gap-3 pr-8">
          <div className="bg-blue-100 dark:bg-blue-900 rounded-lg p-2 mt-1">
            <Play className="h-4 w-4 text-blue-600 dark:text-blue-400" />
          </div>
          
          <div className="flex-1">
            <div className="flex items-center justify-between mb-1">
              <h3 className="font-medium text-sm">{data.name}</h3>
              <Badge variant="outline" className="text-xs">
                {data.service}
              </Badge>
            </div>
            <p className="text-muted-foreground text-xs">{data.description}</p>
          </div>
        </div>
      </div>
    </div>
  )
}

const ConditionNode = ({ data, id }: { data: ConditionNodeData; id: string }) => {
  const handleDelete = () => {
    if (data.onDelete) {
      data.onDelete(id)
    }
  }

  return (
    <div className="relative">
      {/* Completed tag above the step - right side */}
      <div className="absolute -top-8 right-0 z-10">
        <Badge variant={data.completed ? 'default' : 'secondary'} className="text-xs">
          {data.completed && <Check className="h-3 w-3 mr-1" />}
          {data.completed ? 'Completed' : 'Draft'}
        </Badge>
      </div>
      
      <div className="bg-card border-2 border-primary/20 rounded-lg p-4 min-w-[350px] shadow-sm relative">
        <Handle 
          type="target" 
          position={Position.Top} 
          className="w-3 h-3 bg-primary border-2 border-background" 
          style={{ top: -6 }}
        />
        <Handle 
          type="source" 
          position={Position.Bottom} 
          className="w-3 h-3 bg-primary border-2 border-background" 
          style={{ bottom: -6 }}
        />
        
        {/* Delete button */}
        {data.isEditable && (
          <Button
            variant="ghost"
            size="icon"
            className="absolute top-2 right-2 h-6 w-6 hover:bg-destructive hover:text-destructive-foreground"
            onClick={handleDelete}
          >
            <X className="h-3 w-3" />
          </Button>
        )}
        
        <div className="flex items-start gap-3 pr-8">
          <div className="bg-yellow-100 dark:bg-yellow-900 rounded-lg p-2 mt-1">
            <AlertTriangle className="h-4 w-4 text-yellow-600 dark:text-yellow-400" />
          </div>
          
          <div className="flex-1">
            <div className="flex items-center justify-between mb-1">
              <h3 className="font-medium text-sm">{data.name}</h3>
              <Badge variant="outline" className="text-xs">
                {data.service}
              </Badge>
            </div>
            <p className="text-muted-foreground text-xs">{data.description}</p>
          </div>
        </div>
      </div>
    </div>
  )
}

const ActionNode = ({ data, id }: { data: ActionNodeData; id: string }) => {
  const handleDelete = () => {
    if (data.onDelete) {
      data.onDelete(id)
    }
  }

  return (
    <div className="relative">
      {/* Completed tag above the step - right side */}
      <div className="absolute -top-8 right-0 z-10">
        <Badge variant={data.completed ? 'default' : 'secondary'} className="text-xs">
          {data.completed && <Check className="h-3 w-3 mr-1" />}
          {data.completed ? 'Completed' : 'Draft'}
        </Badge>
      </div>
      
      <div className="bg-card border-2 border-primary/20 rounded-lg p-4 min-w-[350px] shadow-sm relative">
        <Handle 
          type="target" 
          position={Position.Top} 
          className="w-3 h-3 bg-primary border-2 border-background" 
          style={{ top: -6 }}
        />
        <Handle 
          type="source" 
          position={Position.Bottom} 
          className="w-3 h-3 bg-primary border-2 border-background" 
          style={{ bottom: -6 }}
        />
        
        {/* Delete button */}
        {data.isEditable && (
          <Button
            variant="ghost"
            size="icon"
            className="absolute top-2 right-2 h-6 w-6 hover:bg-destructive hover:text-destructive-foreground"
            onClick={handleDelete}
          >
            <X className="h-3 w-3" />
          </Button>
        )}
        
        <div className="flex items-start gap-3 pr-8">
          <div className="bg-green-100 dark:bg-green-900 rounded-lg p-2 mt-1">
            <MessageSquare className="h-4 w-4 text-green-600 dark:text-green-400" />
          </div>
          
          <div className="flex-1">
            <div className="flex items-center justify-between mb-1">
              <h3 className="font-medium text-sm">{data.name}</h3>
              <Badge variant="outline" className="text-xs">
                {data.service}
              </Badge>
            </div>
            <p className="text-muted-foreground text-xs">{data.description}</p>
          </div>
        </div>
      </div>
    </div>
  )
}

const BranchActionNode = ({ data, id }: { data: BranchActionNodeData; id: string }) => {
  const handleDelete = () => {
    if (data.onDelete) {
      data.onDelete(id)
    }
  }

  return (
    <div className="relative">
      {/* Completed tag above the step - right side */}
      <div className="absolute -top-8 right-0 z-10">
        <Badge variant={data.completed ? 'default' : 'secondary'} className="text-xs">
          {data.completed && <Check className="h-3 w-3 mr-1" />}
          {data.completed ? 'Completed' : 'Draft'}
        </Badge>
      </div>
      
      <div className="bg-card border-2 border-primary/20 rounded-lg p-4 min-w-[350px] shadow-sm relative">
        <Handle 
          type="target" 
          position={Position.Top} 
          className="w-3 h-3 bg-primary border-2 border-background" 
          style={{ top: -6 }}
        />
        <Handle 
          type="source" 
          position={Position.Bottom} 
          className="w-3 h-3 bg-primary border-2 border-background" 
          style={{ bottom: -6 }}
        />
        
        {/* Delete button */}
        {data.isEditable && (
          <Button
            variant="ghost"
            size="icon"
            className="absolute top-2 right-2 h-6 w-6 hover:bg-destructive hover:text-destructive-foreground"
            onClick={handleDelete}
          >
            <X className="h-3 w-3" />
          </Button>
        )}
        
        <div className="flex items-start gap-3 pr-8">
          <div className="bg-purple-100 dark:bg-purple-900 rounded-lg p-2 mt-1">
            <Mail className="h-4 w-4 text-purple-600 dark:text-purple-400" />
          </div>
          
          <div className="flex-1">
            <div className="flex items-center justify-between mb-1">
              <h3 className="font-medium text-sm">{data.name}</h3>
              <Badge variant="outline" className="text-xs">
                {data.service}
              </Badge>
            </div>
            <p className="text-muted-foreground text-xs">{data.description}</p>
          </div>
        </div>
      </div>
    </div>
  )
}

const nodeTypes: NodeTypes = {
  trigger: TriggerNode,
  condition: ConditionNode,
  action: ActionNode,
  branchAction: BranchActionNode,
}

const customEdgeStyle = {
  stroke: 'hsl(var(--primary))',
  strokeWidth: 2,
}

// Component sidebar for drag and drop
const ComponentSidebar = ({ onDragStart }: { onDragStart: (type: string) => void }) => {
  const groupedComponents = useMemo(() => {
    const groups: Record<string, typeof COMPONENT_TYPES> = {}
    COMPONENT_TYPES.forEach(component => {
      if (!groups[component.category]) {
        groups[component.category] = []
      }
      groups[component.category].push(component)
    })
    return groups
  }, [])

  return (
    <Card className="w-64 h-full">
      <CardHeader className="pb-4">
        <CardTitle className="text-lg">Components</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {Object.entries(groupedComponents).map(([category, components]) => (
          <div key={category}>
            <h4 className="text-sm font-medium text-muted-foreground mb-2">{category}</h4>
            <div className="space-y-2">
              {components.map(component => (
                <div
                  key={component.id}
                  draggable
                  onDragStart={() => onDragStart(component.type)}
                  className="flex items-center gap-2 p-2 rounded-lg border cursor-move hover:bg-accent transition-colors"
                >
                  <div className={`rounded p-1 ${component.color}`}>
                    <component.icon className="h-3 w-3" />
                  </div>
                  <div className="flex-1">
                    <div className="text-xs font-medium">{component.name}</div>
                    <div className="text-xs text-muted-foreground">{component.description}</div>
                  </div>
                </div>
              ))}
            </div>
            <Separator className="my-3" />
          </div>
        ))}
      </CardContent>
    </Card>
  )
}

let nodeId = 0
const getNodeId = () => `node_${nodeId++}`

export default function RuleEditor({ rule, onSave, onCancel }: RuleEditorProps) {
  const [draggedNodeType, setDraggedNodeType] = useState<string | null>(null)

  const onNodeDelete = useCallback((nodeId: string) => {
    setNodes((nds) => nds.filter(node => node.id !== nodeId))
    setEdges((eds) => eds.filter(edge => edge.source !== nodeId && edge.target !== nodeId))
  }, [])

  // Create nodes matching the Attio flow but with theme colors
  const initialNodes: Node[] = useMemo(() => {
    const nodes: Node[] = []
    
    // Trigger node
    nodes.push({
      id: 'trigger',
      type: 'trigger',
      position: { x: 400, y: 80 }, // Added more space for tag above
      data: { 
        name: 'When request received',
        description: 'Trigger when an API request is received',
        service: 'Bifrost',
        completed: true,
        type: 'trigger'
      } as TriggerNodeData,
      sourcePosition: Position.Bottom,
    })

    // Condition node
    nodes.push({
      id: 'condition',
      type: 'condition',
      position: { x: 400, y: 220 }, // Added more space for tag above
      data: {
        name: 'Is header "x-bf-team"?',
        description: 'Check if request header matches condition',
        service: 'Condition',
        completed: true,
        type: 'condition'
      } as ConditionNodeData,
      sourcePosition: Position.Bottom,
      targetPosition: Position.Top,
    })

    // Action node
    nodes.push({
      id: 'action',
      type: 'action',
      position: { x: 400, y: 360 }, // Added more space for tag above
      data: {
        name: 'Route to AI provider',
        description: 'Route request to the appropriate AI provider',
        service: 'Bifrost',
        completed: true,
        type: 'action'
      } as ActionNodeData,
      sourcePosition: Position.Bottom,
      targetPosition: Position.Top,
    })

    // Branch actions
    nodes.push({
      id: 'branch-left',
      type: 'branchAction',
      position: { x: 100, y: 500 }, // Added more space for tag above
      data: {
        name: 'Route to OpenAI',
        description: 'Route request to OpenAI provider',
        service: 'OpenAI',
        completed: true,
        type: 'branchAction',
        branch: 'Enterprise lead'
      } as BranchActionNodeData,
      targetPosition: Position.Top,
    })

    nodes.push({
      id: 'branch-right',
      type: 'branchAction',
      position: { x: 700, y: 500 }, // Added more space for tag above
      data: {
        name: 'Route to Anthropic',
        description: 'Route request to Anthropic provider',
        service: 'Anthropic',
        completed: false,
        type: 'branchAction',
        branch: 'SMB lead'
      } as BranchActionNodeData,
      targetPosition: Position.Top,
    })

    return nodes
  }, [])

  const initialEdges: Edge[] = useMemo(() => {
    const edges: Edge[] = []
    
    // Main flow connections
    edges.push({
      id: 'trigger-to-condition',
      source: 'trigger',
      target: 'condition',
      style: customEdgeStyle,
      type: 'smoothstep',
    })

    edges.push({
      id: 'condition-to-action',
      source: 'condition',
      target: 'action',
      style: customEdgeStyle,
      type: 'smoothstep',
    })

    // Branch connections
    edges.push({
      id: 'action-to-branch-left',
      source: 'action',
      target: 'branch-left',
      style: customEdgeStyle,
      type: 'smoothstep',
      label: 'Enterprise team',
      labelStyle: { fill: 'hsl(var(--primary))', fontWeight: 500 },
      labelBgStyle: { fill: 'hsl(var(--background))', fillOpacity: 0.8 },
    })

    edges.push({
      id: 'action-to-branch-right',
      source: 'action',
      target: 'branch-right',
      style: customEdgeStyle,
      type: 'smoothstep',
      label: 'SMB team',
      labelStyle: { fill: 'hsl(var(--primary))', fontWeight: 500 },
      labelBgStyle: { fill: 'hsl(var(--background))', fillOpacity: 0.8 },
    })

    return edges
  }, [])

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes)
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges)

  const onConnect = useCallback(
    (params: Connection) => setEdges((eds) => addEdge(params, eds)),
    [setEdges]
  )

  const onDragStart = useCallback((type: string) => {
    setDraggedNodeType(type)
  }, [])

  const onDragOver = useCallback((event: React.DragEvent) => {
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }, [])

  const onDrop = useCallback((event: React.DragEvent) => {
    event.preventDefault()

    if (!draggedNodeType) return

    const reactFlowBounds = (event.target as HTMLElement).getBoundingClientRect()
    const position = {
      x: event.clientX - reactFlowBounds.left - 175, // Center the node
      y: event.clientY - reactFlowBounds.top - 60, // Account for tag above
    }

    const newNode: Node = {
      id: getNodeId(),
      type: draggedNodeType,
      position,
      data: {
        name: `New ${draggedNodeType}`,
        description: `Configure this ${draggedNodeType} component`,
        service: 'Bifrost',
        completed: false,
        type: draggedNodeType,
        isEditable: true,
        onDelete: onNodeDelete
      },
      sourcePosition: Position.Bottom,
      targetPosition: Position.Top,
    }

    setNodes((nds) => nds.concat(newNode))
    setDraggedNodeType(null)
  }, [draggedNodeType, setNodes, onNodeDelete])

  const handleSave = useCallback(() => {
    // Create a sample rule from the visual flow
    const newRule: RoutingRule = {
      id: rule?.id || crypto.randomUUID(),
      name: 'Team-based routing',
      description: 'Route requests based on x-bf-team header',
      enabled: true,
      priority: 1,
      condition_groups: [{
        id: 'condition-group-1',
        operator: 'AND',
        conditions: [{
          id: 'condition-1',
          type: 'header',
          field: 'x-bf-team',
          operator: 'equals',
          value: 'engineering'
        }]
      }],
      group_operator: 'AND',
      actions: [{
        id: 'action-1',
        type: 'route_to_provider',
        provider: 'openai',
        priority: 1
      }],
      created_at: rule?.created_at || new Date().toISOString(),
      updated_at: new Date().toISOString()
    }

    onSave(newRule)
  }, [rule, onSave])

  // Create enhanced node types with delete functionality
  const enhancedNodeTypes: NodeTypes = useMemo(() => ({
    trigger: (props) => <TriggerNode {...props} />,
    condition: (props) => <ConditionNode {...props} />,
    action: (props) => <ActionNode {...props} />,
    branchAction: (props) => <BranchActionNode {...props} />,
  }), [])

  return (
    <div className="flex h-[800px] w-full gap-4">
      {/* Component Sidebar */}
      <ComponentSidebar onDragStart={onDragStart} />
      
      {/* Main Flow Editor */}
      <div className="flex-1 bg-muted/30 rounded-lg overflow-hidden">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onConnect={onConnect}
          onDragOver={onDragOver}
          onDrop={onDrop}
          nodeTypes={enhancedNodeTypes}
          defaultViewport={{ x: 0, y: 0, zoom: 0.8 }}
          minZoom={0.3}
          maxZoom={1.5}
          fitView
          nodesDraggable={true}
          nodesConnectable={true}
          elementsSelectable={true}
          connectOnClick={false}
          snapToGrid={true}
          snapGrid={[20, 20]}
          proOptions={{ hideAttribution: true }}
          className="bg-background"
        >
          <Background color="hsl(var(--muted))" />
          <Controls className="bg-background border-border" />
        </ReactFlow>
      </div>
      
      {/* Action buttons */}
      <div className="absolute bottom-4 right-4 flex gap-2">
        <Button variant="outline" onClick={onCancel}>
          Cancel
        </Button>
        <Button onClick={handleSave}>
          Save Rule
        </Button>
      </div>
    </div>
  )
}
