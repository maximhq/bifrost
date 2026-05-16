import FormFooter from "@/components/formFooter";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  resetDurationOptions,
  supportsCalendarAlignment,
} from "@/lib/constants/governance";
import {
  getErrorMessage,
  useCreateTeamMutation,
  useUpdateTeamMutation,
} from "@/lib/store";
import {
  CreateTeamRequest,
  Customer,
  Team,
  UpdateTeamRequest,
} from "@/lib/types/governance";
import { formatCurrency } from "@/lib/utils/governance";
import { Validator } from "@/lib/utils/validation";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { formatDistanceToNow } from "date-fns";
import isEqual from "lodash.isequal";
import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { v4 as uuid } from "uuid";

interface TeamDialogProps {
  team?: Team | null;
  customers: Customer[];
  onSave: () => void;
  onCancel: () => void;
}

// One editable budget row; teams own multiple, each keyed by reset_duration
// on the wire. The client-side `id` is stable across re-renders and equals
// the persisted budget's id for existing rows, or a fresh UUID for new ones —
// used as the React key and for matching against `team.budgets` when we need
// to distinguish "already persisted" from "just added in the form".
interface TeamBudgetRow {
  id: string;
  maxLimit: number | undefined;
  resetDuration: string;
  calendarAligned: boolean;
}

interface TeamFormData {
  name: string;
  customerId: string;
  // Multi-budget: each row has a unique reset_duration on submit
  budgets: TeamBudgetRow[];
  // Rate Limit
  tokenMaxLimit: number | undefined;
  tokenResetDuration: string;
  requestMaxLimit: number | undefined;
  requestResetDuration: string;
  isDirty: boolean;
}

// Helper function to create initial state
const createInitialState = (
  team?: Team | null,
): Omit<TeamFormData, "isDirty"> => {
  return {
    name: team?.name || "",
    customerId: team?.customer_id || "",
    budgets:
      team?.budgets?.map((b) => ({
        id: b.id,
        maxLimit: b.max_limit,
        resetDuration: b.reset_duration,
        calendarAligned: b.calendar_aligned ?? false,
      })) ?? [],
    // Rate Limit
    tokenMaxLimit: team?.rate_limit?.token_max_limit ?? undefined,
    tokenResetDuration: team?.rate_limit?.token_reset_duration || "1h",
    requestMaxLimit: team?.rate_limit?.request_max_limit ?? undefined,
    requestResetDuration: team?.rate_limit?.request_reset_duration || "1h",
  };
};

