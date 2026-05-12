import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from "@/components/ui/form";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import {
  prometheusFormSchema,
  type PrometheusFormSchema,
} from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { zodResolver } from "@hookform/resolvers/zod";
import {
  AlertTriangle,
  Copy,
  Eye,
  EyeOff,
  Info,
  Plus,
  Trash,
  Trash2,
} from "lucide-react";
import { useEffect, useState } from "react";
import { useForm, type Resolver } from "react-hook-form";

interface PrometheusFormFragmentProps {
  currentConfig?: {
    metrics_enabled?: boolean;
    push_gateway_enabled?: boolean;
    push_gateway_url?: string;
    job_name?: string;
    instance_id?: string;
    push_interval?: number;
    basic_auth?: {
      username?: string;
      password?: string;
    };
  };
  onSave: (config: PrometheusFormSchema) => Promise<void>;
  onDelete?: () => void;
  isDeleting?: boolean;
  isLoading?: boolean;
  metricsEndpoint?: string;
}

const buildDefaults = (
  initialConfig?: PrometheusFormFragmentProps["currentConfig"],
): PrometheusFormSchema => ({
  metrics_enabled: initialConfig?.metrics_enabled ?? true,
  push_gateway_enabled: initialConfig?.push_gateway_enabled ?? false,
  prometheus_config: {
    push_gateway_url: initialConfig?.push_gateway_url ?? "",
    job_name: initialConfig?.job_name ?? "bifrost",
    instance_id: initialConfig?.instance_id ?? "",
    push_interval: initialConfig?.push_interval ?? 15,
    basic_auth_username: initialConfig?.basic_auth?.username ?? "",
    basic_auth_password: initialConfig?.basic_auth?.password ?? "",
  },
});

// Field paths considered "owned" by each tab — used for per-tab Reset and to
// gate the per-tab Save button on whether *this* tab has unsaved changes.
const PULL_FIELDS = ["metrics_enabled"] as const;
const PUSH_FIELDS = [
  "push_gateway_enabled",
  "prometheus_config.push_gateway_url",
  "prometheus_config.job_name",
  "prometheus_config.instance_id",
  "prometheus_config.push_interval",
  "prometheus_config.basic_auth_username",
  "prometheus_config.basic_auth_password",
] as const;

