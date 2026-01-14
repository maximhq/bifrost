export const parseArrayFromText = (text?: string): string[] => {
	if (!text) return [];
	return text
		.split(",")
		.map((item) => item.trim())
		.filter((item) => item.length > 0);
};

export const isArrayEqual = <T>(array1: T[], array2: T[]): boolean => {
	return array1?.length === array2?.length && array1?.every((value, index) => value === array2[index]);
};

export const isArrayOverlapping = <T>(array1: T[], array2: T[]): boolean => {
	return array1?.some((value) => array2.includes(value));
};