export default function TeamDialog({
  team,
  customers,
  onSave,
  onCancel,
}: TeamDialogProps) {
  const isEditing = !!team;
  const [initialState, setInitialState] = useState<
    Omit<TeamFormData, "isDirty">
  >(createInitialState(team));
  const [formData, setFormData] = useState<TeamFormData>({
    ...initialState,
    isDirty: false,
  });

  useEffect(() => {
    const nextInitial = createInitialState(team);
    setInitialState(nextInitial);
    setFormData({ ...nextInitial, isDirty: false });
    setPendingCalendarAlignIdx(null);
  }, [team]);

  const hasCreateAccess = useRbac(RbacResource.Teams, RbacOperation.Create);
  const hasUpdateAccess = useRbac(RbacResource.Teams, RbacOperation.Update);
  const hasPermission = isEditing ? hasUpdateAccess : hasCreateAccess;

  // RTK Query hooks
  const [createTeam, { isLoading: isCreating }] = useCreateTeamMutation();
  const [updateTeam, { isLoading: isUpdating }] = useUpdateTeamMutation();
  const loading = isCreating || isUpdating;

  // Tracks which row (by index) is awaiting calendar-align confirmation.
  const [pendingCalendarAlignIdx, setPendingCalendarAlignIdx] = useState<
    number | null
  >(null);
  const showCalendarAlignWarning = pendingCalendarAlignIdx !== null;

  const updateBudgetRow = (idx: number, patch: Partial<TeamBudgetRow>) => {
    setFormData((prev) => {
      const next = prev.budgets.map((row, i) =>
        i === idx ? { ...row, ...patch } : row,
      );
      return { ...prev, budgets: next };
    });
  };

  const addBudgetRow = () => {
    setFormData((prev) => ({
      ...prev,
      budgets: [
        ...prev.budgets,
        {
          id: uuid(),
          maxLimit: undefined,
          resetDuration: "1M",
          calendarAligned: false,
        },
      ],
    }));
  };

  const removeBudgetRow = (idx: number) => {
    setFormData((prev) => ({
      ...prev,
      budgets: prev.budgets.filter((_, i) => i !== idx),
    }));
  };

  const handleCalendarAlignedChange = (idx: number, checked: boolean) => {
    // Match the persisted budget by stable row id — for seeded rows this equals
    // the server-side budget id; for newly-added rows it's a client-only UUID
    // that won't match anything in team.budgets (correctly: no warning for new rows).
    // Avoids the reset_duration-duplicate ambiguity before validation resolves.
    const rowId = formData.budgets[idx]?.id;
    const existingBudget = team?.budgets?.find((b) => b.id === rowId);
    if (checked && isEditing && existingBudget && !existingBudget.calendar_aligned) {
      setPendingCalendarAlignIdx(idx);
    } else {
      updateBudgetRow(idx, { calendarAligned: checked });
    }
  };

  // Track isDirty state
  useEffect(() => {
    const currentData: Omit<TeamFormData, "isDirty"> = {
      name: formData.name,
      customerId: formData.customerId,
      budgets: formData.budgets,
      tokenMaxLimit: formData.tokenMaxLimit,
      tokenResetDuration: formData.tokenResetDuration,
      requestMaxLimit: formData.requestMaxLimit,
      requestResetDuration: formData.requestResetDuration,
    };
    setFormData((prev) => ({
      ...prev,
      isDirty: !isEqual(initialState, currentData),
    }));
  }, [
    formData.name,
    formData.customerId,
    formData.budgets,
    formData.tokenMaxLimit,
    formData.tokenResetDuration,
    formData.requestMaxLimit,
    formData.requestResetDuration,
    initialState,
  ]);

  const tokenMaxLimitNum = formData.tokenMaxLimit;
  const requestMaxLimitNum = formData.requestMaxLimit;

  // Validation
  const validator = useMemo(() => {
    // Per-row budget validation plus cross-row uniqueness on reset_duration.
    const budgetValidators = formData.budgets.flatMap((row, idx) => {
      if (row.maxLimit === undefined || row.maxLimit === null) return [];
      return [
        Validator.minValue(
          row.maxLimit,
          0.01,
          `Budget #${idx + 1} max limit must be greater than $0.01`,
        ),
        Validator.required(
          row.resetDuration,
          `Budget #${idx + 1} reset duration is required`,
        ),
      ];
    });
    const populatedDurations = formData.budgets
      .filter((r) => r.maxLimit !== undefined && r.maxLimit !== null)
      .map((r) => r.resetDuration);
    const uniqueDurations = new Set(populatedDurations).size;

    return new Validator([
      Validator.required(formData.name.trim(), "Team name is required"),
      Validator.custom(formData.isDirty, "No changes to save"),
      ...budgetValidators,
      Validator.custom(
        uniqueDurations === populatedDurations.length,
        "Each budget must have a distinct reset duration",
      ),

      // Rate limit validation - token limits
      ...(formData.tokenMaxLimit !== undefined &&
        formData.tokenMaxLimit !== null
        ? [
          Validator.minValue(
            tokenMaxLimitNum || 0,
            1,
            "Token max limit must be at least 1",
          ),
          Validator.required(
            formData.tokenResetDuration,
            "Token reset duration is required",
          ),
        ]
        : []),

      // Rate limit validation - request limits
      ...(formData.requestMaxLimit !== undefined &&
        formData.requestMaxLimit !== null
        ? [
          Validator.minValue(
            requestMaxLimitNum || 0,
            1,
            "Request max limit must be at least 1",
          ),
          Validator.required(
            formData.requestResetDuration,
            "Request reset duration is required",
          ),
        ]
        : []),
    ]);
  }, [formData, tokenMaxLimitNum, requestMaxLimitNum]);

  const updateField = <K extends keyof TeamFormData>(
    field: K,
    value: TeamFormData[K],
  ) => {
    setFormData((prev) => ({ ...prev, [field]: value }));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (!validator.isValid()) {
      toast.error(validator.getFirstError());
      return;
    }

    // Serialize budget rows whose max_limit was filled in — rows left blank
    // are silently dropped (the backend treats the slice as authoritative).
    const submittableBudgets = formData.budgets
      .filter((r) => r.maxLimit !== undefined && r.maxLimit !== null)
      .map((r) => ({
        max_limit: r.maxLimit as number,
        reset_duration: r.resetDuration,
        calendar_aligned: r.calendarAligned,
      }));

    try {
      if (isEditing && team) {
        // Update existing team
        const updateData: UpdateTeamRequest = {
          name: formData.name,
          customer_id: formData.customerId || undefined,
          // Always send: backend treats `budgets` as a full replacement.
          budgets: submittableBudgets,
        };

        // Detect rate limit changes using had/has pattern
        const hadRateLimit = !!team.rate_limit;
        const hasRateLimit =
          (tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null) ||
          (requestMaxLimitNum !== undefined && requestMaxLimitNum !== null);
        if (hasRateLimit) {
          updateData.rate_limit = {
            token_max_limit: tokenMaxLimitNum,
            token_reset_duration:
              tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null
                ? formData.tokenResetDuration
                : undefined,
            request_max_limit: requestMaxLimitNum,
            request_reset_duration:
              requestMaxLimitNum !== undefined && requestMaxLimitNum !== null
                ? formData.requestResetDuration
                : undefined,
          };
        } else if (hadRateLimit) {
          updateData.rate_limit = {} as UpdateTeamRequest["rate_limit"];
        }

        await updateTeam({ teamId: team.id, data: updateData }).unwrap();
        toast.success("Team updated successfully");
      } else {
        // Create new team
        const createData: CreateTeamRequest = {
          name: formData.name,
          customer_id: formData.customerId || undefined,
          budgets:
            submittableBudgets.length > 0 ? submittableBudgets : undefined,
        };

        // Add rate limit if enabled (token or request limits)
        if (
          (tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null) ||
          (requestMaxLimitNum !== undefined && requestMaxLimitNum !== null)
        ) {
          createData.rate_limit = {
            token_max_limit: tokenMaxLimitNum,
            token_reset_duration:
              tokenMaxLimitNum !== undefined && tokenMaxLimitNum !== null
                ? formData.tokenResetDuration
                : undefined,
            request_max_limit: requestMaxLimitNum,
            request_reset_duration:
              requestMaxLimitNum !== undefined && requestMaxLimitNum !== null
                ? formData.requestResetDuration
                : undefined,
          };
        }

        await createTeam(createData).unwrap();
        toast.success("Team created successfully");
      }

      onSave();
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  return (
    <Dialog open onOpenChange={onCancel}>
      <DialogContent className="sm:max-w-2xl" data-testid="team-dialog-content">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            {isEditing ? "Edit Team" : "Create Team"}
          </DialogTitle>
          <DialogDescription>
            {isEditing
              ? "Update the team information and settings."
              : "Create a new team to organize users and manage shared resources."}
          </DialogDescription>
        </DialogHeader>

        <form onSubmit={handleSubmit} className="">
          <div className="space-y-6">
            {/* Basic Information */}
            <div className="">
              <div className="space-y-2">
                <Label htmlFor="name">Team Name *</Label>
                <Input
                  id="name"
                  placeholder="e.g., Engineering Team"
                  value={formData.name}
                  maxLength={50}
                  onChange={(e) => updateField("name", e.target.value)}
                  data-testid="team-name-input"
                />
              </div>

              {/* Customer Assignment */}
              {customers?.length > 0 && (
                <div className="space-y-2">
                  <Label htmlFor="customer">Customer (optional)</Label>
                  <Select
                    value={formData.customerId || "__none__"}
                    onValueChange={(value) =>
                      updateField(
                        "customerId",
                        value === "__none__" ? "" : value,
                      )
                    }
                  >
                    <SelectTrigger
                      id="customer"
                      className="w-full"
                      data-testid="team-customer-select-trigger"
                    >
                      <SelectValue placeholder="Select a customer" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem
                        value="__none__"
                        data-testid="team-customer-option-none"
                      >
                        None
                      </SelectItem>
                      {customers.map((customer) => (
                        <SelectItem
                          key={customer.id}
                          value={customer.id}
                          data-testid={`team-customer-option-${customer.id}`}
                        >
                          {customer.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className="text-muted-foreground text-sm">
                    Assign to a customer or leave independent.
                  </p>
                </div>
              )}
            </div>

            {/* Multi-budget configuration: one row per budget, each keyed by reset_duration */}
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <Label>Budgets</Label>
                <button
                  type="button"
                  onClick={addBudgetRow}
                  className="text-primary text-xs font-medium hover:underline"
                  data-testid="team-add-budget-btn"
                >
                  + Add budget
                </button>
              </div>
              {formData.budgets.length === 0 && (
                <p className="text-muted-foreground text-xs">
                  No budgets. Click "Add budget" to enforce a spend limit.
                </p>
              )}
              {formData.budgets.map((row, idx) => (
                <div
                  key={row.id}
                  className="space-y-2 rounded-md border p-3"
                  data-testid={`team-budget-row-${idx}`}
                >
                  <div className="flex items-start gap-2">
                    <div className="flex-1">
                      <NumberAndSelect
                        id={`budgetMaxLimit-${idx}`}
                        label={`Budget #${idx + 1} — Maximum Spend (USD)`}
                        value={row.maxLimit}
                        selectValue={row.resetDuration}
                        onChangeNumber={(value) =>
                          updateBudgetRow(idx, { maxLimit: value })
                        }
                        onChangeSelect={(value) => {
                          const patch: Partial<TeamBudgetRow> = {
                            resetDuration: value,
                          };
                          if (!supportsCalendarAlignment(value)) {
                            patch.calendarAligned = false;
                          }
                          updateBudgetRow(idx, patch);
                        }}
                        options={resetDurationOptions}
                        dataTestId={`budget-max-limit-input-${idx}`}
                      />
                    </div>
                    <button
                      type="button"
                      onClick={() => removeBudgetRow(idx)}
                      className="text-muted-foreground hover:text-destructive mt-6 text-xs font-medium"
                      data-testid={`team-remove-budget-btn-${idx}`}
                    >
                      Remove
                    </button>
                  </div>

                  {row.maxLimit !== undefined &&
                    supportsCalendarAlignment(row.resetDuration) && (
                      <div className="flex items-center justify-between gap-4 rounded-md border px-3 py-2">
                        <div className="space-y-0.5">
                          <Label
                            htmlFor={`team-budget-calendar-aligned-toggle-${idx}`}
                            className="text-sm font-normal"
                          >
                            Align to calendar cycle
                          </Label>
                          <p className="text-muted-foreground text-xs">
                            Reset at the start of each period (e.g. 1st of
                            month) instead of rolling from creation date
                          </p>
                        </div>
                        <Switch
                          id={`team-budget-calendar-aligned-toggle-${idx}`}
                          checked={row.calendarAligned}
                          onCheckedChange={(checked) =>
                            handleCalendarAlignedChange(idx, checked)
                          }
                          data-testid={`team-budget-calendar-aligned-toggle-${idx}`}
                        />
                      </div>
                    )}
                </div>
              ))}
            </div>

            {/* Warning dialog shown when enabling calendar alignment on an existing budget */}
            <AlertDialog
              open={showCalendarAlignWarning}
              onOpenChange={(open) => {
                if (!open) setPendingCalendarAlignIdx(null);
              }}
            >
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Reset budget usage?</AlertDialogTitle>
                  <AlertDialogDescription>
                    Enabling calendar alignment will reset this budget&apos;s
                    current usage to{" "}
                    <span className="font-semibold">$0.00</span> and snap the
                    reset date to the start of the current{" "}
                    {pendingCalendarAlignIdx !== null &&
                      formData.budgets[pendingCalendarAlignIdx]?.resetDuration ===
                      "1d"
                      ? "day"
                      : pendingCalendarAlignIdx !== null &&
                        formData.budgets[pendingCalendarAlignIdx]
                          ?.resetDuration === "1w"
                        ? "week"
                        : pendingCalendarAlignIdx !== null &&
                          formData.budgets[pendingCalendarAlignIdx]
                            ?.resetDuration === "1M"
                          ? "month"
                          : pendingCalendarAlignIdx !== null &&
                            formData.budgets[pendingCalendarAlignIdx]
                              ?.resetDuration === "1Y"
                            ? "year"
                            : "period"}
                    . The usage reset to $0.00 cannot be undone, but calendar
                    alignment can be turned off later. This will take effect
                    when you save.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel
                    data-testid="team-calendar-align-cancel-btn"
                    onClick={() => setPendingCalendarAlignIdx(null)}
                  >
                    Cancel
                  </AlertDialogCancel>
                  <AlertDialogAction
                    data-testid="team-calendar-align-enable-btn"
                    onClick={() => {
                      if (pendingCalendarAlignIdx !== null) {
                        updateBudgetRow(pendingCalendarAlignIdx, {
                          calendarAligned: true,
                        });
                      }
                      setPendingCalendarAlignIdx(null);
                    }}
                  >
                    Enable Calendar Alignment
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>

            {/* Rate Limit Configuration - Token Limits */}
            <NumberAndSelect
              id="tokenMaxLimit"
              label="Maximum Tokens"
              value={formData.tokenMaxLimit}
              selectValue={formData.tokenResetDuration}
              onChangeNumber={(value) => updateField("tokenMaxLimit", value)}
              onChangeSelect={(value) =>
                updateField("tokenResetDuration", value)
              }
              options={resetDurationOptions}
            />

            {/* Rate Limit Configuration - Request Limits */}
            <NumberAndSelect
              id="requestMaxLimit"
              label="Maximum Requests"
              value={formData.requestMaxLimit}
              selectValue={formData.requestResetDuration}
              onChangeNumber={(value) => updateField("requestMaxLimit", value)}
              onChangeSelect={(value) =>
                updateField("requestResetDuration", value)
              }
              options={resetDurationOptions}
            />

            {/* Current Usage Section (only shown when editing with existing limits) */}
            {isEditing &&
              ((team?.budgets && team.budgets.length > 0) ||
                team?.rate_limit) && (
                <div className="bg-muted/50 space-y-4 rounded-lg border p-4">
                  <p className="text-sm font-medium">Current Usage</p>
                  <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                    {team?.budgets?.map((b) => (
                      <div key={b.id} className="space-y-1">
                        <p className="text-muted-foreground text-xs">
                          Budget ({b.reset_duration})
                        </p>
                        <div className="flex items-center gap-2">
                          <span className="font-mono text-sm">
                            {formatCurrency(b.current_usage)} /{" "}
                            {formatCurrency(b.max_limit)}
                          </span>
                          <Badge
                            variant={
                              b.max_limit > 0 && b.current_usage >= b.max_limit
                                ? "destructive"
                                : "default"
                            }
                            className="text-xs"
                          >
                            {b.max_limit > 0
                              ? Math.round((b.current_usage / b.max_limit) * 100)
                              : 0}
                            %
                          </Badge>
                        </div>
                        <p className="text-muted-foreground text-xs">
                          Last Reset:{" "}
                          {formatDistanceToNow(new Date(b.last_reset), {
                            addSuffix: true,
                          })}
                        </p>
                      </div>
                    ))}
                    {team?.rate_limit?.token_max_limit && (
                      <div className="space-y-1">
                        <p className="text-muted-foreground text-xs">Tokens</p>
                        <div className="flex items-center gap-2">
                          <span className="font-mono text-sm">
                            {team.rate_limit.token_current_usage.toLocaleString()}{" "}
                            / {team.rate_limit.token_max_limit.toLocaleString()}
                          </span>
                          <Badge
                            variant={
                              team.rate_limit.token_max_limit > 0 &&
                                team.rate_limit.token_current_usage >=
                                team.rate_limit.token_max_limit
                                ? "destructive"
                                : "default"
                            }
                            className="text-xs"
                          >
                            {team.rate_limit.token_max_limit > 0
                              ? Math.round(
                                (team.rate_limit.token_current_usage /
                                  team.rate_limit.token_max_limit) *
                                100,
                              )
                              : 0}
                            %
                          </Badge>
                        </div>
                        <p className="text-muted-foreground text-xs">
                          Last Reset:{" "}
                          {formatDistanceToNow(
                            new Date(team.rate_limit.token_last_reset),
                            { addSuffix: true },
                          )}
                        </p>
                      </div>
                    )}
                    {team?.rate_limit?.request_max_limit && (
                      <div className="space-y-1">
                        <p className="text-muted-foreground text-xs">Requests</p>
                        <div className="flex items-center gap-2">
                          <span className="font-mono text-sm">
                            {team.rate_limit.request_current_usage.toLocaleString()}{" "}
                            / {team.rate_limit.request_max_limit.toLocaleString()}
                          </span>
                          <Badge
                            variant={
                              team.rate_limit.request_max_limit > 0 &&
                                team.rate_limit.request_current_usage >=
                                team.rate_limit.request_max_limit
                                ? "destructive"
                                : "default"
                            }
                            className="text-xs"
                          >
                            {team.rate_limit.request_max_limit > 0
                              ? Math.round(
                                (team.rate_limit.request_current_usage /
                                  team.rate_limit.request_max_limit) *
                                100,
                              )
                              : 0}
                            %
                          </Badge>
                        </div>
                        <p className="text-muted-foreground text-xs">
                          Last Reset:{" "}
                          {formatDistanceToNow(
                            new Date(team.rate_limit.request_last_reset),
                            { addSuffix: true },
                          )}
                        </p>
                      </div>
                    )}
                  </div>
                </div>
              )}
          </div>

          <FormFooter
            validator={validator}
            label="Team"
            onCancel={onCancel}
            isLoading={loading}
            isEditing={isEditing}
            hasPermission={hasPermission}
          />
        </form>
      </DialogContent>
    </Dialog>
  );
}
