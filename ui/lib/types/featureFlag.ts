// FeatureFlagSource mirrors the Go featureflags.Source enum. The four
// layers are surfaced in the UI so operators can see exactly why a flag is
// on or off (e.g. a "file" badge means values.yaml or config.json pinned it).
export type FeatureFlagSource = "default" | "db" | "remote" | "file";

export interface FeatureFlagStatus {
	// id is the stable identifier used by code (useFeatureFlag(id)), the
	// URL path, and the DB key. Lowercase, dotted/dashed, URL-safe.
	id: string;
	// display_name is the human-readable label shown in the UI. Free text.
	display_name: string;
	description: string;
	default: boolean;
	enabled: boolean;
	source: FeatureFlagSource;
	// True when the value came from config.json / Helm and cannot be
	// toggled via the UI. The toggle is rendered disabled in this case.
	locked: boolean;
	// False for stale entries: a value lives in DB or config.json but no
	// code currently calls featureflags.Register(id). Surfaced as a
	// badge in the UI so operators can investigate (the fix is removing
	// the stale entry from values.yaml, or restoring the Register() call).
	registered: boolean;
	// True when the flag's registration is marked EnterpriseOnly. The
	// backend forces enabled=false and locked=true in OSS mode; the UI
	// shows an "Enterprise" badge so operators can see the feature exists
	// but is gated. In enterprise builds the flag toggles normally and
	// this field is informational.
	enterprise_only: boolean;
	updated_at?: number;
}

export interface FeatureFlagsListResponse {
	flags: FeatureFlagStatus[];
}

export interface UpdateFeatureFlagRequest {
	id: string;
	enabled: boolean;
}