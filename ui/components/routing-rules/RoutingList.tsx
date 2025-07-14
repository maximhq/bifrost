'use client'

import { useToast } from '@/hooks/use-toast'
import { apiService } from '@/lib/api'
import { RoutingRule } from '@/lib/types/routing'
import { Edit, Plus, Trash2 } from 'lucide-react'
import { useRouter } from 'next/navigation'
import { useEffect, useState } from 'react'
import { Badge } from '../ui/badge'
import { Button } from '../ui/button'
import { CardDescription, CardHeader, CardTitle } from '../ui/card'
import { Switch } from '../ui/switch'
import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow
} from '../ui/table'

export default function RoutingList() {
  const router = useRouter()
  const { toast } = useToast()
  const [rules, setRules] = useState<RoutingRule[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    loadRules()
  }, [])

  const loadRules = async () => {
    try {
      setLoading(true)
      const [data, error] = await apiService.getRoutingRules()
      if (error) {
        toast({
          title: "Error",
          description: error,
          variant: "destructive",
        })
        return
      }
      setRules(data?.rules || [])
    } catch (error) {
      toast({
        title: "Error",
        description: "Failed to load routing rules",
        variant: "destructive",
      })
    } finally {
      setLoading(false)
    }
  }

  const handleAddRoutingRule = () => {
    router.push('/routing/new')
  }

  const handleEditRule = (rule: RoutingRule) => {
    router.push(`/routing/edit/${rule.id}`)
  }

  const handleToggleRule = async (rule: RoutingRule) => {
    try {
      const updatedRule = { ...rule, enabled: !rule.enabled }
      const [data, error] = await apiService.updateRoutingRule(rule.id, updatedRule)
      if (error) {
        toast({
          title: "Error",
          description: error,
          variant: "destructive",
        })
        return
      }
      
      setRules(rules.map(r => r.id === rule.id ? { ...r, enabled: !r.enabled } : r))
      toast({
        title: "Success",
        description: `Rule ${rule.enabled ? 'disabled' : 'enabled'} successfully`,
      })
    } catch (error) {
      toast({
        title: "Error",
        description: "Failed to update rule",
        variant: "destructive",
      })
    }
  }

  const handleDeleteRule = async (rule: RoutingRule) => {
    if (!confirm(`Are you sure you want to delete the rule "${rule.name}"?`)) {
      return
    }

    try {
      const [data, error] = await apiService.deleteRoutingRule(rule.id)
      if (error) {
        toast({
          title: "Error",
          description: error,
          variant: "destructive",
        })
        return
      }
      
      setRules(rules.filter(r => r.id !== rule.id))
      toast({
        title: "Success",
        description: "Rule deleted successfully",
      })
    } catch (error) {
      toast({
        title: "Error",
        description: "Failed to delete rule",
        variant: "destructive",
      })
    }
  }

  const getConditionSummary = (rule: RoutingRule) => {
    const totalConditions = rule.condition_groups.reduce((sum, group) => sum + group.conditions.length, 0)
    return `${totalConditions} condition${totalConditions !== 1 ? 's' : ''}`
  }

  const getActionSummary = (rule: RoutingRule) => {
    const actions = rule.actions
    if (actions.length === 0) return 'No actions'
    
    const actionTypes = actions.map(a => {
      switch (a.type) {
        case 'route_to_provider':
          return `Route to ${a.provider}`
        case 'route_to_model':
          return `Route to ${a.model}`
        case 'set_header':
          return 'Set header'
        case 'reject':
          return 'Reject'
        default:
          return 'Unknown'
      }
    })
    
    return actionTypes.join(', ')
  }

  if (loading) {
    return (
      <>
        <CardHeader className="mb-4 px-0">
          <CardTitle className="flex items-center justify-between">
            <div className="flex items-center gap-2">Routing rules</div>
            <Button disabled>
              <Plus className="h-4 w-4" />
              Add New Rule
            </Button>
          </CardTitle>
          <CardDescription className="-mt-1">Loading routing rules...</CardDescription>
        </CardHeader>
        <div className="space-y-4">
          {[...Array(3)].map((_, i) => (
            <div key={i} className="h-16 bg-gray-100 rounded-lg animate-pulse" />
          ))}
        </div>
      </>
    )
  }

  return (
    <>
      <CardHeader className="mb-4 px-0">
        <CardTitle className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            Routing rules
            <Badge variant="secondary">{rules.length}</Badge>
          </div>
          <Button onClick={handleAddRoutingRule}>
            <Plus className="h-4 w-4" />
            Add New Rule
          </Button>
        </CardTitle>
        <CardDescription className="-mt-1">
          Manage routing rules to direct requests to appropriate AI providers based on headers and other criteria.
        </CardDescription>
      </CardHeader>

      {rules.length === 0 ? (
        <div className="text-center py-12">
          <div className="text-gray-500 mb-4">
            No routing rules configured yet.
          </div>
          <Button onClick={handleAddRoutingRule} variant="outline">
            <Plus className="h-4 w-4 mr-2" />
            Create your first rule
          </Button>
        </div>
      ) : (
        <div className="border rounded-lg">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Priority</TableHead>
                <TableHead>Conditions</TableHead>
                <TableHead>Actions</TableHead>
                <TableHead>Updated</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rules.map((rule) => (
                <TableRow key={rule.id}>
                  <TableCell className="font-medium">
                    <div>
                      <div className="font-medium">{rule.name}</div>
                      {rule.description && (
                        <div className="text-sm text-gray-500">{rule.description}</div>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="flex items-center gap-2">
                      <Switch
                        checked={rule.enabled}
                        onCheckedChange={() => handleToggleRule(rule)}
                      />
                      <Badge variant={rule.enabled ? "default" : "secondary"}>
                        {rule.enabled ? 'Enabled' : 'Disabled'}
                      </Badge>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{rule.priority}</Badge>
                  </TableCell>
                  <TableCell>
                    <div className="text-sm text-gray-600">
                      {getConditionSummary(rule)}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="text-sm text-gray-600">
                      {getActionSummary(rule)}
                    </div>
                  </TableCell>
                  <TableCell>
                    <div className="text-sm text-gray-500">
                      {new Date(rule.updated_at).toLocaleDateString()}
                    </div>
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex items-center gap-2 justify-end">
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleEditRule(rule)}
                      >
                        <Edit className="h-4 w-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => handleDeleteRule(rule)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </>
  )
}
