import { Flag } from "lucide-react";

// FeatureFlagsEmptyState renders when no flags are registered in code AND
// no overrides exist in DB/config. Unlike plugins or providers, flags
// cannot be created from the UI — they are declared via
// featureflags.Register(...) in Go code — so this state is informational
// only and offers no action button. The framing points operators at the
// code-side registration so they know where to look.
export function FeatureFlagsEmptyState() {
  return (
    <div
      className="flex min-h-[60vh] w-full flex-col items-center justify-center gap-4 py-16 text-center"
      data-testid="feature-flags-empty-state"
    >
      <div className="text-muted-foreground">
        <Flag className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />
      </div>
      <div className="flex flex-col gap-1">
        <h1 className="text-muted-foreground text-xl font-medium">
          No feature flags registered yet
        </h1>
      </div>
    </div>
  );
}
