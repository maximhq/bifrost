// OSS-build fallback for the enterprise entity governance probe registration.
//
// Side-effect imports of this module from OSS code (the customer sheet) compile to a no-op when
// the @enterprise alias resolves to _fallbacks/. The enterprise build replaces this module with
// one that registers the probe, which is what hides an entity's own limit fields while an access
// profile governs it.
export {};