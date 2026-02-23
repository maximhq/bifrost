"use client"

import FullPageLoader from "@/components/fullPageLoader"
import { IS_ENTERPRISE } from "@/lib/constants/config"
import {
  getErrorMessage,
  setSelectedPlugin,
  useAppDispatch,
  useAppSelector,
  useCreatePluginMutation,
  useDeletePluginMutation,
  useGetPluginsQuery,
  useUpdatePluginMutation,
} from "@/lib/store"
import { cn } from "@/lib/utils"
import { useTheme } from "next-themes"
import Image from "next/image"
import { useQueryState } from "nuqs"
import { useCallback, useEffect, useMemo } from "react"
import { toast } from "sonner"
import DatadogView from "./plugins/datadogView"
import MaximView from "./plugins/maximView"
import OtelView from "./plugins/otelView"
import PrometheusView from "./plugins/prometheusView"

type SupportedPlatform = {
  id: string
  name: string
  icon: React.ReactNode
  tag?: string
  disabled?: boolean
}

const supportedPlatformsList = (resolvedTheme: string): SupportedPlatform[] => [
  {
    id: "otel",
    name: "Open Telemetry",
    icon: (
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 128 128" width={21} height={21}>
        <path
          fill="#f5a800"
          d="M67.648 69.797c-5.246 5.25-5.246 13.758 0 19.008 5.25 5.246 13.758 5.246 19.004 0 5.25-5.25 5.25-13.758 0-19.008-5.246-5.246-13.754-5.246-19.004 0Zm14.207 14.219a6.649 6.649 0 0 1-9.41 0 6.65 6.65 0 0 1 0-9.407 6.649 6.649 0 0 1 9.41 0c2.598 2.586 2.598 6.809 0 9.407ZM86.43 3.672l-8.235 8.234a4.17 4.17 0 0 0 0 5.875l32.149 32.149a4.17 4.17 0 0 0 5.875 0l8.234-8.235c1.61-1.61 1.61-4.261 0-5.87L92.29 3.671a4.159 4.159 0 0 0-5.86 0ZM28.738 108.895a3.763 3.763 0 0 0 0-5.31l-4.183-4.187a3.768 3.768 0 0 0-5.313 0l-8.644 8.649-.016.012-2.371-2.375c-1.313-1.313-3.45-1.313-4.75 0-1.313 1.312-1.313 3.449 0 4.75l14.246 14.242a3.353 3.353 0 0 0 4.746 0c1.3-1.313 1.313-3.45 0-4.746l-2.375-2.375.016-.012Zm0 0"
        />
        <path
          fill="#425cc7"
          d="M72.297 27.313 54.004 45.605c-1.625 1.625-1.625 4.301 0 5.926L65.3 62.824c7.984-5.746 19.18-5.035 26.363 2.153l9.148-9.149c1.622-1.625 1.622-4.297 0-5.922L78.22 27.313a4.185 4.185 0 0 0-5.922 0ZM60.55 67.585l-6.672-6.672c-1.563-1.562-4.125-1.562-5.684 0l-23.53 23.54a4.036 4.036 0 0 0 0 5.687l13.331 13.332a4.036 4.036 0 0 0 5.688 0l15.132-15.157c-3.199-6.609-2.625-14.593 1.735-20.73Zm0 0"
        />
      </svg>
    ),
  },
  {
    id: "prometheus",
    name: "Prometheus",
    icon: <Image alt="Prometheus" src="/images/prometheus-logo.svg" width={21} height={21} className="-ml-0.5" />,
  },
  {
    id: "maxim",
    name: "Maxim",
    icon: <Image alt="Maxim" src={`/maxim-logo${resolvedTheme === "dark" ? "-dark" : ""}.png`} width={19} height={19} />,
  },
  {
    id: "datadog",
    name: "Datadog",
    icon: <Image alt="Datadog" src="/images/datadog-logo.png" width={32} height={32} className="-ml-0.5" />,
    disabled: !IS_ENTERPRISE,
    tag: IS_ENTERPRISE ? undefined : "Enterprise only",
  },
  {
    id: "newrelic",
    name: "New Relic",
    icon: (
      <svg viewBox="0 0 832.8 959.8" xmlns="http://www.w3.org/2000/svg" width="19" height="19">
        <path d="M672.6 332.3l160.2-92.4v480L416.4 959.8V775.2l256.2-147.6z" fill="#00ac69" />
        <path d="M416.4 184.6L160.2 332.3 0 239.9 416.4 0l416.4 239.9-160.2 92.4z" fill="#1ce783" />
        <path d="M256.2 572.3L0 424.6V239.9l416.4 240v479.9l-160.2-92.2z" fill="#1d252c" />
      </svg>
    ),
    disabled: true,
    tag: "Coming soon",
  },
]

