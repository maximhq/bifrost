import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import type { ReactNode } from "react";

interface ChartCardProps {
  title: string;
  children: ReactNode;
  headerActions?: ReactNode;
  loading?: boolean;
  testId?: string;
  className?: string;
}

export function ChartCard({
  title,
  children,
  headerActions,
  loading,
  testId,
  className,
}: ChartCardProps) {
  if (loading) {
    return (
      <Card
        className={cn("min-w-0 rounded-sm p-2 shadow-none h-[330px]", className)}
        data-testid={testId}
      >
        <div className="shrink-0 space-y-2">
          <span className="text-primary pl-2 text-sm font-medium">{title}</span>
          {headerActions && (
            <div
              className="w-full min-w-0"
              data-testid={testId ? `${testId}-actions` : undefined}
            >
              {headerActions}
            </div>
          )}
        </div>
        <div
          className="grow"
          data-testid={testId ? `${testId}-chart-skeleton` : undefined}
        >
          <Skeleton className="h-full w-full" />
        </div>
      </Card>
    );
  }

  return (
    <Card
      className={cn("min-w-0 rounded-sm p-2 shadow-none h-[330px]", className)}
      data-testid={testId}
    >
      <div className="shrink-0 space-y-2">
        <span className="text-primary pl-2 text-sm font-medium">{title}</span>
        {headerActions && (
          <div
            className="w-full min-w-0"
            data-testid={testId ? `${testId}-actions` : undefined}
          >
            {headerActions}
          </div>
        )}
      </div>
      <div className="grow">{children}</div>
    </Card>
  );
}
