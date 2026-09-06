import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { getErrorMessage } from "@/lib/store";
import {
  useCancelWarpTopicsMutation,
  useGetWarpTopicsStatusQuery,
  useStartWarpTopicsMutation,
} from "@/lib/store/apis/warpApi";
import { Loader2 } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { localDateTimeValue } from "./warpConfig.utils";

interface WarpTopicsSectionProps {
  hasSettingsUpdateAccess: boolean;
  configured: boolean;
  vectorStoreConnected: boolean;
  /** Unsaved form edits. A run against settings that are about to change is wasted. */
  hasChanges: boolean;
}

/**
 * Computes topic clusters over the indexed conversations.
 *
 * "What do people ask about?" has no aggregate in the log store; the answer is
 * in the meaning of the conversations, which the log vectors already encode.
 * This job clusters them, names each cluster through Warp's model, and stores
 * one centroid per topic so the list_topics tool can answer in one call.
 * Same job shape as the backfill: a window, Sidekiq, cancel, resumable status.
 */
export default function WarpTopicsSection({
  hasSettingsUpdateAccess,
  configured,
  vectorStoreConnected,
  hasChanges,
}: WarpTopicsSectionProps) {
  const [startTopics, { isLoading: isStarting }] = useStartWarpTopicsMutation();
  const [cancelTopics, { isLoading: isCancelling }] =
    useCancelWarpTopicsMutation();
  const [activeID, setActiveID] = useState<string | null>(null);
  const [windowStart, setWindowStart] = useState(() =>
    localDateTimeValue(new Date(Date.now() - 30 * 24 * 60 * 60 * 1000)),
  );
  const [windowEnd, setWindowEnd] = useState(() =>
    localDateTimeValue(new Date()),
  );
  const { data: status } = useGetWarpTopicsStatusQuery(
    activeID ? { id: activeID } : undefined,
    {
      skip: !hasSettingsUpdateAccess || !configured,
      pollingInterval: activeID ? 2000 : 10000,
    },
  );
  const isActive =
    status?.status === "pending" ||
    status?.status === "running" ||
    status?.status === "cancelling";

  const onStart = async () => {
    if (
      !windowStart ||
      !windowEnd ||
      new Date(windowStart) >= new Date(windowEnd)
    ) {
      toast.error("Choose a valid time range for topics.");
      return;
    }
    try {
      const started = await startTopics({
        start_time: new Date(windowStart).toISOString(),
        end_time: new Date(windowEnd).toISOString(),
      }).unwrap();
      setActiveID(started.id ?? null);
      toast.success("Topic clustering started.");
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  const onCancel = async () => {
    try {
      await cancelTopics(activeID ? { id: activeID } : undefined).unwrap();
      toast.success("Topic clustering cancellation requested.");
    } catch (error) {
      toast.error(getErrorMessage(error));
    }
  };

  return (
    <div
      className="space-y-4 rounded-sm border p-4"
      data-testid="warp-topics-section"
    >
      <div className="space-y-0.5">
        <Label>Topic clusters</Label>
        <p className="text-muted-foreground text-sm">
          Group indexed conversations by meaning and name each group, so Warp
          can answer what people ask about. Runs in Sidekiq over the window
          below and replaces the previous topics when it finishes.
        </p>
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="warp-topics-start">Start</Label>
          <Input
            id="warp-topics-start"
            type="datetime-local"
            data-testid="warp-topics-start-input"
            value={windowStart}
            onChange={(event) => setWindowStart(event.target.value)}
            disabled={isActive || !hasSettingsUpdateAccess}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="warp-topics-end">End</Label>
          <Input
            id="warp-topics-end"
            type="datetime-local"
            data-testid="warp-topics-end-input"
            value={windowEnd}
            onChange={(event) => setWindowEnd(event.target.value)}
            disabled={isActive || !hasSettingsUpdateAccess}
          />
        </div>
      </div>

      {status?.id && (
        <div
          className="bg-muted/40 space-y-2 rounded-sm p-3 text-sm"
          data-testid="warp-topics-status"
        >
          <div className="flex items-center justify-between gap-3">
            <span className="font-medium capitalize">{status.status}</span>
            <span className="text-muted-foreground">
              {status.scanned} conversation{status.scanned === 1 ? "" : "s"}{" "}
              read
            </span>
          </div>
          <p className="text-muted-foreground text-xs">
            {status.indexed} topic{status.indexed === 1 ? "" : "s"} written
            {status.failed > 0
              ? ` · ${status.failed} label${status.failed === 1 ? "" : "s"} fell back`
              : ""}
          </p>
          {status.message && (
            <p className="text-muted-foreground text-xs">{status.message}</p>
          )}
          {status.last_error && (
            <p className="text-destructive text-xs">
              Latest error: {status.last_error}
            </p>
          )}
        </div>
      )}

      <div className="flex justify-end">
        {isActive ? (
          <Button
            type="button"
            variant="outline"
            onClick={onCancel}
            disabled={isCancelling || !hasSettingsUpdateAccess}
            data-testid="warp-topics-cancel-btn"
          >
            {isCancelling && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}{" "}
            Cancel
          </Button>
        ) : (
          <Button
            type="button"
            onClick={onStart}
            disabled={
              isStarting ||
              !configured ||
              !vectorStoreConnected ||
              !hasSettingsUpdateAccess ||
              hasChanges
            }
            data-testid="warp-topics-start-btn"
          >
            {isStarting && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}{" "}
            Compute topics
          </Button>
        )}
      </div>
      {hasChanges && (
        <p className="text-muted-foreground text-right text-xs">
          Save configuration changes before computing topics.
        </p>
      )}
    </div>
  );
}
