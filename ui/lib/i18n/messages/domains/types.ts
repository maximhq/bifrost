export type DeepStringify<T> = {
	[K in keyof T]: T[K] extends string ? string : DeepStringify<T[K]>;
};