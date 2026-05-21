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
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label } from "@/components/ui/label";
import NumberAndSelect from "@/components/ui/numberAndSelect";
import { UsageLine } from "@/components/ui/usageLine";
import { resetDurationLabels, resetDurationOptions } from "@/lib/constants/governance";
import {
  getErrorMessage,
  useDeleteGlobalGovernanceMutation,
  useGetGlobalGovernanceQuery,
  useUpdateGlobalGovernanceMutation,
} from "@/lib/store";
import { CreateBudgetRequest, UpdateRateLimitRequest } from "@/lib/types/governance";
import { formatCurrency, parseResetPeriod } from "@/lib/utils/governance";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { formatDistanceToNow } from "date-fns";

import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { v4 as uuid } from "uuid";

interface BudgetRow {
  clientId: string;
  id?: string;
  maxLimit: number | undefined;
  resetDuration: string;
}

interface FormState {
  budgets: BudgetRow[];
  tokenMaxLimit: number | undefined;
  tokenResetDuration: string;
  requestMaxLimit: number | undefined;
  requestResetDuration: string;
}

const DEFAULT_FORM: FormState = {
  budgets: [],
  tokenMaxLimit: undefined,
  tokenResetDuration: "1h",
  requestMaxLimit: undefined,
  requestResetDuration: "1h",
};