export function PrometheusFormFragment({
  currentConfig: initialConfig,
  onSave,
  onDelete,
  isDeleting = false,
  isLoading = false,
  metricsEndpoint,
}: PrometheusFormFragmentProps) {
  const hasPrometheusAccess = useRbac(
    RbacResource.Observability,
    RbacOperation.Update,
  );
  const [showPassword, setShowPassword] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const { copy, copied } = useCopyToClipboard();
  const [showBasicAuth, setShowBasicAuth] = useState(
    !!(
      initialConfig?.basic_auth?.username || initialConfig?.basic_auth?.password
    ),
  );
  const [activeTab, setActiveTab] = useState<"pull" | "push">("pull");

  const form = useForm<PrometheusFormSchema, any, PrometheusFormSchema>({
    resolver: zodResolver(prometheusFormSchema) as Resolver<
      PrometheusFormSchema,
      any,
      PrometheusFormSchema
    >,
    mode: "onChange",
    reValidateMode: "onChange",
    defaultValues: buildDefaults(initialConfig),
  });

  const onSubmit = async (data: PrometheusFormSchema) => {
    setIsSaving(true);
    try {
      await onSave(data);
    } finally {
      setIsSaving(false);
    }
  };

  useEffect(() => {
    form.reset(buildDefaults(initialConfig));
    setShowBasicAuth(
      !!(
        initialConfig?.basic_auth?.username ||
        initialConfig?.basic_auth?.password
      ),
    );
  }, [form, initialConfig]);

  const handleCopyEndpoint = () => {
    if (metricsEndpoint) {
      copy(metricsEndpoint);
    }
  };

  const handleRemoveBasicAuth = () => {
    form.setValue("prometheus_config.basic_auth_username", "", {
      shouldDirty: true,
      shouldValidate: true,
    });
    form.setValue("prometheus_config.basic_auth_password", "", {
      shouldDirty: true,
      shouldValidate: true,
    });
    setShowBasicAuth(false);
  };

  // Reset only the fields belonging to the given tab. The other tab's pending
  // edits are preserved so a Reset on one tab feels scoped.
  const resetPullTab = () => {
    const defaults = buildDefaults(initialConfig);
    form.setValue("metrics_enabled", defaults.metrics_enabled, {
      shouldDirty: true,
      shouldValidate: true,
    });
  };

  const resetPushTab = () => {
    const defaults = buildDefaults(initialConfig);
    form.setValue("push_gateway_enabled", defaults.push_gateway_enabled, {
      shouldDirty: true,
      shouldValidate: true,
    });
    form.setValue(
      "prometheus_config.push_gateway_url",
      defaults.prometheus_config.push_gateway_url,
      {
        shouldDirty: true,
        shouldValidate: true,
      },
    );
    form.setValue(
      "prometheus_config.job_name",
      defaults.prometheus_config.job_name,
      { shouldDirty: true, shouldValidate: true },
    );
    form.setValue(
      "prometheus_config.instance_id",
      defaults.prometheus_config.instance_id ?? "",
      { shouldDirty: true, shouldValidate: true },
    );
    form.setValue(
      "prometheus_config.push_interval",
      defaults.prometheus_config.push_interval,
      { shouldDirty: true, shouldValidate: true },
    );
    form.setValue(
      "prometheus_config.basic_auth_username",
      defaults.prometheus_config.basic_auth_username ?? "",
      { shouldDirty: true, shouldValidate: true },
    );
    form.setValue(
      "prometheus_config.basic_auth_password",
      defaults.prometheus_config.basic_auth_password ?? "",
      { shouldDirty: true, shouldValidate: true },
    );
    setShowBasicAuth(
      !!(
        initialConfig?.basic_auth?.username ||
        initialConfig?.basic_auth?.password
      ),
    );
  };

  // Tabs can independently report whether *their* fields differ from the
  // last-saved state. Both Save buttons submit the entire form (single API
  // shape) — gating per-tab just avoids surfacing a Save when nothing in
  // the visible tab changed.
  const dirtyFields = form.formState.dirtyFields as Record<string, unknown>;
  const isPullDirty = PULL_FIELDS.some((path) => dirtyFields[path]);
  const isPushDirty = PUSH_FIELDS.some((path) => {
    const segments = path.split(".");
    let cursor: any = dirtyFields;
    for (const seg of segments) {
      if (cursor == null) return false;
      cursor = cursor[seg];
    }
    return !!cursor;
  });

  // Whole-form validity. Save is a single API call covering both tabs, so an
  // invalid field on the *other* tab silently blocks handleSubmit. We disable
  // Save when invalid and surface where the error lives so the user isn't
  // hunting through a tab they can't see.
  const formIsInvalid = !form.formState.isValid;
  const errors = form.formState.errors as Record<string, any>;
  const hasPullErrors = !!errors.metrics_enabled;
  const hasPushErrors =
    !!errors.push_gateway_enabled || !!errors.prometheus_config;

  const renderActions = (
    tabKey: "pull" | "push",
    tabDirty: boolean,
    onResetTab: () => void,
  ) => {
    const thisTabHasErrors = tabKey === "pull" ? hasPullErrors : hasPushErrors;
    const otherTabHasErrors = tabKey === "pull" ? hasPushErrors : hasPullErrors;
    const otherTabLabel = tabKey === "pull" ? "Push-based" : "Pull-based";
    const saveDisabled = !hasPrometheusAccess || !tabDirty || formIsInvalid;
    let tooltipMsg = "";
    if (!tabDirty) {
      tooltipMsg = "No changes made in this tab";
    } else if (formIsInvalid && otherTabHasErrors && !thisTabHasErrors) {
      tooltipMsg = `Fix validation errors in the ${otherTabLabel} tab before saving`;
    } else if (formIsInvalid) {
      tooltipMsg = "Fix validation errors before saving";
    }

    return (
      <div className="flex w-full flex-row items-center pt-4">
        <div className="ml-auto flex justify-end space-x-2 py-2">
          {onDelete && (
            <Button
              type="button"
              variant="outline"
              onClick={onDelete}
              disabled={isDeleting || !hasPrometheusAccess}
              data-testid="prometheus-connector-delete-btn"
              title="Delete connector"
              aria-label="Delete connector"
            >
              <Trash2 className="size-4" />
            </Button>
          )}
          <Button
            type="button"
            variant="outline"
            onClick={onResetTab}
            disabled={!hasPrometheusAccess || isLoading || !tabDirty}
            data-testid={`prometheus-${tabKey}-reset-btn`}
          >
            Reset
          </Button>
          <TooltipProvider>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="submit"
                  disabled={saveDisabled}
                  isLoading={isSaving}
                  data-testid={`prometheus-${tabKey}-save-btn`}
                >
                  Save Prometheus Configuration
                </Button>
              </TooltipTrigger>
              {tooltipMsg && (
                <TooltipContent>
                  <p>{tooltipMsg}</p>
                </TooltipContent>
              )}
            </Tooltip>
          </TooltipProvider>
        </div>
      </div>
    );
  };

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-6">
        <Tabs
          value={activeTab}
          onValueChange={(v) => setActiveTab(v as "pull" | "push")}
        >
          <TabsList className="gap-2">
            <TabsTrigger
              value="pull"
              className="px-2 py-1"
              data-testid="prometheus-tab-pull"
            >
              Pull-based
            </TabsTrigger>
            <TabsTrigger
              value="push"
              className="px-2 py-1"
              data-testid="prometheus-tab-push"
            >
              Push-based
            </TabsTrigger>
          </TabsList>

          {/* Pull-based tab: gates the /metrics scrape endpoint */}
          <TabsContent value="pull" className="space-y-4 mt-2">
            <div className="flex items-center justify-between gap-4">
              <div className="flex flex-col gap-1">
                <h3 className="text-sm font-medium">Pull-based Scraping</h3>
                <p className="text-muted-foreground text-xs">
                  Prometheus can scrape metrics from the /metrics endpoint
                </p>
              </div>
              <FormField
                control={form.control}
                name="metrics_enabled"
                render={({ field }) => (
                  <FormItem className="flex items-center gap-2">
                    <FormLabel className="text-muted-foreground text-sm font-medium">
                      Enabled
                    </FormLabel>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={!hasPrometheusAccess}
                        data-testid="prometheus-metrics-enable-toggle"
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
            </div>

            <div className="bg-muted/50 rounded-md p-4">
              <div className="flex items-center justify-between">
                <div className="flex flex-col gap-1">
                  <span className="text-sm font-medium">Metrics Endpoint</span>
                  <code className="text-muted-foreground text-xs">
                    {metricsEndpoint || "http://<bifrost-host>:<port>/metrics"}
                  </code>
                </div>
                {metricsEndpoint && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={handleCopyEndpoint}
                    className="shrink-0"
                    data-testid="prometheus-copy-endpoint"
                  >
                    <Copy className="mr-2 h-3 w-3" />
                    {copied ? "Copied!" : "Copy"}
                  </Button>
                )}
              </div>
              <p className="text-muted-foreground mt-2 text-xs">
                Configure your Prometheus server to scrape this endpoint. Served
                only while Pull-based scraping is enabled.
              </p>
            </div>

            {renderActions("pull", isPullDirty, resetPullTab)}
          </TabsContent>

          {/* Push-based tab: gates the push gateway loop */}
          <TabsContent value="push" className="space-y-4 mt-2">
            <div className="flex items-center justify-between gap-4">
              <div className="flex flex-col gap-1">
                <h3 className="flex flex-row items-center gap-2 text-sm font-medium">
                  Push-based (Push Gateway){" "}
                  <Badge variant="secondary">BETA</Badge>
                </h3>
                <p className="text-muted-foreground text-xs">
                  Push metrics to a Prometheus Push Gateway for proper
                  aggregation in cluster deployments
                </p>
              </div>
              <FormField
                control={form.control}
                name="push_gateway_enabled"
                render={({ field }) => (
                  <FormItem className="flex items-center gap-2">
                    <FormLabel className="text-muted-foreground text-sm font-medium">
                      Enabled
                    </FormLabel>
                    <FormControl>
                      <Switch
                        checked={field.value}
                        onCheckedChange={field.onChange}
                        disabled={!hasPrometheusAccess}
                        data-testid="prometheus-push-enable-toggle"
                      />
                    </FormControl>
                  </FormItem>
                )}
              />
            </div>

            <Alert variant="info">
              <AlertTriangle className="" />
              <AlertDescription className="text-xs">
                If you are running multiple Bifrost nodes, use push gateway for
                accurate metrics. Pull-based /metrics scraping may miss nodes
                behind a load balancer.
              </AlertDescription>
            </Alert>

            <div className="space-y-4">
              <FormField
                control={form.control}
                name="prometheus_config.push_gateway_url"
                render={({ field }) => (
                  <FormItem className="w-full">
                    <FormLabel>Push Gateway URL</FormLabel>
                    <FormControl>
                      <Input
                        placeholder="http://pushgateway:9091"
                        disabled={!hasPrometheusAccess}
                        data-testid="prometheus-push-gateway-url"
                        {...field}
                      />
                    </FormControl>
                    <FormDescription>
                      URL of your Prometheus Push Gateway
                    </FormDescription>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <div className="grid grid-cols-2 gap-4">
                <FormField
                  control={form.control}
                  name="prometheus_config.job_name"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Job Name</FormLabel>
                      <FormControl>
                        <Input
                          placeholder="bifrost"
                          disabled={!hasPrometheusAccess}
                          data-testid="prometheus-job-name"
                          {...field}
                        />
                      </FormControl>
                      <FormDescription>Job label for metrics</FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name="prometheus_config.push_interval"
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>Push Interval (seconds)</FormLabel>
                      <FormControl>
                        <Input
                          type="number"
                          min={1}
                          max={300}
                          disabled={!hasPrometheusAccess}
                          data-testid="prometheus-push-interval"
                          {...field}
                          onChange={(e) =>
                            field.onChange(parseInt(e.target.value) || 15)
                          }
                        />
                      </FormControl>
                      <FormDescription>
                        How often to push (1-300s)
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />
              </div>

              <FormField
                control={form.control}
                name="prometheus_config.instance_id"
                render={({ field }) => (
                  <FormItem>
                    <FormLabel className="flex items-center gap-2">
                      Instance ID
                      <TooltipProvider>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Info className="text-muted-foreground h-3 w-3" />
                          </TooltipTrigger>
                          <TooltipContent>
                            <p className="max-w-xs text-xs">
                              Used to identify this Bifrost instance in metrics.
                              If not set, hostname is used automatically.
                            </p>
                          </TooltipContent>
                        </Tooltip>
                      </TooltipProvider>
                    </FormLabel>
                    <FormControl>
                      <Input
                        placeholder="Auto-generated from hostname"
                        disabled={!hasPrometheusAccess}
                        data-testid="prometheus-instance-id"
                        {...field}
                        value={field.value ?? ""}
                      />
                    </FormControl>
                    <FormMessage />
                  </FormItem>
                )}
              />

              <div className="space-y-4 border-t pt-4">
                {!showBasicAuth ? (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => setShowBasicAuth(true)}
                    disabled={!hasPrometheusAccess}
                    data-testid="prometheus-add-basic-auth"
                  >
                    <Plus className="mr-2 h-3 w-3" />
                    Add Basic Auth
                  </Button>
                ) : (
                  <>
                    <div className="flex items-center justify-between">
                      <span className="text-sm font-medium">
                        Basic Authentication
                      </span>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={handleRemoveBasicAuth}
                        disabled={!hasPrometheusAccess}
                        className="text-muted-foreground hover:text-destructive h-auto p-1"
                        data-testid="prometheus-remove-basic-auth"
                        aria-label="Remove basic auth"
                      >
                        <Trash className="h-4 w-4" />
                      </Button>
                    </div>
                    <div className="border-muted grid grid-cols-2 gap-4">
                      <FormField
                        control={form.control}
                        name="prometheus_config.basic_auth_username"
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>Username</FormLabel>
                            <FormControl>
                              <Input
                                placeholder="Username"
                                disabled={!hasPrometheusAccess}
                                data-testid="prometheus-basic-auth-username"
                                {...field}
                              />
                            </FormControl>
                            <FormMessage />
                          </FormItem>
                        )}
                      />

                      <FormField
                        control={form.control}
                        name="prometheus_config.basic_auth_password"
                        render={({ field }) => (
                          <FormItem>
                            <FormLabel>Password</FormLabel>
                            <FormControl>
                              <div className="relative">
                                <Input
                                  type={showPassword ? "text" : "password"}
                                  placeholder="Password"
                                  disabled={!hasPrometheusAccess}
                                  data-testid="prometheus-basic-auth-password"
                                  {...field}
                                  className="pr-10"
                                />
                                <Button
                                  type="button"
                                  variant="ghost"
                                  size="sm"
                                  className="absolute top-0 right-0 h-full px-3 py-2 hover:bg-transparent"
                                  onClick={() => setShowPassword(!showPassword)}
                                  disabled={!hasPrometheusAccess}
                                  data-testid="prometheus-toggle-password"
                                  aria-label={
                                    showPassword
                                      ? "Hide password"
                                      : "Show password"
                                  }
                                >
                                  {showPassword ? (
                                    <EyeOff className="h-4 w-4" />
                                  ) : (
                                    <Eye className="h-4 w-4" />
                                  )}
                                </Button>
                              </div>
                            </FormControl>
                            <FormMessage />
                          </FormItem>
                        )}
                      />
                    </div>
                  </>
                )}
              </div>
            </div>

            {renderActions("push", isPushDirty, resetPushTab)}
          </TabsContent>
        </Tabs>
      </form>
    </Form>
  );
}
