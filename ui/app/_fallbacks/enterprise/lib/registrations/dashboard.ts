// OSS-build fallback for the dashboard registrations.
//
// The side-effect import of this module from the dashboard's Overview tab compiles to a no-op
// when the @enterprise alias resolves to _fallbacks/. A downstream build replaces this module
// with one that registers its Overview charts.
export {};