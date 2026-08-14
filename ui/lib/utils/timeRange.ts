export const TIME_PERIODS = [
	{ label: "最近 1 小时", value: "1h" },
	{ label: "最近 6 小时", value: "6h" },
	{ label: "最近 24 小时", value: "24h" },
	{ label: "最近 7 天", value: "7d" },
	{ label: "最近 30 天", value: "30d" },
];

export type TimePeriod = (typeof TIME_PERIODS)[number]["value"];

/** Returns a fresh { from, to } Date pair for the given relative period string. */
export function getRangeForPeriod(period: string): { from: Date; to: Date } {
	const to = new Date();
	const from = new Date(to.getTime());
	switch (period) {
		case "1h":
			from.setHours(from.getHours() - 1);
			break;
		case "6h":
			from.setHours(from.getHours() - 6);
			break;
		case "24h":
			from.setHours(from.getHours() - 24);
			break;
		case "7d":
			from.setDate(from.getDate() - 7);
			break;
		case "30d":
			from.setDate(from.getDate() - 30);
			break;
		default:
			from.setHours(from.getHours() - 1);
	}
	return { from, to };
}

/** Returns unix timestamps (seconds) for the given relative period string. */
export function getUnixRangeForPeriod(period: string): { start: number; end: number } {
	const { from, to } = getRangeForPeriod(period);
	return {
		start: Math.floor(from.getTime() / 1000),
		end: Math.floor(to.getTime() / 1000),
	};
}