import i18n from "@/lib/i18n";
import type { ParseKeys } from "i18next";

// Lazy validation message: resolved when a check fails, so it follows the active locale instead of the one at load time.
export const msg = (key: ParseKeys<"common">) => ({ error: () => i18n.t(key) });