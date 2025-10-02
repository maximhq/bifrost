"use client";

import { useCallback, useEffect, useRef, useState } from "react";

export function useDebouncedValue(value: any, delay: number): any {
	const [debouncedValue, setDebouncedValue] = useState(value);

	useEffect(() => {
		const handler = setTimeout(() => {
			setDebouncedValue(value);
		}, delay);

		return () => {
			clearTimeout(handler);
		};
	}, [value, delay]);

	return debouncedValue;
}

export const useDebouncedFunction = <T extends (...args: any[]) => any>(func: T, delay: number): ((...args: Parameters<T>) => void) => {
	const timeoutRef = useRef<NodeJS.Timeout | null>(null);

	const debouncedFunction = useCallback(
		(...args: Parameters<T>) => {
			if (timeoutRef.current) {
				clearTimeout(timeoutRef.current);
			}
			timeoutRef.current = setTimeout(() => func(...args), delay);
		},
		[func, delay],
	);

	return debouncedFunction;
};
