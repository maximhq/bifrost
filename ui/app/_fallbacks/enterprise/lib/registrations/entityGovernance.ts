// OSS-build fallback for the enterprise entity governance probe registration.
//
// Side-effect imports of this module from OSS code (the customer sheet) compile to a no-op when
// the @enterprise alias resolves to _fallbacks/. The enterprise build replaces this module with
// one that registers a probe reading the entity's access profile, which is what hides an entity's
// own limit fields while a profile governs it.
export {};