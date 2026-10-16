import * as React from "react";

const MOBILE_BREAKPOINT = 768;

export function useIsMobile() {
	const [isMobile, setIsMobile] = React.useState<boolean | undefined>(undefined);

	React.useEffect(() => {
		const mql = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`);
		const onChange = () => {
			setIsMobile(window.innerWidth < MOBILE_BREAKPOINT);
		};
		mql.addEventListener("change", onChange);
		setIsMobile(window.innerWidth < MOBILE_BREAKPOINT);
		return () => mql.removeEventListener("change", onChange);
	}, []);

	return !!isMobile;
}
/**
 * Whether the viewport is narrower than `breakpoint`.
 *
 * useIsMobile answers "is this a phone", which is not the same question as
 * "is there room for a side panel": a 768px tablet is not mobile but cannot
 * hold a 240px sidebar, a 400px dock and a readable page at once.
 */
export function useIsNarrowerThan(breakpoint: number) {
	const [narrow, setNarrow] = React.useState<boolean | undefined>(undefined);

	React.useEffect(() => {
		const mql = window.matchMedia(`(max-width: ${breakpoint - 1}px)`);
		const onChange = () => setNarrow(window.innerWidth < breakpoint);
		mql.addEventListener("change", onChange);
		setNarrow(window.innerWidth < breakpoint);
		return () => mql.removeEventListener("change", onChange);
	}, [breakpoint]);

	return !!narrow;
}