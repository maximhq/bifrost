import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { DottedSeparator } from "@/components/ui/separator";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Textarea } from "@/components/ui/textarea";
import { RenderProviderIcon } from "@/lib/constants/icons";
import {
  ProviderLabels,
  ProviderName,
  RequestTypeLabels,
} from "@/lib/constants/logs";
import {
  getErrorMessage,
  ModelDetails,
  PricingTimeRule,
  ModelPricingOverrideSummary,
  useGetCoreConfigQuery,
  useUpsertModelCatalogEntriesMutation,
} from "@/lib/store";
import { KnownProvider } from "@/lib/types/config";
import { PricingOverrideScopeKind } from "@/lib/types/governance";
import {
  formatCharacterPriceFull,
  formatTokenPriceFull,
} from "@/lib/utils/numbers";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { Link } from "@tanstack/react-router";
import { ExternalLink, Plus, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import {
  fieldLabelByKey,
  PricingFieldKey,
  pricingFieldUnit,
} from "../../custom-pricing/overrides/pricingFields";
import OverriddenPrice from "./overriddenPrice";

const DEFAULT_PRICING_SOURCE_URL = "https://getbifrost.ai/datasheet";

// Scopes whose overrides can't be resolved from the model catalog alone — they
// only apply to requests carrying the matching virtual key, user, or provider
// key, so they are listed here but never change the displayed price.
const SCOPE_CAVEATS: Partial<Record<PricingOverrideScopeKind, string>> = {
  provider_key:
    "Applies only to requests routed through the matching provider key.",
  virtual_key: "Applies only to requests using the matching virtual key.",
  virtual_key_provider:
    "Applies only to requests using the matching virtual key and provider.",
  virtual_key_provider_key:
    "Applies only to requests using the matching virtual key and provider key.",
  user: "Applies only to requests from the matching user.",
  user_provider:
    "Applies only to requests from the matching user and provider.",
  user_provider_key:
    "Applies only to requests from the matching user and provider key.",
};

interface AttributeSheetProps {
  model: ModelDetails;
  /** Overrides referenced by `model.pricing_override_ids`, keyed by ID. */
  overrides?: Record<string, ModelPricingOverrideSummary>;
  onClose: () => void;
}

// Local row type for the extra-attributes editor. We keep these outside any
// schema because empty rows are valid during editing — we filter them at
// submit time. The id is a render-stable identifier (not persisted) so React
// keeps DOM nodes pinned to the right row across add/remove.
interface AttributeRow {
  id: string;
  key: string;
  value: string;
}

let rowIdCounter = 0;
function newRowId(): string {
  if (
    typeof crypto !== "undefined" &&
    typeof crypto.randomUUID === "function"
  ) {
    return crypto.randomUUID();
  }
  rowIdCounter += 1;
  return `row-${rowIdCounter}`;
}

interface ScheduleRuleRow {
  id: string;
  days: string[];
  startTime: string;
  endTime: string;
  multiplier: string;
}

const WEEKDAYS = [
  "monday",
  "tuesday",
  "wednesday",
  "thursday",
  "friday",
  "saturday",
  "sunday",
] as const;

function scheduleRowsFromSchedule(schedule?: {
  rules?: PricingTimeRule[];
}): ScheduleRuleRow[] {
  return (schedule?.rules ?? []).map((rule) => ({
    id: newRowId(),
    days: rule.days ?? [],
    startTime: rule.start_time ?? "00:00",
    endTime: rule.end_time ?? "00:00",
    multiplier: String(rule.multiplier ?? 1),
  }));
}

function stripScheduleRowIds(rows: ScheduleRuleRow[]) {
  return rows.map(({ days, startTime, endTime, multiplier }) => ({
    days,
    startTime,
    endTime,
    multiplier,
  }));
}

function parseScheduleFromRows(rows: ScheduleRuleRow[]): {
  rules?: PricingTimeRule[];
  error?: string;
} {
  const rules: PricingTimeRule[] = [];
  for (const [index, row] of rows.entries()) {
    const multiplier = Number(row.multiplier);
    if (!Number.isFinite(multiplier) || multiplier <= 0)
      return {
        error: `Rule ${index + 1}: multiplier must be greater than zero`,
      };
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(row.startTime))
      return { error: `Rule ${index + 1}: start time must be HH:MM` };
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(row.endTime))
      return { error: `Rule ${index + 1}: end time must be HH:MM` };
    rules.push({
      days: row.days,
      start_time: row.startTime,
      end_time: row.endTime,
      multiplier,
    });
  }
  if (rules.length === 0)
    return { error: "At least one schedule rule is required" };
  return { rules };
}

