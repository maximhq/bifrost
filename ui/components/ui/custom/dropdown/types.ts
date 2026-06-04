import { ReactNode } from "react";

export type DropdownOptionBase<T = {}> = {
	label?: string;
	icon?: ReactNode;
	view?: ReactNode; // Optional view for items
	onSelectItem?: (option: DropdownOption<T>) => void;
} & T;

export type DropdownItemOption<T = {}> = DropdownOptionBase<T> & {
	type?: "item";
	value: string; // Required for items
	description?: ReactNode;
	hidden?: boolean;
} & Record<string, any>;

export type DropdownGroupOption<T = {}> = DropdownOptionBase<T> & {
	type: "group";
	value?: never; // Not allowed for groups
	description?: string;
	options?: DropdownOption<T>[]; // Sub-options for groups
	hidden?: boolean;
};

export type DropdownOption<T = {}> = DropdownItemOption<T> | DropdownGroupOption<T>;

export type FlattenedDropdownOption<T = {}> = {
	option: DropdownOption<T>;
	groupIndex?: number;
	parentIndex: number;
};