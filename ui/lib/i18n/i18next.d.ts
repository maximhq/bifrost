import "i18next";

import type { resources } from "./resources";

// Arrays (e.g. warp.errors.*.suggestions, read with returnObjects) are typed as
// leaves. Left as-is, i18next expands them into `${number}` keys crossed with
// plural suffixes, which blows TypeScript's instantiation limit (TS2589).
type ArraysAsLeaves<T> = T extends readonly unknown[] ? string : T extends object ? { [K in keyof T]: ArraysAsLeaves<T[K]> } : T;

// Types t() keys against the English catalogs so editors autocomplete them and
// unknown keys fail the typecheck. English is the source of truth; other locales
// are expected to mirror its shape.
declare module "i18next" {
	interface CustomTypeOptions {
		defaultNS: "common";
		// Matches the runtime init; also halves the key union TypeScript has to build.
		nsSeparator: false;
		resources: ArraysAsLeaves<(typeof resources)["en"]>;
	}
}