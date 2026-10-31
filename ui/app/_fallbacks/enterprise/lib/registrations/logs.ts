// OSS-build fallback for the Logs page registrations.
//
// Side-effect imports of this module from OSS code (the log sheet and the logs filter sidebar)
// compile to a no-op when the @enterprise alias resolves to _fallbacks/. A downstream build
// replaces this module with one that registers its Logs page additions.
export {};