export default function GlobalGovernanceCard() {
  const hasViewAccess = useRbac(RbacResource.Governance, RbacOperation.View);
  const hasUpdateAccess = useRbac(RbacResource.Governance, RbacOperation.Update);

  const { data, isLoading, error } = useGetGlobalGovernanceQuery(undefined, {
    skip: !hasViewAccess,
    pollingInterval: 5000,
  });
  const [updateGlobal, { isLoading: isUpdating }] = useUpdateGlobalGovernanceMutation();
  const [deleteGlobal, { isLoading: isDeleting }] = useDeleteGlobalGovernanceMutation();

  const [isEditing, setIsEditing] = useState(false);
  const [form, setForm] = useState<FormState>(DEFAULT_FORM);
  const [showBudgetResetPrompt, setShowBudgetResetPrompt] = useState(false);
  const [pendingForm, setPendingForm] = useState<FormState | null>(null);
  const prevErrorRef = useRef<typeof error>(undefined);

  useEffect(() => {
    if (!isEditing && data) {
      setForm({
        budgets:
          data.budgets?.map((b) => ({
            clientId: b.id,
            id: b.id,
            maxLimit: b.max_limit,
            resetDuration: b.reset_duration,
          })) ?? [],
        tokenMaxLimit: data.rate_limit?.token_max_limit ?? undefined,
        tokenResetDuration: data.rate_limit?.token_reset_duration || "1h",
        requestMaxLimit: data.rate_limit?.request_max_limit ?? undefined,
        requestResetDuration: data.rate_limit?.request_reset_duration || "1h",
      });
    }
  }, [data, isEditing]);

  useEffect(() => {
    if (error && error !== prevErrorRef.current) {
      toast.error(`Failed to load global limits: ${getErrorMessage(error)}`);
    }
    prevErrorRef.current = error;
  }, [error]);

  const activeBudgets = form.budgets.filter((b) => b.maxLimit !== undefined && b.maxLimit > 0 && b.resetDuration);
  const resetDurationCounts = activeBudgets.reduce<Record<string, number>>((acc, b) => {
    acc[b.resetDuration] = (acc[b.resetDuration] ?? 0) + 1;
    return acc;
  }, {});
  const duplicateResetDurations = new Set(
    Object.entries(resetDurationCounts)
      .filter(([, count]) => count > 1)
      .map(([dur]) => dur),
  );
  const hasDuplicateBudgetPeriods = duplicateResetDurations.size > 0;

  const hasBudgetResetRelevantChanges = (f: FormState) => {
    if (!isEditing || !data?.budgets?.length) return false;
    const existingById = new Map((data.budgets ?? []).map((b) => [b.id, b]));
    return f.budgets.some((row) => {
      if (!row.id) return false;
      const existing = existingById.get(row.id);
      if (!existing) return false;
      return row.maxLimit !== existing.max_limit || row.resetDuration !== existing.reset_duration;
    });
  };

  const doSave = async (f: FormState, resetBudgetUsage: boolean) => {
    const budgets: CreateBudgetRequest[] = f.budgets
      .filter((b) => b.maxLimit !== undefined && b.maxLimit > 0 && b.resetDuration)
      .map((b) => ({
        ...(b.id ? { id: b.id } : {}),
        max_limit: b.maxLimit!,
        reset_duration: b.resetDuration,
      }));

    const hadRateLimit = !!(data?.rate_limit?.token_max_limit || data?.rate_limit?.request_max_limit);
    const hasRateLimit = !!(f.tokenMaxLimit || f.requestMaxLimit);

    let rateLimit: UpdateRateLimitRequest | undefined;
    if (hasRateLimit) {
      rateLimit = {
        token_max_limit: f.tokenMaxLimit || null,
        token_reset_duration: f.tokenMaxLimit ? f.tokenResetDuration : undefined,
        request_max_limit: f.requestMaxLimit || null,
        request_reset_duration: f.requestMaxLimit ? f.requestResetDuration : undefined,
      };
    } else if (hadRateLimit) {
      rateLimit = {} as UpdateRateLimitRequest;
    }

    try {
      await updateGlobal({ budgets, rate_limit: rateLimit, ...(resetBudgetUsage ? { reset_budget_usage: true } : {}) }).unwrap();
      toast.success("Global limits updated");
      setIsEditing(false);
    } catch (err) {
      toast.error(`Failed to update: ${getErrorMessage(err)}`);
    }
  };

  const handleSave = async () => {
    if (hasBudgetResetRelevantChanges(form)) {
      setPendingForm(form);
      setShowBudgetResetPrompt(true);
      return;
    }
    await doSave(form, false);
  };

  const handleBudgetResetChoice = async (resetBudgetUsage: boolean) => {
    if (!pendingForm) return;
    const f = pendingForm;
    setPendingForm(null);
    setShowBudgetResetPrompt(false);
    await doSave(f, resetBudgetUsage);
  };

  const handleDeleteAll = async () => {
    try {
      await deleteGlobal().unwrap();
      toast.success("Global limits removed");
      setIsEditing(false);
    } catch (err) {
      toast.error(`Failed to remove: ${getErrorMessage(err)}`);
    }
  };

  const budgetResetDialog = (
    <AlertDialog open={showBudgetResetPrompt} onOpenChange={setShowBudgetResetPrompt}>
      <AlertDialogContent data-testid="global-gov-budget-reset-dialog">
        <AlertDialogHeader>
          <AlertDialogTitle>Reset budget usage?</AlertDialogTitle>
          <AlertDialogDescription>
            You changed a budget amount or reset frequency. Reset current budget usage to 0, or preserve the existing usage counters.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel
            onClick={() => handleBudgetResetChoice(false)}
            data-testid="global-gov-budget-reset-preserve-btn"
          >
            Preserve Usage
          </AlertDialogCancel>
          <AlertDialogAction
            onClick={() => handleBudgetResetChoice(true)}
            data-testid="global-gov-budget-reset-confirm-btn"
          >
            Reset Usage
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );

  if (isLoading) return null;

  const hasBudgets = (data?.budgets?.length ?? 0) > 0;
  const hasRateLimit = !!(data?.rate_limit?.token_max_limit || data?.rate_limit?.request_max_limit);
  const hasAnyLimit = hasBudgets || hasRateLimit;

  if (!hasAnyLimit && !isEditing) {
    return (
      <>
        {budgetResetDialog}
        <Card>
          <CardHeader>
            <CardTitle>Global Limits</CardTitle>
            <CardDescription>
              Global spend cap and throughput limit evaluated before any virtual key, team, customer, or model check.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground mb-4">
              No global limits configured. All traffic passes through to entity-level checks.
            </p>
            {hasUpdateAccess && (
              <Button data-testid="global-gov-configure-button" onClick={() => setIsEditing(true)}>Configure limits</Button>
            )}
          </CardContent>
        </Card>
      </>
    );
  }

  if (isEditing) {
    return (
      <>
      {budgetResetDialog}
      <Card>
        <CardHeader>
          <CardTitle>Global Limits</CardTitle>
          <CardDescription>Evaluated before all other governance tiers.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {/* Budgets */}
          <div className="space-y-3">
            <Label>Spend Budgets</Label>
            {form.budgets.map((row, idx) => (
              <div key={row.clientId} className="flex items-end gap-2">
                <div className="flex-1">
                  <NumberAndSelect
                    id={`budget-${row.clientId}`}
                    label="Max spend (USD)"
                    value={row.maxLimit}
                    onChangeNumber={(v) =>
                      setForm((prev) => ({
                        ...prev,
                        budgets: prev.budgets.map((b, i) => (i === idx ? { ...b, maxLimit: v } : b)),
                      }))
                    }
                    selectValue={row.resetDuration}
                    onChangeSelect={(v) =>
                      setForm((prev) => ({
                        ...prev,
                        budgets: prev.budgets.map((b, i) => (i === idx ? { ...b, resetDuration: v } : b)),
                      }))
                    }
                    options={resetDurationOptions}
                  />
                </div>
                <Button
                  variant="ghost"
                  size="sm"
                  data-testid={`global-gov-remove-budget-${idx}`}
                  onClick={() =>
                    setForm((prev) => ({
                      ...prev,
                      budgets: prev.budgets.filter((_, i) => i !== idx),
                    }))
                  }
                >
                  Remove
                </Button>
              </div>
            ))}
            <Button
              variant="outline"
              size="sm"
              data-testid="global-gov-add-budget-button"
              onClick={() =>
                setForm((prev) => ({
                  ...prev,
                  budgets: [
                    ...prev.budgets,
                    { clientId: uuid(), maxLimit: undefined, resetDuration: "1M" },
                  ],
                }))
              }
            >
              Add budget
            </Button>
            {hasDuplicateBudgetPeriods && (
              <p className="text-sm text-destructive">
                Each budget must have a unique reset period. Duplicate:{" "}
                {[...duplicateResetDurations].map((d) => resetDurationLabels[d] ?? d).join(", ")}.
              </p>
            )}
          </div>

          {/* Rate Limit */}
          <div className="space-y-3">
            <Label>Rate Limits</Label>
            <NumberAndSelect
              id="token-limit"
              label="Token limit"
              value={form.tokenMaxLimit}
              onChangeNumber={(v) => setForm((prev) => ({ ...prev, tokenMaxLimit: v }))}
              selectValue={form.tokenResetDuration}
              onChangeSelect={(v) => setForm((prev) => ({ ...prev, tokenResetDuration: v }))}
              options={resetDurationOptions}
            />
            <NumberAndSelect
              id="request-limit"
              label="Request limit"
              value={form.requestMaxLimit}
              onChangeNumber={(v) => setForm((prev) => ({ ...prev, requestMaxLimit: v }))}
              selectValue={form.requestResetDuration}
              onChangeSelect={(v) => setForm((prev) => ({ ...prev, requestResetDuration: v }))}
              options={resetDurationOptions}
            />
          </div>

          <div className="flex gap-2 pt-2">
            {hasAnyLimit && (
              <Button
                variant="destructive"
                data-testid="global-gov-remove-all-button"
                onClick={handleDeleteAll}
                disabled={isUpdating || isDeleting}
              >
                {isDeleting ? "Removing…" : "Remove all limits"}
              </Button>
            )}
            <div className="flex gap-2 ml-auto">
              <Button
                variant="outline"
                data-testid="global-gov-cancel-button"
                onClick={() => setIsEditing(false)}
                disabled={isUpdating || isDeleting}
              >
                Cancel
              </Button>
              <Button data-testid="global-gov-save-button" onClick={handleSave} disabled={isUpdating || isDeleting || hasDuplicateBudgetPeriods}>
                {isUpdating ? "Saving…" : "Save"}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
      </>
    );
  }

  return (
    <>
    {budgetResetDialog}
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <div>
          <CardTitle>Global Limits</CardTitle>
          <CardDescription>Evaluated before all other governance tiers.</CardDescription>
        </div>
        {hasUpdateAccess && (
          <Button variant="outline" data-testid="global-gov-edit-button" onClick={() => setIsEditing(true)}>
            Edit
          </Button>
        )}
      </CardHeader>
      <CardContent className="space-y-6">
        {hasBudgets && (
          <div className="space-y-3">
            <Label>Spend Budgets</Label>
            {data!.budgets.map((b) => (
              <div key={b.id} className="space-y-2">
                <UsageLine current={b.current_usage} max={b.max_limit} format={formatCurrency} />
                <div className="text-muted-foreground flex items-center justify-between text-xs">
                  <span>Resets {parseResetPeriod(b.reset_duration)}</span>
                  {b.last_reset && (
                    <span>Last reset {formatDistanceToNow(new Date(b.last_reset), { addSuffix: true })}</span>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}

        {hasRateLimit && (
          <div className="space-y-3">
            <Label>Rate Limits</Label>
            {data!.rate_limit!.token_max_limit != null && (
              <div className="space-y-2">
                <span className="text-muted-foreground text-xs font-medium">TOKEN LIMITS</span>
                <UsageLine
                  current={data!.rate_limit!.token_current_usage}
                  max={data!.rate_limit!.token_max_limit}
                  format={(n) => n.toLocaleString()}
                />
                <div className="text-muted-foreground flex items-center justify-between text-xs">
                  <span>Resets {parseResetPeriod(data!.rate_limit!.token_reset_duration!)}</span>
                  {data!.rate_limit!.token_last_reset && (
                    <span>Last reset {formatDistanceToNow(new Date(data!.rate_limit!.token_last_reset), { addSuffix: true })}</span>
                  )}
                </div>
              </div>
            )}
            {data!.rate_limit!.request_max_limit != null && (
              <div className="space-y-2">
                <span className="text-muted-foreground text-xs font-medium">REQUEST LIMITS</span>
                <UsageLine
                  current={data!.rate_limit!.request_current_usage}
                  max={data!.rate_limit!.request_max_limit}
                  format={(n) => n.toLocaleString()}
                />
                <div className="text-muted-foreground flex items-center justify-between text-xs">
                  <span>Resets {parseResetPeriod(data!.rate_limit!.request_reset_duration!)}</span>
                  {data!.rate_limit!.request_last_reset && (
                    <span>Last reset {formatDistanceToNow(new Date(data!.rate_limit!.request_last_reset), { addSuffix: true })}</span>
                  )}
                </div>
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
    </>
  );
}
