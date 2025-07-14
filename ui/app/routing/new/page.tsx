'use client'

import RuleEditor from '@/components/routing-rules/RuleEditor'
import { Breadcrumb, BreadcrumbItem, BreadcrumbLink, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from '@/components/ui/breadcrumb'
import { CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useToast } from '@/hooks/use-toast'
import { apiService } from '@/lib/api'
import { RoutingRule } from '@/lib/types/routing'
import Link from 'next/link'
import { useRouter } from 'next/navigation'

export default function NewRoutingPage() {
  const router = useRouter()
  const { toast } = useToast()

  const handleSave = async (rule: RoutingRule) => {
    try {
      const createRequest = {
        name: rule.name,
        description: rule.description,
        enabled: rule.enabled,
        priority: rule.priority,
        condition_groups: rule.condition_groups,
        group_operator: rule.group_operator,
        actions: rule.actions
      }

      const [data, error] = await apiService.createRoutingRule(createRequest)
      
      if (error) {
        toast({
          title: "Error",
          description: error,
          variant: "destructive",
        })
        return
      }
      
      toast({
        title: "Rule created",
        description: `Routing rule "${rule.name}" has been created successfully.`,
      })
      
      router.push('/routing')
    } catch (error) {
      toast({
        title: "Error",
        description: "Failed to create routing rule. Please try again.",
        variant: "destructive",
      })
    }
  }

  const handleCancel = () => {
    router.push('/routing')
  }

  return (
    <>
      <CardHeader className="mb-4 px-0">
        <CardTitle className="flex items-center justify-between">
          <Breadcrumb className="mt-2">
            <BreadcrumbList>
              <BreadcrumbItem>
                <BreadcrumbLink asChild>
                  <Link href="/routing">Routing rules</Link>
                </BreadcrumbLink>
              </BreadcrumbItem>
              <BreadcrumbSeparator />
              <BreadcrumbItem>
                <BreadcrumbPage>
                  New Rule
                </BreadcrumbPage>
              </BreadcrumbItem>
            </BreadcrumbList>
          </Breadcrumb>
        </CardTitle>
        <CardDescription className="mt-1">
          Create a new routing rule to direct requests to specific providers based on headers, paths, and other criteria.
        </CardDescription>
      </CardHeader>
      
      <RuleEditor onSave={handleSave} onCancel={handleCancel} />
    </>
  )
}
