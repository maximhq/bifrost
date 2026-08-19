// OSS-build fallback for the enterprise scope registrations.
//
// Side-effect imports of this module from OSS code (e.g. from the Model
// Limits page) compile to a no-op when the @enterprise alias resolves to
// _fallbacks/. The enterprise build replaces this module with one that
// registers the "user" scope (and its picker + deep-link).
export {};