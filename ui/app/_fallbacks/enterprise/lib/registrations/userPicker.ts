// OSS-build fallback for the enterprise user picker registration.
//
// Side-effect imports of this module from OSS code (the pricing override
// and routing rule sheets) compile to a no-op when the @enterprise alias
// resolves to _fallbacks/. The enterprise build replaces this module with
// one that registers the async user picker, which is what reveals the
// "User" scope options.
export {};
