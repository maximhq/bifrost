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
import { Customer, Team, VirtualKey } from '@/lib/types/governance'
import { Edit, Plus, Trash2, DollarSign, Key, Users } from 'lucide-react'
import { useState } from 'react'
import { toast } from 'sonner'
import CustomerDialog from './customer-dialog'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { formatCurrency } from '@/lib/utils/governance'

interface CustomersTableProps {
  customers: Customer[]
  teams: Team[]
  virtualKeys: VirtualKey[]
  onRefresh: () => void
}

export default function CustomersTable({ customers, teams, virtualKeys, onRefresh }: CustomersTableProps) {
  const [showCustomerDialog, setShowCustomerDialog] = useState(false)
  const [editingCustomer, setEditingCustomer] = useState<Customer | null>(null)
  const [deletingCustomerId, setDeletingCustomerId] = useState<string | null>(null)

  const handleDelete = async (customerId: string) => {
    setDeletingCustomerId(customerId)
    const [, error] = await apiService.deleteCustomer(customerId)
    setDeletingCustomerId(null)

    if (error) {
      toast.error(error)
    } else {
      toast.success('Customer deleted successfully')
      onRefresh()
    }
  }

  const handleAddCustomer = () => {
    setEditingCustomer(null)
    setShowCustomerDialog(true)
  }

  const handleEditCustomer = (customer: Customer) => {
    setEditingCustomer(customer)
    setShowCustomerDialog(true)
  }

  const handleCustomerSaved = () => {
    setShowCustomerDialog(false)
    setEditingCustomer(null)
    onRefresh()
  }

  const getTeamsForCustomer = (customerId: string) => {
    return teams.filter((team) => team.customer_id === customerId)
  }

  const getVirtualKeysForCustomer = (customerId: string) => {
    return virtualKeys.filter((vk) => vk.customer_id === customerId)
  }

  return (
    <>
      {showCustomerDialog && (
        <CustomerDialog customer={editingCustomer} onSave={handleCustomerSaved} onCancel={() => setShowCustomerDialog(false)} />
      )}

      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-lg font-semibold">Customers</h3>
            <p className="text-muted-foreground text-sm">Manage customer accounts with their own teams, budgets, and access controls.</p>
          </div>
          <Button onClick={handleAddCustomer}>
            <Plus className="h-4 w-4" />
            Add Customer
          </Button>
        </div>

        <div className="rounded-md border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Teams</TableHead>
                <TableHead>Budget</TableHead>
                <TableHead>Virtual Keys</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {customers.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground py-8 text-center">
                    No customers found. Create your first customer to get started.
                  </TableCell>
                </TableRow>
              ) : (
                customers.map((customer) => {
                  const teams = getTeamsForCustomer(customer.id)
                  const vks = getVirtualKeysForCustomer(customer.id)

                  return (
                    <TableRow key={customer.id}>
                      <TableCell>
                        <div className="font-medium">{customer.name}</div>
                      </TableCell>
                      <TableCell>
                        {teams.length > 0 ? (
                          <div className="flex items-center gap-2">
                            <Users className="h-4 w-4" />
                            <TooltipProvider>
                              <Tooltip>
                                <TooltipTrigger>
                                  <Badge variant="outline" className="text-xs">
                                    {teams.length} {teams.length === 1 ? 'team' : 'teams'}
                                  </Badge>
                                </TooltipTrigger>
                                <TooltipContent>{teams.map((team) => team.name).join(', ')}</TooltipContent>
                              </Tooltip>
                            </TooltipProvider>
                          </div>
                        ) : (
                          <span className="text-muted-foreground text-sm">-</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {customer.budget ? (
                          <div className="flex items-center gap-1">
                            <DollarSign className="h-3 w-3" />
                            <span className="text-sm">
                              {formatCurrency(customer.budget.current_usage)} / {formatCurrency(customer.budget.max_limit)}
                            </span>
                            <Badge
                              variant={customer.budget.current_usage >= customer.budget.max_limit ? 'destructive' : 'secondary'}
                              className="text-xs"
                            >
                              {customer.budget.reset_duration}
                            </Badge>
                          </div>
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
                          <Button variant="ghost" size="sm" onClick={() => handleEditCustomer(customer)}>
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
                                <AlertDialogTitle>Delete Customer</AlertDialogTitle>
                                <AlertDialogDescription>
                                  Are you sure you want to delete "{customer.name}"? This will also delete all associated teams and unassign
                                  any virtual keys. This action cannot be undone.
                                </AlertDialogDescription>
                              </AlertDialogHeader>
                              <AlertDialogFooter>
                                <AlertDialogCancel>Cancel</AlertDialogCancel>
                                <AlertDialogAction onClick={() => handleDelete(customer.id)} disabled={deletingCustomerId === customer.id}>
                                  {deletingCustomerId === customer.id ? 'Deleting...' : 'Delete'}
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
