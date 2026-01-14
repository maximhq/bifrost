import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
	return twMerge(clsx(inputs));
}

export const isURLValid = (input: string) => {
	try {
		const url = new URL(input);
		return url.protocol === "http:" || url.protocol === "https:";
	} catch (_) {
		return false;
	}
};

export function keyify(obj: any, prefix = ""): string[] {
	if (typeof obj !== "object" || obj === null) {
		return [];
	}
	return Object.keys(obj).reduce((res: any, el: any) => {
		if (typeof obj[el] === "object" && obj[el] !== null) {
			if (Array.isArray(obj[el])) {
				return [...res, prefix + el, ...keyify(obj[el], prefix + el + ".")];
			}
			return [...res, ...keyify(obj[el], prefix + el + ".")];
		}
		return [...res, prefix + el];
	}, []);
}

export interface NumberObject {
	[key: string]: number;
}

export function calculateAverageByKeys(numbersArray: NumberObject[]): NumberObject | null {
	// Check if the array is empty
	if (numbersArray.length === 0) {
		return null;
	}

	const result: NumberObject = {};
	const keys = Object.keys(numbersArray[0]);

	for (const key of keys) {
		let sum = 0;
		let count = 0;

		for (const obj of numbersArray) {
			if (typeof obj[key] === "number") {
				sum += obj[key];
				count++;
			}
		}

		result[key] = count > 0 ? sum / count : 0;
	}

	return result;
}

export function capitalizeString(str: string): string {
	return str.charAt(0).toUpperCase() + str.slice(1);
}

export function parseURL(url: string): URL {
	return new URL(url);
}

export function getQueryParams(url: string): Record<string, string> {
	try {
		const queryParams = parseURL(url).searchParams;
		const result: Record<string, string> = {};
		queryParams.forEach((value, key) => {
			result[key] = value;
		});
		return result;
	} catch {
		return {};
	}
}

export const radixDialogOnBlurWorkaround = (event: React.FocusEvent<HTMLInputElement | HTMLTextAreaElement>) => {
	const element = event.relatedTarget as HTMLElement;
	if (element?.getAttribute("role") === "dialog" || element?.getAttribute("role") === "tabpanel") return;
	element?.focus();
};

export const defaultGaugeColors = {
	"0": "#DC3412",
	"80": "#22c55d",
};

export const tailwindUtilityClasses = {
	textLabel: "text-md font-medium",
	textBodySmall: "text-sm font-normal",
	textBodyMedium: "text-md font-normal",
	textBodyLarge: "text-lg font-normal",
} as const;