const getPluginNameForTab = (tabId: string) => (tabId === "prometheus" ? "telemetry" : tabId)
const getTabIdForPluginName = (pluginName: string) => (pluginName === "telemetry" ? "prometheus" : pluginName)

export default function ObservabilityConnectorsView() {
  const dispatch = useAppDispatch()
  const { data: plugins, isLoading } = useGetPluginsQuery()
  const [createPlugin, { isLoading: isCreating }] = useCreatePluginMutation()
  const [deletePlugin, { isLoading: isDeleting }] = useDeletePluginMutation()
  const [updatePlugin, { isLoading: isUpdating }] = useUpdatePluginMutation()
  const [selectedPluginId, setSelectedPluginId] = useQueryState("plugin")
  const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin)

  const { resolvedTheme } = useTheme()

  const supportedPlatforms = useMemo(() => supportedPlatformsList(resolvedTheme || "light"), [resolvedTheme])

  const configuredConnectorsMap = useMemo(() => {
    if (!plugins) return new Map()
    const map = new Map()
    for (const plugin of plugins) {
      const tabId = getTabIdForPluginName(plugin.name)
      map.set(tabId, plugin)
    }
    return map
  }, [plugins])

  const handleSelectConnector = useCallback(async (platform: SupportedPlatform) => {
    if (platform.disabled) return

    const pluginName = getPluginNameForTab(platform.id)
    const isConfigured = configuredConnectorsMap.has(platform.id)

    if (!isConfigured) {
      try {
        await createPlugin({
          name: pluginName,
          path: "",
          enabled: false,
          config: {},
        }).unwrap()
        toast.success("Connector added.")
      } catch (err) {
        toast.error(getErrorMessage(err))
        return
      }
    }

    setSelectedPluginId(platform.id)
  }, [configuredConnectorsMap, createPlugin, setSelectedPluginId])

  const handleDeleteConnectorById = async (tabId: string) => {
    const pluginName = getPluginNameForTab(tabId)
    try {
      await deletePlugin(pluginName).unwrap()
      const firstConfigured = supportedPlatforms.find(
        (p) => !p.disabled && p.id !== tabId && configuredConnectorsMap.has(p.id),
      )
      if (firstConfigured) {
        setSelectedPluginId(firstConfigured.id)
      } else {
        const firstAvailable = supportedPlatforms.find(
          (p) => !p.disabled && p.id !== tabId,
        )
        if (firstAvailable) {
          void handleSelectConnector(firstAvailable)
        } else {
          setSelectedPluginId(null)
        }
      }
      toast.success("Connector removed.")
    } catch (err) {
      toast.error(getErrorMessage(err))
    }
  }

  const handleToggleEnabled = async (tabId: string) => {
    const plugin = configuredConnectorsMap.get(tabId)
    if (!plugin) return
    try {
      await updatePlugin({
        name: getPluginNameForTab(tabId),
        data: {
          enabled: !plugin.enabled,
          config: plugin.config ?? {},
        },
      }).unwrap()
      toast.success(plugin.enabled ? "Connector disabled." : "Connector enabled.")
    } catch (err) {
      toast.error(getErrorMessage(err))
    }
  }

  useEffect(() => {
    if (!selectedPluginId) {
      const first = supportedPlatforms.find((p) => !p.disabled)
      if (first) {
        void handleSelectConnector(first)
      } else {
        setSelectedPluginId(null)
      }
    }
  }, [selectedPluginId, supportedPlatforms, handleSelectConnector, setSelectedPluginId])

  useEffect(() => {
    if (selectedPluginId && plugins) {
      const pluginName = getPluginNameForTab(selectedPluginId)
      const plugin = plugins.find((p) => p.name === pluginName) ?? {
        name: selectedPluginId,
        enabled: false,
        config: {},
        isCustom: false,
        path: "",
      }
      dispatch(setSelectedPlugin(plugin))
    }
  }, [selectedPluginId, plugins, dispatch])

  if (isLoading) {
    return <FullPageLoader />
  }

  const currentId = selectedPluginId ?? supportedPlatforms[0]?.id ?? ""
  const currentPlugin = configuredConnectorsMap.get(currentId)
  const currentPlatform = supportedPlatforms.find((p) => p.id === currentId)

  const enableToggle = currentPlugin
    ? {
        enabled: currentPlugin.enabled,
        onToggle: () => handleToggleEnabled(currentId),
        disabled: isUpdating,
      }
    : undefined

  return (
    <div className="flex h-full gap-6">
      <div className="flex w-[260px] shrink-0 flex-col gap-1.5">
        {supportedPlatforms.map((platform) => {
          const isActive = currentId === platform.id
          const plugin = configuredConnectorsMap.get(platform.id)
          const isConfigured = !!plugin

          return (
            <div
              key={platform.id}
              role="button"
              tabIndex={platform.disabled ? -1 : 0}
              aria-disabled={platform.disabled}
              onClick={() => handleSelectConnector(platform)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault()
                  handleSelectConnector(platform)
                }
              }}
              data-testid={`observability-connector-card-${platform.id}`}
              className={cn(
                "group flex cursor-pointer items-center gap-3 rounded-sm border border-transparent px-2 py-1 text-left transition-colors",
                isActive
                  ? "bg-muted border-border"
                  : "hover:bg-muted/50",
                platform.disabled && "cursor-not-allowed opacity-60",
                isCreating && "pointer-events-none",
              )}
            >
              <div className="flex shrink-0 items-center">{platform.icon}</div>
              <span className="min-w-0 flex-1 truncate text-sm">{platform.name}</span>
              {platform.tag && (
                <span className="text-muted-foreground shrink-0 text-xs">{platform.tag}</span>
              )}
              {!platform.disabled && isConfigured && (
                <span
                  className={cn(
                    "size-2 shrink-0 rounded-full",
                    plugin.enabled ? "bg-green-500" : "bg-muted-foreground/40",
                  )}
                  title={plugin.enabled ? "Enabled" : "Disabled"}
                />
              )}
            </div>
          )
        })}
      </div>

      <div className="custom-scrollbar min-h-0 min-w-0 flex-1 overflow-y-auto ml-4">
        {currentPlatform?.disabled ? (
          <div className="flex h-full items-center justify-center">
            <div className="text-muted-foreground text-center">
              <p className="text-sm font-medium">{currentPlatform.name}</p>
              <p className="mt-1 text-xs">{currentPlatform.tag}</p>
            </div>
          </div>
        ) : (
          <>
            {currentId === "prometheus" && (
              <PrometheusView
                onDelete={() => handleDeleteConnectorById("prometheus")}
                isDeleting={isDeleting}
                enableToggle={enableToggle}
              />
            )}
            {currentId === "otel" && (
              <OtelView
                onDelete={() => handleDeleteConnectorById("otel")}
                isDeleting={isDeleting}
                enableToggle={enableToggle}
              />
            )}
            {currentId === "maxim" && (
              <MaximView
                onDelete={() => handleDeleteConnectorById("maxim")}
                isDeleting={isDeleting}
                enableToggle={enableToggle}
              />
            )}
            {currentId === "datadog" && (
              <DatadogView
                onDelete={() => handleDeleteConnectorById("datadog")}
                isDeleting={isDeleting}
                enableToggle={enableToggle}
              />
            )}
          </>
        )}
      </div>
    </div>
  )
}
