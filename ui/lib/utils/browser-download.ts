const safeStringify = (value: unknown, space: number): string => {
	try {
		return JSON.stringify(value, null, space);
	} catch {
		const seen = new WeakSet();
		return JSON.stringify(
			value,
			(_key, val) => {
				if (typeof val === "bigint") return val.toString();
				if (typeof val === "object" && val !== null) {
					if (seen.has(val)) return "[Circular]";
					seen.add(val);
				}
				return val;
			},
			space,
		);
	}
};

export const downloadAsJson = (data: unknown, filename: string) => {
	const json = safeStringify(data, 2);
	const blob = new Blob([json], { type: "application/json" });
	const url = URL.createObjectURL(blob);
	const a = document.createElement("a");
	a.href = url;
	a.download = filename.endsWith(".json") ? filename : `${filename}.json`;
	document.body.appendChild(a);
	a.click();
	document.body.removeChild(a);
	setTimeout(() => URL.revokeObjectURL(url), 0);
};