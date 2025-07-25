'use client'

import { Card, CardContent } from '@/components/ui/card'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import GradientHeader from '@/components/ui/gradient-header'
import { Shield, Users, User, Key, FileText } from 'lucide-react'
import { useState, useEffect } from 'react'
import { toast } from 'sonner'
import { apiService } from '@/lib/api'
import { VirtualKey, Team, Customer, AuditLog } from '@/lib/types/governance'
import VirtualKeysTable from '@/components/governance/virtual-keys-table'
import TeamsTable from '@/components/governance/teams-table'
import CustomersTable from '@/components/governance/customers-table'
import AuditLogsSection from '@/components/governance/audit-logs-section'
import FullPageLoader from '@/components/full-page-loader'
export default function GovernancePage() {
  const [activeTab, setActiveTab] = useState('virtual-keys')
  const [virtualKeys, setVirtualKeys] = useState<VirtualKey[]>([])
  const [teams, setTeams] = useState<Team[]>([])
  const [customers, setCustomers] = useState<Customer[]>([])
  const [auditLogs, setAuditLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)

  const fetchData = async () => {
    setLoading(true)
    try {
      const [vkResult, teamsResult, customersResult, auditResult] = await Promise.all([
        apiService.getVirtualKeys(),
        apiService.getTeams(),
        apiService.getCustomers(),
        apiService.getAuditLogs({ limit: 50, offset: 0 }),
      ])

      if (vkResult[1]) {
        toast.error(`Failed to load virtual keys: ${vkResult[1]}`)
      } else if (vkResult[0]) {
        setVirtualKeys(vkResult[0].virtual_keys)
      }

      if (teamsResult[1]) {
        toast.error(`Failed to load teams: ${teamsResult[1]}`)
      } else if (teamsResult[0]) {
        setTeams(teamsResult[0].teams)
      }

      if (customersResult[1]) {
        toast.error(`Failed to load customers: ${customersResult[1]}`)
      } else if (customersResult[0]) {
        setCustomers(customersResult[0].customers)
      }

      if (auditResult[1]) {
        toast.error(`Failed to load audit logs: ${auditResult[1]}`)
      } else if (auditResult[0]) {
        setAuditLogs(auditResult[0].audit_logs)
      }
    } catch (error) {
      toast.error('Failed to load governance data')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    fetchData()
  }, [])

  const handleRefresh = () => {
    fetchData()
  }

  return loading ? (
    <FullPageLoader />
  ) : (
    <div className="">
      <div>
        <h1 className="mb-2 text-3xl font-bold">Governance</h1>
        <p className="text-muted-foreground">Manage virtual keys, teams, customers, budgets, and rate limits</p>
      </div>

      <div className="mt-8">
        <Tabs value={activeTab} onValueChange={setActiveTab} className="w-full">
          <TabsList className="mb-4 grid h-12 w-full grid-cols-3">
            {['virtual-keys', 'teams', 'customers'].map((tab) => (
              <TabsTrigger key={tab} value={tab} className="flex items-center gap-2 capitalize transition-all duration-200 ease-in-out">
                {tab.replace('-', ' ')}
              </TabsTrigger>
            ))}
          </TabsList>

          <div className="">
            <TabsContent value="virtual-keys" className="mt-0">
              <VirtualKeysTable virtualKeys={virtualKeys} teams={teams} customers={customers} onRefresh={handleRefresh} />
            </TabsContent>

            <TabsContent value="teams" className="mt-0">
              <TeamsTable teams={teams} customers={customers} virtualKeys={virtualKeys} onRefresh={handleRefresh} />
            </TabsContent>

            <TabsContent value="customers" className="mt-0">
              <CustomersTable customers={customers} teams={teams} virtualKeys={virtualKeys} onRefresh={handleRefresh} />
            </TabsContent>

            <TabsContent value="audit-logs" className="mt-0">
              <AuditLogsSection auditLogs={auditLogs} onRefresh={handleRefresh} loading={loading} />
            </TabsContent>
          </div>
        </Tabs>
      </div>
    </div>
  )
}
