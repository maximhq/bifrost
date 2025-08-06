'use client'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { apiService } from '@/lib/api'
import { Team, Customer, VirtualKey } from '@/lib/types/governance'
import { Edit, Users, Plus, Trash2, DollarSign, Key, User } from 'lucide-react'
import { useState } from 'react'
import { toast } from 'sonner'
import TeamDialog from './team-dialog'
import { formatCurrency, parseResetPeriod } from '@/lib/utils/governance'
import { cn } from '@/lib/utils'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'

interface TeamsTableProps {
  teams: Team[]
  customers: Customer[]
  virtualKeys: VirtualKey[]
  onRefresh: () => void
}

export default function TeamsTable({ teams, customers, virtualKeys, onRefresh }: TeamsTableProps) {
  const [showTeamDialog, setShowTeamDialog] = useState(false)
  const [editingTeam, setEditingTeam] = useState<Team | null>(null)
  const [deletingTeamId, setDeletingTeamId] = useState<string | null>(null)

  const handleDelete = async (teamId: string) => {
    setDeletingTeamId(teamId)
    const [, error] = await apiService.deleteTeam(teamId)
    setDeletingTeamId(null)

    if (error) {
      toast.error(error)
    } else {
      toast.success('Team deleted successfully')
      onRefresh()
    }
  }

  const handleAddTeam = () => {
    setEditingTeam(null)
    setShowTeamDialog(true)
  }

  const handleEditTeam = (team: Team) => {
    setEditingTeam(team)
    setShowTeamDialog(true)
  }

  const handleTeamSaved = () => {
    setShowTeamDialog(false)
    setEditingTeam(null)
    onRefresh()
  }

  const getVirtualKeysForTeam = (teamId: string) => {
    return virtualKeys.filter((vk) => vk.team_id === teamId)
  }

  const getCustomerName = (customerId?: string) => {
    if (!customerId) return '-'
    const customer = customers.find((c) => c.id === customerId)
    return customer ? customer.name : 'Unknown Customer'
  }

  return (
    <>
      {showTeamDialog && (
        <TeamDialog team={editingTeam} customers={customers} onSave={handleTeamSaved} onCancel={() => setShowTeamDialog(false)} />
      )}

      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-lg font-semibold">Teams</h3>
            <p className="text-muted-foreground text-sm">Organize users into teams with shared budgets and access controls.</p>
          </div>
          <Button onClick={handleAddTeam}>
            <Plus className="h-4 w-4" />
            Add Team
          </Button>
        </div>

        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Customer</TableHead>
                <TableHead>Max Budget</TableHead>
                <TableHead>Reset Period</TableHead>
                <TableHead>Virtual Keys</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {teams.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={6} className="text-muted-foreground py-8 text-center">
                    No teams found. Create your first team to get started.
                  </TableCell>
                </TableRow>
              ) : (
                teams.map((team) => {
                  const vks = getVirtualKeysForTeam(team.id)
                  const customerName = getCustomerName(team.customer_id)

                  return (
                    <TableRow key={team.id}>
                      <TableCell className="max-w-[200px]">
                        <div className="font-medium truncate">{team.name}</div>
                      </TableCell>
                      <TableCell>
                        <div className="flex items-center gap-2">
                          <Badge variant={team.customer_id ? 'secondary' : 'outline'}>{customerName}</Badge>
                        </div>
                      </TableCell>
                      <TableCell>
                        {team.budget ? (
                          <span
                            className={cn('font-mono text-sm', team.budget.current_usage >= team.budget.max_limit && 'text-destructive')}
                          >
                            {formatCurrency(team.budget.current_usage)} / {formatCurrency(team.budget.max_limit)}
                          </span>
                        ) : (
                          <span className="text-muted-foreground text-sm">-</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {team.budget ? (
                          parseResetPeriod(team.budget.reset_duration)
                        ) : (
                          <span className="text-muted-foreground text-sm">-</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {vks.length > 0 ? (
                          <div className="flex items-center gap-2">
                            <Key className="h-4 w-4" />
                            <TooltipProvider>
                              <Tooltip>
                                <TooltipTrigger>
                                  <Badge variant="outline" className="text-xs">
                                    {vks.length} {vks.length === 1 ? 'key' : 'keys'}
                                  </Badge>
                                </TooltipTrigger>
                                <TooltipContent>{vks.map((vk) => vk.name).join(', ')}</TooltipContent>
                              </Tooltip>
                            </TooltipProvider>
                          </div>
                        ) : (
                          <span className="text-muted-foreground text-sm">-</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-2">
                          <Button variant="ghost" size="sm" onClick={() => handleEditTeam(team)}>
                            <Edit className="h-4 w-4" />
                          </Button>
                          <AlertDialog>
                            <AlertDialogTrigger asChild>
                              <Button variant="ghost" size="sm">
                                <Trash2 className="h-4 w-4" />
                              </Button>
                            </AlertDialogTrigger>
                            <AlertDialogContent>
                              <AlertDialogHeader>
                                <AlertDialogTitle>Delete Team</AlertDialogTitle>
                                <AlertDialogDescription>
                                  Are you sure you want to delete "{team.name}"? This will also unassign any virtual keys from this team.
                                  This action cannot be undone.
                                </AlertDialogDescription>
                              </AlertDialogHeader>
                              <AlertDialogFooter>
                                <AlertDialogCancel>Cancel</AlertDialogCancel>
                                <AlertDialogAction onClick={() => handleDelete(team.id)} disabled={deletingTeamId === team.id}>
                                  {deletingTeamId === team.id ? 'Deleting...' : 'Delete'}
                                </AlertDialogAction>
                              </AlertDialogFooter>
                            </AlertDialogContent>
                          </AlertDialog>
                        </div>
                      </TableCell>
                    </TableRow>
                  )
                })
              )}
            </TableBody>
          </Table>
        </div>
      </div>
    </>
  )
}
