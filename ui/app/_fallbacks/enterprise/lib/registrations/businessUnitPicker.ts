// OSS-build fallback for the enterprise business unit picker registration.
//
// Side-effect imports of this module from OSS code (the virtual key sheet)
// compile to a no-op when the @enterprise alias resolves to _fallbacks/. The
// enterprise build replaces this module with one that registers the async
// business unit picker, which is what reveals the "Business unit" assignment
// option.
export {};