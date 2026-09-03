import { en, type Messages } from "./messages/en";
import { zh } from "./messages/zh";
import { DEFAULT_LOCALE, type Locale, type MessageParams } from "./types";

export const messagesByLocale: Record<Locale, Messages> = { en, zh };

type Join<K, P> = K extends string ? (P extends string ? `${K}.${P}` : never) : never;

type NestedKeys<T> = T extends object
	? {
			[K in keyof T & string]: T[K] extends object ? (T[K] extends string ? K : K | Join<K, NestedKeys<T[K]>>) : K;
		}[keyof T & string]
	: never;

export type MessageKey = NestedKeys<Messages>;

function lookup(messages: Messages, key: string): string | undefined {
	let current: unknown = messages;
	for (const part of key.split(".")) {
		if (current == null || typeof current !== "object" || !(part in current)) {
			return undefined;
		}
		current = (current as Record<string, unknown>)[part];
	}
	return typeof current === "string" ? current : undefined;
}

export function interpolate(template: string, params?: MessageParams): string {
	if (!params) return template;
	return template.replace(/\{\{(\w+)\}\}/g, (_, name: string) => {
		const value = params[name];
		return value == null ? `{{${name}}}` : String(value);
	});
}

export function pluralKey(key: MessageKey, count: number): MessageKey {
	const suffix = count === 1 ? "_one" : "_other";
	return `${key}${suffix}` as MessageKey;
}

export function translate(locale: Locale, key: MessageKey, params?: MessageParams): string {
	const catalog = messagesByLocale[locale] ?? messagesByLocale[DEFAULT_LOCALE];
	const template = lookup(catalog, key) ?? lookup(messagesByLocale[DEFAULT_LOCALE], key) ?? key;
	return interpolate(template, params);
}

export type TranslateFn = (key: MessageKey, params?: MessageParams) => string;