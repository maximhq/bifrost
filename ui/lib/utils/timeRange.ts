export const TIME_PERIODS = [
  { label: "Last hour", value: "1h" },
  { label: "Last 6 hours", value: "6h" },
  { label: "Last 24 hours", value: "24h" },
  { label: "Last 7 days", value: "7d" },
  { label: "Last 30 days", value: "30d" },
];

export type TimePeriod = (typeof TIME_PERIODS)[number]["value"];

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

export function getUnixRangeForPeriod(period: string): { start: number; end: number } {
  const { from, to } = getRangeForPeriod(period);
  return { start: Math.floor(from.getTime() / 1000), end: Math.floor(to.getTime() / 1000) };
}