function rowsFromAttributes(attrs?: Record<string, string>): AttributeRow[] {
  if (!attrs) return [];
  return Object.entries(attrs)
    .filter(([k]) => k !== "description")
    .map(([key, value]) => ({ id: newRowId(), key, value }));
}

function isLinkableSource(url: string) {
  return url.startsWith("http://") || url.startsWith("https://");
}

function getPricingSourceUrl(
  configuredUrl: string | undefined,
  modelName: string,
) {
  if (configuredUrl) return configuredUrl;
  const url = new URL(DEFAULT_PRICING_SOURCE_URL);
  url.searchParams.set("model", modelName);
  return url.toString();
}

// formatPatchValue renders a patch value in its field's own unit: the way the
// pricing table does for token-priced fields, as a bare multiplier for the geo
// multiplier, and as a plain dollar amount for everything else (per-image,
// per-second, per-page, …).
function formatPatchValue(key: string, value: number): string {
  switch (pricingFieldUnit(key)) {
    case "token":
      return formatTokenPriceFull(value);
    case "character":
      return formatCharacterPriceFull(value);
    case "multiplier":
      return `${value}×`;
    default:
      return `$${value}`;
  }
}

export default function AttributeSheet({
  model,
  overrides,
  onClose,
}: AttributeSheetProps) {
  const [isOpen, setIsOpen] = useState(true);
  const hasUpdateAccess = useRbac(
    RbacResource.ModelProvider,
    RbacOperation.Update,
  );
  const { data: bifrostConfig } = useGetCoreConfigQuery({ fromDB: true });

  const [upsertEntries, { isLoading }] = useUpsertModelCatalogEntriesMutation();

  const initialDescription = model.additional_attributes?.description ?? "";
  const [description, setDescription] = useState(initialDescription);
  const initialSchedule = model.pricing_schedule;
  const [scheduleEnabled, setScheduleEnabled] = useState(!!initialSchedule);
  const [scheduleTimezone, setScheduleTimezone] = useState(
    initialSchedule?.timezone ?? "Asia/Shanghai",
  );
  const [scheduleCalendar, setScheduleCalendar] = useState(
    initialSchedule?.calendar ?? "iso_weekday",
  );
  const initialScheduleRows = useMemo(
    () => scheduleRowsFromSchedule(initialSchedule),
    [initialSchedule],
  );
  const [scheduleRows, setScheduleRows] =
    useState<ScheduleRuleRow[]>(initialScheduleRows);

  // Overrides arrive as IDs into a response-level index; skip any that went
  // missing (e.g. deleted in another tab before this list refreshed).
  const matchingOverrides = useMemo(
    () =>
      (model.pricing_override_ids ?? [])
        .map((id) => overrides?.[id])
        .filter((o): o is ModelPricingOverrideSummary => !!o),
    [model.pricing_override_ids, overrides],
  );
  const appliedOverrideName = model.applied_override_id
    ? overrides?.[model.applied_override_id]?.name
    : undefined;

  const initialRows = useMemo(
    () => rowsFromAttributes(model.additional_attributes),
    [model.additional_attributes],
  );
  const stripIds = (rows: AttributeRow[]) =>
    rows.map(({ key, value }) => ({ key, value }));
  const [initialRowsKey] = useState(() =>
    JSON.stringify(stripIds(initialRows)),
  );
  const [extraRows, setExtraRows] = useState<AttributeRow[]>(initialRows);

  const rowsDirty = JSON.stringify(stripIds(extraRows)) !== initialRowsKey;
  const initialScheduleRowsKey = JSON.stringify(
    stripScheduleRowIds(initialScheduleRows),
  );
  const scheduleRowsDirty =
    JSON.stringify(stripScheduleRowIds(scheduleRows)) !==
    initialScheduleRowsKey;
  const scheduleDirty =
    scheduleEnabled !== !!initialSchedule ||
    (scheduleEnabled &&
      (scheduleTimezone !== (initialSchedule?.timezone ?? "Asia/Shanghai") ||
        scheduleCalendar !== (initialSchedule?.calendar ?? "iso_weekday") ||
        scheduleRowsDirty));
  const isDirty =
    description !== initialDescription || rowsDirty || scheduleDirty;
  const pricingSourceUrl = getPricingSourceUrl(
    bifrostConfig?.framework_config?.pricing_url,
    model.name,
  );
  const canOpenPricingSource = isLinkableSource(pricingSourceUrl);

  const handleClose = () => {
    setIsOpen(false);
    setTimeout(() => onClose(), 150);
  };

  const handleAddRow = () =>
    setExtraRows((prev) => [...prev, { id: newRowId(), key: "", value: "" }]);
  const handleRowChange = (id: string, field: "key" | "value", val: string) =>
    setExtraRows((prev) =>
      prev.map((row) => (row.id === id ? { ...row, [field]: val } : row)),
    );
  const handleRemoveRow = (id: string) =>
    setExtraRows((prev) => prev.filter((row) => row.id !== id));

  const handleAddScheduleRule = () =>
    setScheduleRows((prev) => [
      ...prev,
      {
        id: newRowId(),
        days: ["monday", "tuesday", "wednesday", "thursday", "friday"],
        startTime: "09:00",
        endTime: "12:00",
        multiplier: "1",
      },
    ]);
  const handleScheduleRuleChange = (
    id: string,
    patch: Partial<ScheduleRuleRow>,
  ) =>
    setScheduleRows((prev) =>
      prev.map((row) => (row.id === id ? { ...row, ...patch } : row)),
    );
  const handleScheduleRuleRemove = (id: string) =>
    setScheduleRows((prev) => prev.filter((row) => row.id !== id));
  const toggleScheduleDay = (id: string, day: string) =>
    setScheduleRows((prev) =>
      prev.map((row) =>
        row.id === id
          ? {
              ...row,
              days: row.days.includes(day)
                ? row.days.filter((value) => value !== day)
                : [...row.days, day],
            }
          : row,
      ),
    );

  const handleSubmit = async () => {
    if (!hasUpdateAccess) {
      toast.error("You don't have permission to perform this action");
      return;
    }

    // Validate that extra rows have non-empty keys when they have any value.
    // Empty rows are fine — we drop them.
    const cleaned = extraRows
      .map((r) => ({ key: r.key.trim(), value: r.value }))
      .filter((r) => r.key !== "" || r.value !== "");
    const missingKey = cleaned.find((r) => r.key === "");
    if (missingKey) {
      toast.error("Attribute rows must have a key");
      return;
    }
    const dupKey = cleaned.find(
      (r, i) => cleaned.findIndex((other) => other.key === r.key) !== i,
    );
    if (dupKey) {
      toast.error(`Duplicate attribute key: ${dupKey.key}`);
      return;
    }
    // "description" is the special-cased field above — disallow it as an extra row.
    const reservedClash = cleaned.find((r) => r.key === "description");
    if (reservedClash) {
      toast.error(
        "Use the Description field instead of a 'description' attribute row",
      );
      return;
    }

    const attributes: Record<string, string> = {};
    const desc = description.trim();
    if (desc !== "") attributes.description = desc;
    for (const r of cleaned) attributes[r.key] = r.value;

    let scheduleRules: PricingTimeRule[] | undefined;
    if (scheduleEnabled) {
      if (!scheduleTimezone.trim()) {
        toast.error("Pricing schedule timezone is required");
        return;
      }
      const parsed = parseScheduleFromRows(scheduleRows);
      if (parsed.error) {
        toast.error(parsed.error);
        return;
      }
      scheduleRules = parsed.rules;
    }

    try {
      await upsertEntries([
        {
          model: model.name,
          provider: model.provider,
          additional_attributes: attributes,
          ...(scheduleEnabled
            ? {
                pricing_schedule: {
                  timezone: scheduleTimezone.trim(),
                  calendar: scheduleCalendar,
                  rules: scheduleRules,
                },
              }
            : initialSchedule
              ? { clear_pricing_schedule: true }
              : {}),
        },
      ]).unwrap();
      toast.success("Attributes saved");
      handleClose();
    } catch (err) {
      toast.error(getErrorMessage(err));
    }
  };

  return (
    <Sheet open={isOpen} onOpenChange={(open) => !open && handleClose()}>
      <SheetContent
        className="flex w-full flex-col overflow-x-hidden pt-4"
        onInteractOutside={(e) => {
          if (isDirty) e.preventDefault();
        }}
        onEscapeKeyDown={(e) => {
          if (isDirty) e.preventDefault();
        }}
        data-testid="model-catalog-attribute-sheet"
      >
        <SheetHeader
          className="flex flex-col items-start p-0 px-4 py-4 md:px-8"
          headerClassName="mb-0 sticky -top-4 bg-card z-10"
        >
          <SheetTitle>Edit Model Attributes</SheetTitle>
          <SheetDescription>
            Update the description, attributes, and time-based pricing schedule
            for this model. Editorial fields are preserved across pricing sync.
          </SheetDescription>
        </SheetHeader>

        <div className="flex h-full flex-col gap-6">
          <div className="grow space-y-4 px-4 md:px-8">
            {/* Read-only provider / model header */}
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <div>
                <Label className="text-sm font-medium">Provider</Label>
                <div className="bg-muted/30 mt-2 flex items-center gap-2 rounded-sm border px-3 py-2 text-sm">
                  <RenderProviderIcon
                    provider={model.provider as KnownProvider}
                    size="sm"
                    className="h-4 w-4"
                  />
                  <span>
                    {ProviderLabels[model.provider as ProviderName] ||
                      model.provider}
                  </span>
                </div>
              </div>
              <div>
                <Label className="text-sm font-medium">Model</Label>
                <div className="bg-muted/30 mt-2 rounded-sm border px-3 py-2 font-mono text-sm">
                  {model.name}
                </div>
              </div>
            </div>

            <DottedSeparator />

            {/* Pricing */}
            <div className="space-y-3">
              <div className="flex items-center justify-between gap-3">
                <Label className="text-sm font-medium">Pricing</Label>
                {canOpenPricingSource ? (
                  <a
                    href={pricingSourceUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
                    data-testid="model-catalog-pricing-source-link"
                  >
                    Source
                    <ExternalLink className="h-3 w-3" />
                  </a>
                ) : (
                  <span
                    className="text-muted-foreground max-w-[260px] truncate text-right font-mono text-xs"
                    title={pricingSourceUrl}
                  >
                    {pricingSourceUrl}
                  </span>
                )}
              </div>
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                <div className="bg-muted/30 rounded-sm border px-3 py-2">
                  <p className="text-muted-foreground text-xs">Input</p>
                  <p
                    className="mt-1 font-mono text-sm"
                    data-testid="model-catalog-input-cost"
                  >
                    <OverriddenPrice
                      variant="full"
                      base={model.input_cost_per_token}
                      override={model.overridden_pricing?.input_cost_per_token}
                      overrideName={appliedOverrideName}
                    />
                  </p>
                </div>
                <div className="bg-muted/30 rounded-sm border px-3 py-2">
                  <p className="text-muted-foreground text-xs">Output</p>
                  <p
                    className="mt-1 font-mono text-sm"
                    data-testid="model-catalog-output-cost"
                  >
                    <OverriddenPrice
                      variant="full"
                      base={model.output_cost_per_token}
                      override={model.overridden_pricing?.output_cost_per_token}
                      overrideName={appliedOverrideName}
                    />
                  </p>
                </div>
                <div className="bg-muted/30 rounded-sm border px-3 py-2">
                  <p className="text-muted-foreground text-xs">Cache Write</p>
                  <p
                    className="mt-1 font-mono text-sm"
                    data-testid="model-catalog-cache-write-cost"
                  >
                    <OverriddenPrice
                      variant="full"
                      base={model.cache_creation_input_token_cost}
                      override={
                        model.overridden_pricing
                          ?.cache_creation_input_token_cost
                      }
                      overrideName={appliedOverrideName}
                    />
                  </p>
                </div>
                <div className="bg-muted/30 rounded-sm border px-3 py-2">
                  <p className="text-muted-foreground text-xs">Cache Read</p>
                  <p
                    className="mt-1 font-mono text-sm"
                    data-testid="model-catalog-cache-read-cost"
                  >
                    <OverriddenPrice
                      variant="full"
                      base={model.cache_read_input_token_cost}
                      override={
                        model.overridden_pricing?.cache_read_input_token_cost
                      }
                      overrideName={appliedOverrideName}
                    />
                  </p>
                </div>
              </div>
            </div>

            {matchingOverrides.length > 0 && (
              <>
                <DottedSeparator />

                {/* Pricing overrides */}
                <div
                  className="space-y-3"
                  data-testid="model-catalog-pricing-overrides"
                >
                  <div className="flex items-center justify-between gap-3">
                    <Label className="text-sm font-medium">
                      Pricing overrides
                    </Label>
                    <Link
                      to="/workspace/custom-pricing/overrides"
                      className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
                    >
                      Manage
                      <ExternalLink className="h-3 w-3" />
                    </Link>
                  </div>
                  {matchingOverrides.map((override) => {
                    const caveat = SCOPE_CAVEATS[override.scope_kind];
                    const patchEntries = Object.entries(override.patch).filter(
                      ([, value]) => value !== undefined && value !== null,
                    );
                    return (
                      <div
                        key={override.id}
                        className="bg-muted/30 space-y-2 rounded-sm border px-3 py-2"
                        data-testid={`model-catalog-pricing-override-${override.id}`}
                      >
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-sm font-medium">
                            {override.name || override.id}
                          </span>
                          <Badge variant="secondary">
                            {override.scope_kind}
                          </Badge>
                          {override.id === model.applied_override_id && (
                            <Badge variant="outline">Applied</Badge>
                          )}
                        </div>
                        <p className="text-muted-foreground font-mono text-xs">
                          {override.match_type === "wildcard"
                            ? "Matches"
                            : "Exact"}{" "}
                          {override.pattern}
                        </p>
                        {caveat && (
                          <p className="text-muted-foreground text-xs">
                            {caveat}
                          </p>
                        )}
                        {override.request_types &&
                          override.request_types.length > 0 && (
                            <div className="flex flex-wrap gap-1">
                              {override.request_types.map((rt) => (
                                <Badge
                                  key={rt}
                                  variant="outline"
                                  className="text-[10px]"
                                >
                                  {RequestTypeLabels[
                                    rt as keyof typeof RequestTypeLabels
                                  ] ?? rt}
                                </Badge>
                              ))}
                            </div>
                          )}
                        {patchEntries.length > 0 && (
                          <div className="space-y-1">
                            {patchEntries.map(([key, value]) => (
                              <div
                                key={key}
                                className="flex items-baseline justify-between gap-3 text-xs"
                              >
                                <span className="text-muted-foreground">
                                  {fieldLabelByKey[key as PricingFieldKey] ||
                                    key}
                                </span>
                                <span className="font-mono">
                                  {formatPatchValue(key, value as number)}
                                </span>
                              </div>
                            ))}
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>
              </>
            )}

            <DottedSeparator />

            {/* Time-based pricing */}
            <div
              className="space-y-3"
              data-testid="model-catalog-pricing-schedule"
            >
              <div className="flex items-center justify-between gap-3">
                <div>
                  <Label className="text-sm font-medium">
                    Time-based pricing
                  </Label>
                  <p className="text-muted-foreground mt-1 text-xs">
                    Recurring multipliers are applied after service/context
                    tiers. Billing uses the provider attempt start time, not
                    completion time.
                  </p>
                </div>
                <Checkbox
                  checked={scheduleEnabled}
                  onCheckedChange={(checked) =>
                    setScheduleEnabled(checked === true)
                  }
                  aria-label="Enable time-based pricing"
                  data-testid="model-catalog-pricing-schedule-enabled"
                />
              </div>
              {scheduleEnabled && (
                <div className="space-y-3">
                  <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
                    <div>
                      <Label className="text-xs">Timezone</Label>
                      <Input
                        className="mt-1"
                        value={scheduleTimezone}
                        onChange={(e) => setScheduleTimezone(e.target.value)}
                        placeholder="Asia/Shanghai"
                        data-testid="model-catalog-pricing-schedule-timezone"
                      />
                    </div>
                    <div>
                      <Label className="text-xs">Calendar</Label>
                      <Select
                        value={scheduleCalendar}
                        onValueChange={setScheduleCalendar}
                      >
                        <SelectTrigger
                          className="mt-1 w-full"
                          data-testid="model-catalog-pricing-schedule-calendar"
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="none">Time only</SelectItem>
                          <SelectItem value="iso_weekday">
                            ISO weekday
                          </SelectItem>
                        </SelectContent>
                      </Select>
                    </div>
                  </div>

                  <div className="space-y-2">
                    {scheduleRows.map((row, index) => (
                      <div
                        key={row.id}
                        className="bg-muted/30 space-y-2 rounded-sm border px-3 py-3"
                        data-testid={`model-catalog-pricing-schedule-rule-${index}`}
                      >
                        <div className="flex flex-wrap gap-1">
                          {WEEKDAYS.map((day) => (
                            <label
                              key={day}
                              className="flex items-center gap-1 text-xs"
                            >
                              <Checkbox
                                checked={row.days.includes(day)}
                                onCheckedChange={() =>
                                  toggleScheduleDay(row.id, day)
                                }
                                aria-label={day}
                                data-testid={`model-catalog-pricing-schedule-day-${index}-${day}`}
                              />
                              {day.slice(0, 3)}
                            </label>
                          ))}
                        </div>
                        <div className="grid grid-cols-1 gap-2 md:grid-cols-4">
                          <div>
                            <Label className="text-xs">Start</Label>
                            <Input
                              className="mt-1"
                              value={row.startTime}
                              onChange={(e) =>
                                handleScheduleRuleChange(row.id, {
                                  startTime: e.target.value,
                                })
                              }
                              placeholder="09:00"
                              data-testid={`model-catalog-pricing-schedule-start-${index}`}
                            />
                          </div>
                          <div>
                            <Label className="text-xs">End</Label>
                            <Input
                              className="mt-1"
                              value={row.endTime}
                              onChange={(e) =>
                                handleScheduleRuleChange(row.id, {
                                  endTime: e.target.value,
                                })
                              }
                              placeholder="12:00"
                              data-testid={`model-catalog-pricing-schedule-end-${index}`}
                            />
                          </div>
                          <div>
                            <Label className="text-xs">Multiplier</Label>
                            <Input
                              className="mt-1"
                              value={row.multiplier}
                              onChange={(e) =>
                                handleScheduleRuleChange(row.id, {
                                  multiplier: e.target.value,
                                })
                              }
                              placeholder="2"
                              data-testid={`model-catalog-pricing-schedule-multiplier-${index}`}
                            />
                          </div>
                          <div className="flex items-end">
                            <Button
                              type="button"
                              variant="ghost"
                              size="icon"
                              onClick={() => handleScheduleRuleRemove(row.id)}
                              aria-label="Remove pricing schedule rule"
                              data-testid={`model-catalog-pricing-schedule-remove-${index}`}
                            >
                              <Trash2 className="h-4 w-4" />
                            </Button>
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={handleAddScheduleRule}
                    data-testid="model-catalog-pricing-schedule-add-rule"
                  >
                    <Plus className="mr-1 h-3 w-3" />
                    Add Rule
                  </Button>
                </div>
              )}
            </div>

            <DottedSeparator />

            {/* Description */}
            <div>
              <Label className="text-sm font-medium">Description</Label>
              <Textarea
                className="mt-2"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                rows={4}
                placeholder="A short description of this model, shown anywhere additional_attributes.description is consumed."
                data-testid="model-catalog-description-textarea"
              />
            </div>

            <DottedSeparator />

            {/* Other attributes */}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label className="text-sm font-medium">Other Attributes</Label>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={handleAddRow}
                  data-testid="model-catalog-add-attribute-row"
                >
                  <Plus className="mr-1 h-3 w-3" />
                  Add
                </Button>
              </div>
              {extraRows.length === 0 ? (
                <p className="text-muted-foreground text-xs">
                  No additional attributes. Add a key-value pair for anything
                  beyond description.
                </p>
              ) : (
                <div className="space-y-2">
                  {extraRows.map((row, i) => (
                    <div key={row.id} className="flex items-start gap-2">
                      <Input
                        value={row.key}
                        onChange={(e) =>
                          handleRowChange(row.id, "key", e.target.value)
                        }
                        placeholder="key"
                        className="flex-1"
                        data-testid={`model-catalog-attribute-key-${i}`}
                      />
                      <Input
                        value={row.value}
                        onChange={(e) =>
                          handleRowChange(row.id, "value", e.target.value)
                        }
                        placeholder="value"
                        className="flex-1"
                        data-testid={`model-catalog-attribute-value-${i}`}
                      />
                      <Button
                        type="button"
                        variant="ghost"
                        size="icon"
                        onClick={() => handleRemoveRow(row.id)}
                        data-testid={`model-catalog-attribute-remove-${i}`}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>

          <div className="bg-card sticky bottom-0 shrink-0 border-t px-4 py-4 md:px-8">
            <div className="flex items-center justify-end gap-3">
              {!hasUpdateAccess && (
                <p className="text-destructive text-sm">
                  You don't have permission to perform this action
                </p>
              )}
              <Button
                type="button"
                variant="outline"
                onClick={handleClose}
                data-testid="model-catalog-attribute-cancel"
              >
                Cancel
              </Button>
              <Button
                type="button"
                onClick={handleSubmit}
                disabled={isLoading || !isDirty || !hasUpdateAccess}
                data-testid="model-catalog-attribute-submit"
              >
                {isLoading ? "Saving..." : "Save Changes"}
              </Button>
            </div>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
