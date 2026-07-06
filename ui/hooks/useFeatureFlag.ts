import { useListFeatureFlagsQuery } from "@/lib/store/apis/featureFlagsApi";
import type { FeatureFlagStatus } from "@/lib/types/featureFlag";

/**
 * useFeatureFlag returns a single boolean: whether the named flag is enabled
 * for the current user/process. Designed for guarding UI: render the new
 * variant when true, the old when false.
 *
 * The argument is the flag's `id` (the stable identifier registered in
 * code), NOT its display name.
 *
 * Usage:
 *   const isNewDashboard = useFeatureFlag("ui.new-dashboard");
 *   return isNewDashboard ? <NewDashboard /> : <OldDashboard />;
 *
 * Defaults to `false` until the list query resolves, and also for any flag
 * id the server doesn't know about. "Off until proven on" is the safer
 * default for guarding new UI - users see the existing behavior while the
 * query is in flight, not a flash of an unreleased variant.
 *
 * The hook uses RTK Query's selectFromResult so this component only
 * re-renders when THIS flag's value changes, not when any flag in the list
 * is toggled. This keeps the hook cheap to use in many places at once.
 */
export function useFeatureFlag(id: string): boolean {
	const { isEnabled } = useListFeatureFlagsQuery(undefined, {
		selectFromResult: (result) => ({
			isEnabled: findEnabled(result.data?.flags, id),
		}),
	});
	return isEnabled;
}

/**
 * useFeatureFlagStatus returns the full FlagStatus (or undefined if the
 * flag is unknown), useful for advanced cases like showing a "locked"
 * indicator in admin tooling rather than just the boolean.
 */
export function useFeatureFlagStatus(id: string): FeatureFlagStatus | undefined {
	const { status } = useListFeatureFlagsQuery(undefined, {
		selectFromResult: (result) => ({
			status: result.data?.flags.find((f) => f.id === id),
		}),
	});
	return status;
}

function findEnabled(flags: FeatureFlagStatus[] | undefined, id: string): boolean {
	if (!flags) return false;
	const match = flags.find((f) => f.id === id);
	return match?.enabled ?? false;
}